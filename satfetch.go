// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Package satfetch fetches Sentinel-2 imagery products by geo coordinates
// from free, keyless sources: a STAC catalog picks the most recent low-cloud
// scene and only the tiles of the scene's Cloud-Optimized GeoTIFFs that
// overlap the requested window are fetched, decoded and rendered. Pure Go,
// no GDAL, no system dependencies.
package satfetch

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/karamble/satfetch/internal/arcgis"
	"github.com/karamble/satfetch/internal/cache"
	"github.com/karamble/satfetch/internal/cog"
	"github.com/karamble/satfetch/internal/geo"
	"github.com/karamble/satfetch/internal/pcsign"
	"github.com/karamble/satfetch/internal/render"
	"github.com/karamble/satfetch/internal/tiffw"
	"github.com/karamble/satfetch/internal/tiles"
	"github.com/karamble/satfetch/internal/wms"
)

// Options configures a Service. Catalog and CacheDir are required.
type Options struct {
	Catalog             Catalog
	CacheDir            string
	CacheMaxMB          int           // default 2048, <0 disables the cap
	BuildTimeout        time.Duration // per product build, default 60s
	MaxConcurrentBuilds int           // default 4
	TileConcurrency     int           // parallel range requests per build, default 4
	HTTPClient          *http.Client  // pixel-data fetches
	Logger              *slog.Logger
	WMSSources          []WMSSource    // ortho sources; nil means BuiltinWMSSources()
	TileSources         []TileSource   // tile pyramids; nil means BuiltinTileSources()
	ArcGISSources       []ArcGISSource // ArcGIS exports; nil means BuiltinArcGISSources()
	STACSources         []STACSource   // STAC COG collections; nil means BuiltinSTACSources()
}

// Result describes a finished product sitting in the cache.
type Result struct {
	Path          string
	ContentType   string
	Scene         Scene   // zero for ortho products
	Source        string  // WMS source name, empty for scene products
	SourceGSD     float64 // native meters per pixel of the source, 0 when n/a
	CacheHit      bool
	Width, Height int   // 0 when unknown (GeoTIFF cache hits)
	BytesFetched  int64 // upstream bytes pulled for this call
}

// Service builds imagery products.
type Service struct {
	catalog  Catalog
	cache    *cache.Cache
	httpc    *http.Client
	log      *slog.Logger
	timeout  time.Duration
	tileConc int
	sem      chan struct{}
	wms      map[string]WMSSource
	tiles    map[string]TileSource
	arcgis   map[string]ArcGISSource
	stac     map[string]stacOrtho
	names    []string // all source names, sorted

	mu     sync.Mutex
	flight map[string]chan struct{}
}

