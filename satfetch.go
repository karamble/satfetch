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
	"sync"
	"time"

	"github.com/karamble/satfetch/internal/cache"
	"github.com/karamble/satfetch/internal/cog"
	"github.com/karamble/satfetch/internal/geo"
	"github.com/karamble/satfetch/internal/render"
	"github.com/karamble/satfetch/internal/tiffw"
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
}

// Result describes a finished product sitting in the cache.
type Result struct {
	Path          string
	ContentType   string
	Scene         Scene
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
		flight:   make(map[string]chan struct{}),
	}, nil
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

	for {
		if p, ok := s.cache.Get(key, ext); ok {
			w, h := imageDims(p, r.Format)
			return &Result{
				Path: p, ContentType: formatContentType(r.Format),
				Scene: scene, CacheHit: true, Width: w, Height: h,
			}, nil
		}
		s.mu.Lock()
		if ch, ok := s.flight[key]; ok {
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ch:
			}
			continue
		}
		ch := make(chan struct{})
		s.flight[key] = ch
		s.mu.Unlock()

		path, w, h, fetched, err := s.build(ctx, r, product, scene, key, ext)
		s.mu.Lock()
		delete(s.flight, key)
		s.mu.Unlock()
		close(ch)
		if err != nil {
			return nil, err
		}
		s.log.Info("product built", "product", product, "scene", scene.ID,
			"size", fmt.Sprintf("%dx%d", w, h), "format", r.Format, "fetched_bytes", fetched)
		return &Result{
			Path: path, ContentType: formatContentType(r.Format),
			Scene: scene, CacheHit: false, Width: w, Height: h, BytesFetched: fetched,
		}, nil
	}
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

// window derives the pixel window of the request on the file, choosing the
// overview level that keeps the output within MaxPx.
func (s *Service) window(f *cog.File, scene Scene, r ImageRequest) (level, x0, y0, w, h int, lg cog.Grid, err error) {
	epsg := f.Grid.EPSG
	if epsg == 0 {
		epsg = scene.EPSG
	}
	if epsg == 0 {
		return 0, 0, 0, 0, 0, lg, fmt.Errorf("%w: scene %s has no CRS", ErrUpstream, scene.ID)
	}
	minX, minY, maxX, maxY, err := geo.AOIBBox(epsg, r.Lat, r.Lon, r.SizeKM)
	if err != nil {
		return 0, 0, 0, 0, 0, lg, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	full := f.IFDs[0]
	_, _, fw, fh, ok := f.Grid.Window(full.Width, full.Height, minX, minY, maxX, maxY)
	if !ok {
		return 0, 0, 0, 0, 0, lg, ErrOutsideScene
	}
	level = f.PickLevel(r.MaxPx, fw, fh)
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
	level, x0, y0, w, h, lg, err := s.window(f, scene, r)
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
	level, x0, y0, w, h, lg, err := s.window(fRed, scene, r)
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
