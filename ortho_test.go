// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package satfetch

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

type stubCatalog struct{}

func (stubCatalog) Search(context.Context, Query) ([]Scene, error) { return nil, nil }
func (stubCatalog) Get(context.Context, string) (Scene, error)     { return Scene{}, ErrNoScene }

func TestOrthoRequestNormalize(t *testing.T) {
	tests := []struct {
		name    string
		req     OrthoRequest
		wantErr bool
	}{
		{"defaults fill in", OrthoRequest{Lat: 50, Lon: 19, Source: "pl"}, false},
		{"missing source", OrthoRequest{Lat: 50, Lon: 19}, true},
		{"size too large", OrthoRequest{Lat: 50, Lon: 19, Source: "pl", SizeKM: 20}, true},
		{"size too small", OrthoRequest{Lat: 50, Lon: 19, Source: "pl", SizeKM: 0.01}, true},
		{"px too small", OrthoRequest{Lat: 50, Lon: 19, Source: "pl", Px: 32}, true},
		{"px too large", OrthoRequest{Lat: 50, Lon: 19, Source: "pl", Px: 8192}, true},
		{"gtiff rejected", OrthoRequest{Lat: 50, Lon: 19, Source: "pl", Format: FormatGTiff}, true},
		{"unknown format", OrthoRequest{Lat: 50, Lon: 19, Source: "pl", Format: "bmp"}, true},
		{"bad latitude", OrthoRequest{Lat: 91, Lon: 19, Source: "pl"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.normalize()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil {
				if tc.req.SizeKM != 1 || tc.req.Px != 1024 || tc.req.Format != FormatJPEG {
					t.Errorf("defaults not applied: %+v", tc.req)
				}
			} else if !errors.Is(err, ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", err)
			}
		})
	}
}