// New creates a Service and starts the cache eviction loop; Close releases
// it.
func New(o Options) (*Service, error) {
	if o.Catalog == nil {
		return nil, fmt.Errorf("satfetch: catalog required")
	}
	if o.CacheDir == "" {
		return nil, fmt.Errorf("satfetch: cache directory required")
	}
	if o.CacheMaxMB == 0 {
		o.CacheMaxMB = 2048
	}
	if o.BuildTimeout <= 0 {
		o.BuildTimeout = 60 * time.Second
	}
	if o.MaxConcurrentBuilds <= 0 {
		o.MaxConcurrentBuilds = 4
	}
	if o.TileConcurrency <= 0 {
		o.TileConcurrency = 4
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.WMSSources == nil {
		o.WMSSources = BuiltinWMSSources()
	}
	if o.TileSources == nil {
		o.TileSources = BuiltinTileSources()
	}
	if o.ArcGISSources == nil {
		o.ArcGISSources = BuiltinArcGISSources()
	}
	if o.STACSources == nil {
		o.STACSources = BuiltinSTACSources()
	}
	wmsMap := make(map[string]WMSSource, len(o.WMSSources))
	tileMap := make(map[string]TileSource, len(o.TileSources))
	arcgisMap := make(map[string]ArcGISSource, len(o.ArcGISSources))
	stacMap := make(map[string]stacOrtho, len(o.STACSources))
	names := make([]string, 0, len(o.WMSSources)+len(o.TileSources)+len(o.ArcGISSources)+len(o.STACSources))
	// claim reserves a source name across every source type, so the
	// namespace the Ortho lookup walks stays unambiguous.
	claim := func(name string) error {
		for _, taken := range names {
			if taken == name {
				return fmt.Errorf("satfetch: duplicate source name %q", name)
			}
		}
		names = append(names, name)
		return nil
	}
	for _, src := range o.WMSSources {
		if src.Name == "" || src.BaseURL == "" {
			return nil, fmt.Errorf("satfetch: WMS source needs a name and base URL")
		}
		if src.MaxPx <= 0 {
			src.MaxPx = 4096
		}
		if err := claim(src.Name); err != nil {
			return nil, err
		}
		wmsMap[src.Name] = src
	}
	for _, src := range o.TileSources {
		if src.Name == "" || src.URLTemplate == "" {
			return nil, fmt.Errorf("satfetch: tile source needs a name and URL template")
		}
		if src.TileSize <= 0 {
			src.TileSize = 256
		}
		if src.MaxZoom <= 0 {
			src.MaxZoom = 19
		}
		if err := claim(src.Name); err != nil {
			return nil, err
		}
		tileMap[src.Name] = src
	}
	for _, src := range o.ArcGISSources {
		if src.Name == "" || src.BaseURL == "" {
			return nil, fmt.Errorf("satfetch: ArcGIS source needs a name and base URL")
		}
		if src.MaxPx <= 0 {
			src.MaxPx = 4096
		}
		if err := claim(src.Name); err != nil {
			return nil, err
		}
		arcgisMap[src.Name] = src
	}
	for _, src := range o.STACSources {
		if src.Name == "" || src.STACURL == "" || src.Collection == "" || src.Asset == "" {
			return nil, fmt.Errorf("satfetch: STAC source needs a name, STAC URL, collection and asset")
		}
		if src.MaxPx <= 0 {
			src.MaxPx = 4096
		}
		if src.Days <= 0 {
			src.Days = 3650
		}
		if err := claim(src.Name); err != nil {
			return nil, err
		}
		opts := EarthSearchOptions{
			BaseURL:       src.STACURL,
			Collection:    src.Collection,
			NoCloudFilter: true,
		}
		if src.SignTokenURL != "" {
			opts.HrefHook = pcsign.New(src.SignTokenURL, defaultUserAgent, nil).Sign
		}
		stacMap[src.Name] = stacOrtho{src: src, cat: NewEarthSearch(opts)}
	}
	sort.Strings(names)
	c, err := cache.New(o.CacheDir, o.CacheMaxMB, o.Logger)
	if err != nil {
		return nil, err
	}
	return &Service{
		catalog:  o.Catalog,
		cache:    c,
		httpc:    o.HTTPClient,
		log:      o.Logger,
		timeout:  o.BuildTimeout,
		tileConc: o.TileConcurrency,
		sem:      make(chan struct{}, o.MaxConcurrentBuilds),
		wms:      wmsMap,
		tiles:    tileMap,
		arcgis:   arcgisMap,
		stac:     stacMap,
		names:    names,
		flight:   make(map[string]chan struct{}),
	}, nil
}

// SourceInfo describes one configured ortho source.
type SourceInfo struct {
	Name        string
	Type        string // "wms" or "tiles"
	GSD         float64
	Attribution string
}

// SourceCatalog lists the configured ortho sources in name order.
func (s *Service) SourceCatalog() []SourceInfo {
	out := make([]SourceInfo, 0, len(s.names))
	for _, name := range s.names {
		if src, ok := s.wms[name]; ok {
			out = append(out, SourceInfo{Name: name, Type: "wms", GSD: src.GSD, Attribution: src.Attribution})
			continue
		}
		if src, ok := s.tiles[name]; ok {
			out = append(out, SourceInfo{Name: name, Type: "tiles", GSD: src.GSD, Attribution: src.Attribution})
			continue
		}
		if so, ok := s.stac[name]; ok {
			out = append(out, SourceInfo{Name: name, Type: "stac", GSD: so.src.GSD, Attribution: so.src.Attribution})
			continue
		}
		src := s.arcgis[name]
		out = append(out, SourceInfo{Name: name, Type: "arcgis", GSD: src.GSD, Attribution: src.Attribution})
	}
	return out
}

// Close stops background work.
func (s *Service) Close() { s.cache.Close() }

// Scenes lists catalog scenes covering the point, newest first.
func (s *Service) Scenes(ctx context.Context, r ScenesRequest) ([]Scene, error) {
	if err := r.normalize(); err != nil {
		return nil, err
	}
	return s.catalog.Search(ctx, Query{
		Lon: r.Lon, Lat: r.Lat, MaxCloud: r.MaxCloud, Days: r.Days, Limit: r.Limit,
	})
}

// Image builds the true-color product for the request.
func (s *Service) Image(ctx context.Context, r ImageRequest) (*Result, error) {
	if err := r.normalize(productImage); err != nil {
		return nil, err
	}
	return s.product(ctx, r, productImage)
}

// NDVI builds the vegetation-index product for the request.
func (s *Service) NDVI(ctx context.Context, r ImageRequest) (*Result, error) {
	if err := r.normalize(productNDVI); err != nil {
		return nil, err
	}
	return s.product(ctx, r, productNDVI)
}

func neededAssets(product string) []string {
	if product == productNDVI {
		return []string{"red", "nir"}
	}
	return []string{"visual"}
}

func hasAssets(sc Scene, keys []string) bool {
	for _, k := range keys {
		if sc.Assets[k] == "" {
			return false
		}
	}
	return true
}

func (s *Service) resolveScene(ctx context.Context, r ImageRequest, product string) (Scene, error) {
	need := neededAssets(product)
	if r.SceneID != "" {
		sc, err := s.catalog.Get(ctx, r.SceneID)
		if err != nil {
			return Scene{}, err
		}
		if !hasAssets(sc, need) {
			return Scene{}, fmt.Errorf("%w: scene %s lacks assets %v", ErrNoScene, sc.ID, need)
		}
		return sc, nil
	}
	scenes, err := s.catalog.Search(ctx, Query{
		Lon: r.Lon, Lat: r.Lat, MaxCloud: r.MaxCloud, Days: r.Days, Limit: 50,
	})
	if err != nil {
		return Scene{}, err
	}
	for _, sc := range scenes {
		if hasAssets(sc, need) {
			return sc, nil
		}
	}
	return Scene{}, fmt.Errorf("%w: point %.4f,%.4f cloud<%.0f%% last %dd",
		ErrNoScene, r.Lat, r.Lon, r.MaxCloud, r.Days)
}

func formatExt(f Format) string {
	switch f {
	case FormatJPEG:
		return "jpg"
	case FormatGTiff:
		return "tif"
	}
	return "png"
}

func formatContentType(f Format) string {
	switch f {
	case FormatJPEG:
		return "image/jpeg"
	case FormatGTiff:
		return "image/tiff"
	}
	return "image/png"
}

func (s *Service) product(ctx context.Context, r ImageRequest, product string) (*Result, error) {
	scene, err := s.resolveScene(ctx, r, product)
	if err != nil {
		return nil, err
	}
	quality := 0
	if r.Format == FormatJPEG {
		quality = r.JPEGQuality
	}
	key := cache.Key("v1", scene.ID, product,
		fmt.Sprintf("%.6f,%.6f", r.Lat, r.Lon),
		fmt.Sprintf("%.3f", r.SizeKM),
		fmt.Sprintf("%d", r.MaxPx),
		string(r.Format),
		fmt.Sprintf("%d", quality))
	ext := formatExt(r.Format)

	path, w, h, fetched, hit, err := s.buildCached(ctx, key, ext,
		func(ctx context.Context) (string, int, int, int64, error) {
			return s.build(ctx, r, product, scene, key, ext)
		})
	if err != nil {
		return nil, err
	}
	if hit {
		w, h = imageDims(path, r.Format)
	} else {
		s.log.Info("product built", "product", product, "scene", scene.ID,
			"size", fmt.Sprintf("%dx%d", w, h), "format", r.Format, "fetched_bytes", fetched)
	}
	return &Result{
		Path: path, ContentType: formatContentType(r.Format),
		Scene: scene, CacheHit: hit, Width: w, Height: h, BytesFetched: fetched,
	}, nil
}

// buildCached serves key/ext from the cache, or runs build exactly once
// across concurrent callers of the same key.
func (s *Service) buildCached(ctx context.Context, key, ext string,
	build func(context.Context) (string, int, int, int64, error)) (path string, w, h int, fetched int64, hit bool, err error) {

	for {
		if p, ok := s.cache.Get(key, ext); ok {
			return p, 0, 0, 0, true, nil
		}
		s.mu.Lock()
		if ch, ok := s.flight[key]; ok {
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return "", 0, 0, 0, false, ctx.Err()
			case <-ch:
			}
			continue
		}
		ch := make(chan struct{})
		s.flight[key] = ch
		s.mu.Unlock()

		path, w, h, fetched, err = build(ctx)
		s.mu.Lock()
		delete(s.flight, key)
		s.mu.Unlock()
		close(ch)
		return path, w, h, fetched, false, err
	}
}

