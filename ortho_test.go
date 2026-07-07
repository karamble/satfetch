// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package satfetch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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

	catalog := svc.WMSCatalog()
	if len(catalog) != len(BuiltinWMSSources()) {
		t.Fatalf("builtin sources %d, want %d", len(catalog), len(BuiltinWMSSources()))
	}
	seen := make(map[string]bool)
	prev := ""
	for _, src := range catalog {
		if src.Name == "" || src.BaseURL == "" || src.Layers == "" ||
			src.GSD <= 0 || src.Attribution == "" || src.MaxPx <= 0 {
			t.Errorf("incomplete source %+v", src)
		}
		if seen[src.Name] {
			t.Errorf("duplicate source name %q", src.Name)
		}
		seen[src.Name] = true
		if src.Name < prev {
			t.Errorf("catalog not sorted: %q after %q", src.Name, prev)
		}
		prev = src.Name
	}
	for _, want := range []string{"pl", "nl", "fr", "ch", "es", "us"} {
		if !seen[want] {
			t.Errorf("missing builtin source %q", want)
		}
	}
}