func TestOrtho(t *testing.T) {
	payload := []byte("FAKEJPEG")
	var lastQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(payload)
	}))
	defer ts.Close()

	svc, err := New(Options{
		Catalog:  stubCatalog{},
		CacheDir: t.TempDir(),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		WMSSources: []WMSSource{
			{Name: "test", BaseURL: ts.URL, Layers: "L", GSD: 0.25, MaxPx: 2048},
		},
		TileSources:   []TileSource{},
		ArcGISSources: []ArcGISSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	res, err := svc.Ortho(context.Background(), OrthoRequest{
		Lat: 50.2649, Lon: 19.0238, SizeKM: 0.5, Source: "test", Px: 512,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheHit || res.Source != "test" || res.SourceGSD != 0.25 ||
		res.Width != 512 || res.Height != 512 || res.ContentType != "image/jpeg" {
		t.Errorf("result %+v", res)
	}
	if res.BytesFetched != int64(len(payload)) {
		t.Errorf("fetched %d bytes, want %d", res.BytesFetched, len(payload))
	}
	if res.Scene.ID != "" {
		t.Errorf("scene must be empty for ortho, have %q", res.Scene.ID)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("cached bytes differ from the WMS response")
	}
	if !strings.Contains(lastQuery, "WIDTH=512") {
		t.Errorf("query %q missing width", lastQuery)
	}

	res, err = svc.Ortho(context.Background(), OrthoRequest{
		Lat: 50.2649, Lon: 19.0238, SizeKM: 0.5, Source: "test", Px: 512,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.CacheHit {
		t.Error("second identical call must hit the cache")
	}

	_, err = svc.Ortho(context.Background(), OrthoRequest{Lat: 50, Lon: 19, Source: "nope"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "available: test") {
		t.Errorf("unknown source error %v", err)
	}
}

// tileFixture serves solid-color 256px PNG tiles whose red channel encodes
// the tile column and green channel the row.
func tileFixture(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 3 {
			http.NotFound(w, r)
			return
		}
		x, _ := strconv.Atoi(parts[1])
		y, _ := strconv.Atoi(parts[2])
		img := image.NewNRGBA(image.Rect(0, 0, 256, 256))
		for i := 0; i < 256*256; i++ {
			img.Pix[i*4+0] = uint8(x % 256)
			img.Pix[i*4+1] = uint8(y % 256)
			img.Pix[i*4+3] = 0xff
		}
		w.Header().Set("Content-Type", "image/png")
		png.Encode(w, img)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestOrthoTiles(t *testing.T) {
	ts := tileFixture(t)
	svc, err := New(Options{
		Catalog:    stubCatalog{},
		CacheDir:   t.TempDir(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		WMSSources: []WMSSource{},
		TileSources: []TileSource{
			{Name: "tt", URLTemplate: ts.URL + "/{z}/{x}/{y}", GSD: 0.3, MaxZoom: 19},
		},
		ArcGISSources: []ArcGISSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	res, err := svc.Ortho(context.Background(), OrthoRequest{
		Lat: 50.2649, Lon: 19.0238, SizeKM: 0.5, Source: "tt", Px: 512, Format: FormatPNG,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheHit || res.Source != "tt" || res.ContentType != "image/png" {
		t.Errorf("result %+v", res)
	}
	if res.Width <= 256 || res.Width > 512 || res.Height <= 256 || res.Height > 512 {
		t.Errorf("dimensions %dx%d, want within (256,512]", res.Width, res.Height)
	}
	f, err := os.Open(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != res.Width {
		t.Errorf("file width %d, result width %d", img.Bounds().Dx(), res.Width)
	}

	res, err = svc.Ortho(context.Background(), OrthoRequest{
		Lat: 50.2649, Lon: 19.0238, SizeKM: 0.5, Source: "tt", Px: 512, Format: FormatPNG,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.CacheHit {
		t.Error("second identical call must hit the cache")
	}

	res, err = svc.Ortho(context.Background(), OrthoRequest{
		Lat: 50.2649, Lon: 19.0238, SizeKM: 0.5, Source: "tt", Px: 512,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentType != "image/jpeg" {
		t.Errorf("default format content type %s", res.ContentType)
	}
}

func TestOrthoDefaultRegistry(t *testing.T) {
	svc, err := New(Options{
		Catalog:  stubCatalog{},
		CacheDir: t.TempDir(),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	catalog := svc.SourceCatalog()
	if want := len(BuiltinWMSSources()) + len(BuiltinTileSources()) + len(BuiltinArcGISSources()); len(catalog) != want {
		t.Fatalf("builtin sources %d, want %d", len(catalog), want)
	}
	seen := make(map[string]string)
	prev := ""
	for _, src := range catalog {
		if src.Name == "" || src.GSD <= 0 || src.Attribution == "" {
			t.Errorf("incomplete source %+v", src)
		}
		if src.Type != "wms" && src.Type != "tiles" && src.Type != "arcgis" {
			t.Errorf("source %q has type %q", src.Name, src.Type)
		}
		if _, dup := seen[src.Name]; dup {
			t.Errorf("duplicate source name %q", src.Name)
		}
		seen[src.Name] = src.Type
		if src.Name < prev {
			t.Errorf("catalog not sorted: %q after %q", src.Name, prev)
		}
		prev = src.Name
	}
	for name, typ := range map[string]string{
		"pl": "wms", "nl": "wms", "fr": "wms", "ch": "wms", "es": "wms", "us": "wms",
		"de-bb": "wms", "be-wa": "wms", "es-ct": "wms",
		"at": "tiles", "cz": "tiles", "ee": "tiles", "jp": "tiles", "tw": "tiles", "za": "tiles",
		"si": "arcgis", "au-nsw": "arcgis",
	} {
		if seen[name] != typ {
			t.Errorf("source %q has type %q, want %q", name, seen[name], typ)
		}
	}
}

func TestDuplicateSourceName(t *testing.T) {
	_, err := New(Options{
		Catalog:  stubCatalog{},
		CacheDir: t.TempDir(),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		WMSSources: []WMSSource{
			{Name: "dup", BaseURL: "http://a", Layers: "L", GSD: 1, Attribution: "a"},
		},
		TileSources: []TileSource{
			{Name: "dup", URLTemplate: "http://b/{z}/{x}/{y}", GSD: 1, Attribution: "b"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate source name") {
		t.Errorf("error %v, want duplicate-name rejection", err)
	}
	_, err = New(Options{
		Catalog:  stubCatalog{},
		CacheDir: t.TempDir(),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		WMSSources: []WMSSource{
			{Name: "dup", BaseURL: "http://a", Layers: "L", GSD: 1, Attribution: "a"},
		},
		TileSources: []TileSource{},
		ArcGISSources: []ArcGISSource{
			{Name: "dup", BaseURL: "http://c/MapServer", GSD: 1, Attribution: "c"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate source name") {
		t.Errorf("error %v, want duplicate-name rejection across arcgis", err)
	}
}

func TestOrthoArcGIS(t *testing.T) {
	payload := []byte("FAKEJPEG")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/export") {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(payload)
	}))
	defer ts.Close()

	svc, err := New(Options{
		Catalog:     stubCatalog{},
		CacheDir:    t.TempDir(),
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		WMSSources:  []WMSSource{},
		TileSources: []TileSource{},
		ArcGISSources: []ArcGISSource{
			{Name: "ta", BaseURL: ts.URL + "/rest/services/DOF/MapServer", GSD: 0.26},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	res, err := svc.Ortho(context.Background(), OrthoRequest{
		Lat: 46.0510, Lon: 14.5058, SizeKM: 0.5, Source: "ta", Px: 512,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheHit || res.Source != "ta" || res.SourceGSD != 0.26 ||
		res.Width != 512 || res.ContentType != "image/jpeg" {
		t.Errorf("result %+v", res)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("cached bytes differ from the export response")
	}

	res, err = svc.Ortho(context.Background(), OrthoRequest{
		Lat: 46.0510, Lon: 14.5058, SizeKM: 0.5, Source: "ta", Px: 512,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.CacheHit {
		t.Error("second identical call must hit the cache")
	}
}