// Ortho fetches an orthophoto window from a configured WMS or tile source.
func (s *Service) Ortho(ctx context.Context, r OrthoRequest) (*Result, error) {
	if err := r.normalize(); err != nil {
		return nil, err
	}
	if src, ok := s.wms[r.Source]; ok {
		return s.orthoWMS(ctx, r, src)
	}
	if src, ok := s.tiles[r.Source]; ok {
		return s.orthoTiles(ctx, r, src)
	}
	if src, ok := s.arcgis[r.Source]; ok {
		return s.orthoArcGIS(ctx, r, src)
	}
	if so, ok := s.stac[r.Source]; ok {
		return s.orthoSTAC(ctx, r, so)
	}
	return nil, fmt.Errorf("%w: unknown source %q (available: %s)",
		ErrInvalid, r.Source, strings.Join(s.names, ", "))
}

// stacOrtho pairs a STAC orthophoto source with the catalog client bound to
// its collection.
type stacOrtho struct {
	src STACSource
	cat *EarthSearch
}

// resolveSTACItem picks the newest item covering the point that carries the
// source's COG asset.
func (s *Service) resolveSTACItem(ctx context.Context, r OrthoRequest, so stacOrtho) (Scene, error) {
	scenes, err := so.cat.Search(ctx, Query{
		Lon: r.Lon, Lat: r.Lat, MaxCloud: 100, Days: so.src.Days, Limit: 50,
	})
	if err != nil {
		return Scene{}, err
	}
	for _, sc := range scenes {
		if sc.Assets[so.src.Asset] != "" {
			return sc, nil
		}
	}
	return Scene{}, fmt.Errorf("%w: source %s covers no imagery at %.4f,%.4f",
		ErrNoScene, so.src.Name, r.Lat, r.Lon)
}

