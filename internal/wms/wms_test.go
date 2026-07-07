// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package wms

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func testRequest(base string) Request {
	return Request{
		BaseURL: base,
		Layers:  "Raster",
		MinLat:  50.1, MinLon: 19.2, MaxLat: 50.3, MaxLon: 19.4,
		Px:   512,
		MIME: "image/jpeg",
	}
}

func TestFetchQuery(t *testing.T) {
	payload := []byte("JPEGDATA")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		want := map[string]string{
			"SERVICE": "WMS",
			"VERSION": "1.3.0",
			"REQUEST": "GetMap",
			"LAYERS":  "Raster",
			"STYLES":  "",
			"CRS":     "EPSG:4326",
			"BBOX":    "50.100000,19.200000,50.300000,19.400000",
			"WIDTH":   "512",
			"HEIGHT":  "512",
			"FORMAT":  "image/jpeg",
		}
		for k, v := range want {
			if got := q.Get(k); got != v {
				t.Errorf("param %s = %q, want %q", k, got, v)
			}
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(payload)
	}))
	defer ts.Close()

	var buf bytes.Buffer
	n, err := Fetch(context.Background(), ts.Client(), "test-agent", testRequest(ts.URL), &buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(payload)) || !bytes.Equal(buf.Bytes(), payload) {
		t.Errorf("got %d bytes %q", n, buf.Bytes())
	}
}

func TestFetchServiceException(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte("<ServiceExceptionReport>layer misconfigured</ServiceExceptionReport>"))
	}))
	defer ts.Close()

	var buf bytes.Buffer
	_, err := Fetch(context.Background(), ts.Client(), "", testRequest(ts.URL), &buf)
	if err == nil || !strings.Contains(err.Error(), "ServiceException") {
		t.Fatalf("error %v, want service exception detail", err)
	}
	if calls.Load() != 1 {
		t.Errorf("attempts %d, want 1 (not retryable)", calls.Load())
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %d bytes on failure", buf.Len())
	}
}

func TestFetchRetries(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			http.Error(w, "busy", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNG"))
	}))
	defer ts.Close()

	var buf bytes.Buffer
	if _, err := Fetch(context.Background(), ts.Client(), "", testRequest(ts.URL), &buf); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("attempts %d, want 3", calls.Load())
	}
}

func TestFetchRetriesExhausted(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer ts.Close()

	var buf bytes.Buffer
	if _, err := Fetch(context.Background(), ts.Client(), "", testRequest(ts.URL), &buf); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 4 {
		t.Errorf("attempts %d, want 4", calls.Load())
	}
}

func TestFetchNoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "bad layer", http.StatusBadRequest)
	}))
	defer ts.Close()

	var buf bytes.Buffer
	if _, err := Fetch(context.Background(), ts.Client(), "", testRequest(ts.URL), &buf); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Errorf("attempts %d, want 1", calls.Load())
	}
}