// orthoSTAC serves an orthophoto window by reading the COG of the newest
// catalog item covering the point. Unlike the rendered sources the item is
// resolved before the cache lookup, so a fresh flight year produces a fresh
// key rather than serving stale pixels.
func (s *Service) orthoSTAC(ctx context.Context, r OrthoRequest, so stacOrtho) (*Result, error) {
	px := min(r.Px, so.src.MaxPx)
	scene, err := s.resolveSTACItem(ctx, r, so)
	if err != nil {
		return nil, err
	}
	key := cache.Key("v1-ortho", so.src.Name, scene.ID,
		fmt.Sprintf("%.6f,%.6f", r.Lat, r.Lon),
		fmt.Sprintf("%.3f", r.SizeKM),
		fmt.Sprintf("%d", px),
		string(r.Format))
	ext := formatExt(r.Format)

	path, w, h, fetched, hit, err := s.buildCached(ctx, key, ext,
		func(ctx context.Context) (string, int, int, int64, error) {
			return s.buildOrthoSTAC(ctx, r, so, scene, px, key, ext)
		})
	if err != nil {
		return nil, err
	}
	if hit {
		w, h = imageDims(path, r.Format)
	} else {
		s.log.Info("ortho built", "source", so.src.Name, "item", scene.ID,
			"size", fmt.Sprintf("%dx%d", w, h), "format", r.Format, "fetched_bytes", fetched)
	}
	gsd := scene.GSD
	if gsd == 0 {
		gsd = so.src.GSD
	}
	return &Result{
		Path: path, ContentType: formatContentType(r.Format),
		Scene: scene, Source: so.src.Name, SourceGSD: gsd,
		CacheHit: hit, Width: w, Height: h, BytesFetched: fetched,
	}, nil
}

func (s *Service) buildOrthoSTAC(ctx context.Context, r OrthoRequest, so stacOrtho, scene Scene, px int, key, ext string) (string, int, int, int64, error) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return "", 0, 0, 0, ctx.Err()
	}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	f, src, err := s.openAsset(ctx, scene, so.src.Asset)
	if err != nil {
		return "", 0, 0, 0, err
	}
	level, x0, y0, w, h, _, err := s.window(f, scene, r.Lat, r.Lon, r.SizeKM, px)
	if err != nil {
		return "", 0, 0, src.BytesFetched(), err
	}
	ras, err := f.ReadWindow(ctx, level, x0, y0, w, h, s.tileConc)
	if err != nil {
		return "", 0, 0, src.BytesFetched(), fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	img, err := render.TrueColor(ras)
	if err != nil {
		return "", 0, 0, src.BytesFetched(), err
	}
	writeOut := func(out io.Writer) error { return render.EncodePNG(out, img) }
	if r.Format == FormatJPEG {
		writeOut = func(out io.Writer) error { return render.EncodeJPEG(out, img, 85) }
	}
	path, err := s.cache.Put(key, ext, writeOut)
	if err != nil {
		return "", 0, 0, src.BytesFetched(), err
	}
	return path, ras.W, ras.H, src.BytesFetched(), nil
}

func (s *Service) orthoArcGIS(ctx context.Context, r OrthoRequest, src ArcGISSource) (*Result, error) {
	px := min(r.Px, src.MaxPx)
	key := cache.Key("v1-ortho", src.Name,
		fmt.Sprintf("%.6f,%.6f", r.Lat, r.Lon),
		fmt.Sprintf("%.3f", r.SizeKM),
		fmt.Sprintf("%d", px),
		string(r.Format))
	ext := formatExt(r.Format)

	path, _, _, fetched, hit, err := s.buildCached(ctx, key, ext,
		func(ctx context.Context) (string, int, int, int64, error) {
			return s.buildOrthoArcGIS(ctx, r, src, px, key, ext)
		})
	if err != nil {
		return nil, err
	}
	if !hit {
		s.log.Info("ortho built", "source", src.Name, "px", px,
			"format", r.Format, "fetched_bytes", fetched)
	}
	return &Result{
		Path: path, ContentType: formatContentType(r.Format),
		Source: src.Name, SourceGSD: src.GSD,
		CacheHit: hit, Width: px, Height: px, BytesFetched: fetched,
	}, nil
}

func (s *Service) buildOrthoArcGIS(ctx context.Context, r OrthoRequest, src ArcGISSource, px int, key, ext string) (string, int, int, int64, error) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return "", 0, 0, 0, ctx.Err()
	}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	dLat, dLon, err := geo.AOIDegrees(r.Lat, r.Lon, r.SizeKM)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var fetched int64
	path, err := s.cache.Put(key, ext, func(w io.Writer) error {
		n, err := arcgis.Fetch(ctx, s.httpc, defaultUserAgent, arcgis.Request{
			BaseURL: src.BaseURL,
			MinLat:  r.Lat - dLat,
			MinLon:  r.Lon - dLon,
			MaxLat:  r.Lat + dLat,
			MaxLon:  r.Lon + dLon,
			Px:      px,
			MIME:    formatContentType(r.Format),
		}, w)
		fetched = n
		if err != nil {
			return fmt.Errorf("%w: source %s: %v", ErrUpstream, src.Name, err)
		}
		return nil
	})
	if err != nil {
		return "", 0, 0, fetched, err
	}
	return path, px, px, fetched, nil
}

func (s *Service) orthoWMS(ctx context.Context, r OrthoRequest, src WMSSource) (*Result, error) {
	px := min(r.Px, src.MaxPx)
	key := cache.Key("v1-ortho", src.Name,
		fmt.Sprintf("%.6f,%.6f", r.Lat, r.Lon),
		fmt.Sprintf("%.3f", r.SizeKM),
		fmt.Sprintf("%d", px),
		string(r.Format))
	ext := formatExt(r.Format)

	path, _, _, fetched, hit, err := s.buildCached(ctx, key, ext,
		func(ctx context.Context) (string, int, int, int64, error) {
			return s.buildOrtho(ctx, r, src, px, key, ext)
		})
	if err != nil {
		return nil, err
	}
	if !hit {
		s.log.Info("ortho built", "source", src.Name, "px", px,
			"format", r.Format, "fetched_bytes", fetched)
	}
	return &Result{
		Path: path, ContentType: formatContentType(r.Format),
		Source: src.Name, SourceGSD: src.GSD,
		CacheHit: hit, Width: px, Height: px, BytesFetched: fetched,
	}, nil
}

// pickZoom returns the deepest zoom level whose output for the request
// stays within px pixels per side.
func pickZoom(src TileSource, lat, sizeKM float64, px int) int {
	sizeM := sizeKM * 1000
	for z := src.MaxZoom; z > 0; z-- {
		if sizeM/tiles.MetersPerPixel(lat, z) <= float64(px) {
			return z
		}
	}
	return 0
}

func (s *Service) orthoTiles(ctx context.Context, r OrthoRequest, src TileSource) (*Result, error) {
	zoom := pickZoom(src, r.Lat, r.SizeKM, r.Px)
	key := cache.Key("v1-ortho", src.Name,
		fmt.Sprintf("%.6f,%.6f", r.Lat, r.Lon),
		fmt.Sprintf("%.3f", r.SizeKM),
		fmt.Sprintf("%d", r.Px),
		string(r.Format))
	ext := formatExt(r.Format)

	path, w, h, fetched, hit, err := s.buildCached(ctx, key, ext,
		func(ctx context.Context) (string, int, int, int64, error) {
			return s.buildOrthoTiles(ctx, r, src, zoom, key, ext)
		})
	if err != nil {
		return nil, err
	}
	if hit {
		w, h = imageDims(path, r.Format)
	} else {
		s.log.Info("ortho built", "source", src.Name, "zoom", zoom,
			"size", fmt.Sprintf("%dx%d", w, h), "format", r.Format, "fetched_bytes", fetched)
	}
	return &Result{
		Path: path, ContentType: formatContentType(r.Format),
		Source: src.Name, SourceGSD: src.GSD,
		CacheHit: hit, Width: w, Height: h, BytesFetched: fetched,
	}, nil
}

func (s *Service) buildOrthoTiles(ctx context.Context, r OrthoRequest, src TileSource, zoom int, key, ext string) (string, int, int, int64, error) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return "", 0, 0, 0, ctx.Err()
	}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	dLat, dLon, err := geo.AOIDegrees(r.Lat, r.Lon, r.SizeKM)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	img, fetched, err := tiles.Fetch(ctx, s.httpc, defaultUserAgent, tiles.Request{
		URLTemplate: src.URLTemplate,
		MinLat:      r.Lat - dLat,
		MinLon:      r.Lon - dLon,
		MaxLat:      r.Lat + dLat,
		MaxLon:      r.Lon + dLon,
		Zoom:        zoom,
		TileSize:    src.TileSize,
		Concurrency: s.tileConc,
	})
	if err != nil {
		return "", 0, 0, fetched, fmt.Errorf("%w: source %s: %v", ErrUpstream, src.Name, err)
	}
	writeOut := func(out io.Writer) error { return render.EncodePNG(out, img) }
	if r.Format == FormatJPEG {
		writeOut = func(out io.Writer) error { return render.EncodeJPEG(out, img, 85) }
	}
	path, err := s.cache.Put(key, ext, writeOut)
	if err != nil {
		return "", 0, 0, fetched, err
	}
	b := img.Bounds()
	return path, b.Dx(), b.Dy(), fetched, nil
}

func (s *Service) buildOrtho(ctx context.Context, r OrthoRequest, src WMSSource, px int, key, ext string) (string, int, int, int64, error) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return "", 0, 0, 0, ctx.Err()
	}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	dLat, dLon, err := geo.AOIDegrees(r.Lat, r.Lon, r.SizeKM)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var fetched int64
	path, err := s.cache.Put(key, ext, func(w io.Writer) error {
		n, err := wms.Fetch(ctx, s.httpc, defaultUserAgent, wms.Request{
			BaseURL: src.BaseURL,
			Layers:  src.Layers,
			Version: src.Version,
			CRS:     src.CRS,
			MinLat:  r.Lat - dLat,
			MinLon:  r.Lon - dLon,
			MaxLat:  r.Lat + dLat,
			MaxLon:  r.Lon + dLon,
			Px:      px,
			MIME:    formatContentType(r.Format),
		}, w)
		fetched = n
		if err != nil {
			return fmt.Errorf("%w: source %s: %v", ErrUpstream, src.Name, err)
		}
		return nil
	})
	if err != nil {
		return "", 0, 0, fetched, err
	}
	return path, px, px, fetched, nil
}

func (s *Service) build(ctx context.Context, r ImageRequest, product string, scene Scene, key, ext string) (string, int, int, int64, error) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return "", 0, 0, 0, ctx.Err()
	}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if product == productNDVI {
		return s.buildNDVI(ctx, r, scene, key, ext)
	}
	return s.buildImage(ctx, r, scene, key, ext)
}

func (s *Service) openAsset(ctx context.Context, scene Scene, asset string) (*cog.File, *cog.HTTPSource, error) {
	src := cog.NewHTTPSource(scene.Assets[asset], s.httpc, defaultUserAgent)
	f, err := cog.Open(ctx, src)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s asset of %s: %v", ErrUpstream, asset, scene.ID, err)
	}
	return f, src, nil
}

// window derives the pixel window of a sizeKM square centered on lat/lon on
// the file, choosing the overview level that keeps the output within maxPx.
func (s *Service) window(f *cog.File, scene Scene, lat, lon, sizeKM float64, maxPx int) (level, x0, y0, w, h int, lg cog.Grid, err error) {
	epsg := f.Grid.EPSG
	if epsg == 0 {
		epsg = scene.EPSG
	}
	if epsg == 0 {
		return 0, 0, 0, 0, 0, lg, fmt.Errorf("%w: scene %s has no CRS", ErrUpstream, scene.ID)
	}
	minX, minY, maxX, maxY, err := geo.AOIBBox(epsg, lat, lon, sizeKM)
	if err != nil {
		return 0, 0, 0, 0, 0, lg, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	full := f.IFDs[0]
	_, _, fw, fh, ok := f.Grid.Window(full.Width, full.Height, minX, minY, maxX, maxY)
	if !ok {
		return 0, 0, 0, 0, 0, lg, ErrOutsideScene
	}
	level = f.PickLevel(maxPx, fw, fh)
	lg = f.LevelGrid(level)
	x0, y0, w, h, ok = lg.Window(f.IFDs[level].Width, f.IFDs[level].Height, minX, minY, maxX, maxY)
	if !ok {
		return 0, 0, 0, 0, 0, lg, ErrOutsideScene
	}
	return level, x0, y0, w, h, lg, nil
}

func tiffGeo(f *cog.File, lg cog.Grid, x0, y0 int, nodata string) tiffw.Geo {
	return tiffw.Geo{
		ScaleX:  lg.ScaleX,
		ScaleY:  lg.ScaleY,
		OriginX: lg.OriginX + float64(x0)*lg.ScaleX,
		OriginY: lg.OriginY - float64(y0)*lg.ScaleY,
		KeyDir:  f.KeyDir,
		Ascii:   f.GeoAscii,
		NoData:  nodata,
	}
}

func (s *Service) buildImage(ctx context.Context, r ImageRequest, scene Scene, key, ext string) (string, int, int, int64, error) {
	f, src, err := s.openAsset(ctx, scene, "visual")
	if err != nil {
		return "", 0, 0, 0, err
	}
	level, x0, y0, w, h, lg, err := s.window(f, scene, r.Lat, r.Lon, r.SizeKM, r.MaxPx)
	if err != nil {
		return "", 0, 0, src.BytesFetched(), err
	}
	ras, err := f.ReadWindow(ctx, level, x0, y0, w, h, s.tileConc)
	if err != nil {
		return "", 0, 0, src.BytesFetched(), fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	var writeOut func(io.Writer) error
	switch r.Format {
	case FormatGTiff:
		g := tiffGeo(f, lg, x0, y0, f.NoData)
		writeOut = func(out io.Writer) error {
			return tiffw.WriteRGB8(out, ras.W, ras.H, ras.U8, g)
		}
	default:
		img, err := render.TrueColor(ras)
		if err != nil {
			return "", 0, 0, src.BytesFetched(), err
		}
		if r.Format == FormatJPEG {
			writeOut = func(out io.Writer) error { return render.EncodeJPEG(out, img, r.JPEGQuality) }
		} else {
			writeOut = func(out io.Writer) error { return render.EncodePNG(out, img) }
		}
	}
	path, err := s.cache.Put(key, ext, writeOut)
	if err != nil {
		return "", 0, 0, src.BytesFetched(), err
	}
	return path, ras.W, ras.H, src.BytesFetched(), nil
}

func (s *Service) buildNDVI(ctx context.Context, r ImageRequest, scene Scene, key, ext string) (string, int, int, int64, error) {
	fRed, srcRed, err := s.openAsset(ctx, scene, "red")
	if err != nil {
		return "", 0, 0, 0, err
	}
	fNir, srcNir, err := s.openAsset(ctx, scene, "nir")
	if err != nil {
		return "", 0, 0, srcRed.BytesFetched(), err
	}
	fetched := func() int64 { return srcRed.BytesFetched() + srcNir.BytesFetched() }
	level, x0, y0, w, h, lg, err := s.window(fRed, scene, r.Lat, r.Lon, r.SizeKM, r.MaxPx)
	if err != nil {
		return "", 0, 0, fetched(), err
	}
	if level >= len(fNir.IFDs) ||
		fNir.IFDs[level].Width != fRed.IFDs[level].Width ||
		fNir.IFDs[level].Height != fRed.IFDs[level].Height {
		return "", 0, 0, fetched(), fmt.Errorf("%w: red and nir grids of %s differ", ErrUpstream, scene.ID)
	}
	redRas, err := fRed.ReadWindow(ctx, level, x0, y0, w, h, s.tileConc)
	if err != nil {
		return "", 0, 0, fetched(), fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	nirRas, err := fNir.ReadWindow(ctx, level, x0, y0, w, h, s.tileConc)
	if err != nil {
		return "", 0, 0, fetched(), fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	vals, err := render.NDVI(redRas, nirRas)
	if err != nil {
		return "", 0, 0, fetched(), err
	}
	var writeOut func(io.Writer) error
	if r.Format == FormatGTiff {
		g := tiffGeo(fRed, lg, x0, y0, "nan")
		writeOut = func(out io.Writer) error {
			return tiffw.WriteFloat32(out, w, h, vals, g)
		}
	} else {
		img, err := render.NDVIImage(vals, w, h)
		if err != nil {
			return "", 0, 0, fetched(), err
		}
		writeOut = func(out io.Writer) error { return render.EncodePNG(out, img) }
	}
	path, err := s.cache.Put(key, ext, writeOut)
	if err != nil {
		return "", 0, 0, fetched(), err
	}
	return path, w, h, fetched(), nil
}

// imageDims reads the dimensions of a cached png/jpeg without decoding the
// pixels. GeoTIFFs report 0 (callers can parse the file if they need it).
func imageDims(path string, f Format) (int, int) {
	if f == FormatGTiff {
		return 0, 0
	}
	fh, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer fh.Close()
	cfg, _, err := image.DecodeConfig(fh)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}
