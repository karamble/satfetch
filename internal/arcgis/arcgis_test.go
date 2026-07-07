// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package arcgis

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
		MinLat:  46.0490, MinLon: 14.5020, MaxLat: 46.0530, MaxLon: 14.5085,
		Px:   512,
		MIME: "image/jpeg",
	}
}

func TestFetchQuery(t *testing.T) {
	payload := []byte("JPEGDATA")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/export") {
			t.Errorf("path %s, want /export", r.URL.Path)
		}
		q := r.URL.Query()
		want := map[string]string{
			"bbox":        "14.502000,46.049000,14.508500,46.053000",
			"bboxSR":      "4326",
			"size":        "512,512",
			"format":      "jpg",
			"transparent": "false",
			"f":           "image",
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

func TestFetchPNGFormat(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("format"); got != "png" {
			t.Errorf("format %q, want png", got)
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNG"))
	}))
	defer ts.Close()

	req := testRequest(ts.URL)
	req.MIME = "image/png"
	var buf bytes.Buffer
	if _, err := Fetch(context.Background(), ts.Client(), "", req, &buf); err != nil {
		t.Fatal(err)
	}
}

func TestFetchJSONError(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"error":{"code":400,"message":"Invalid bbox"}}`))
	}))
	defer ts.Close()

	var buf bytes.Buffer
	_, err := Fetch(context.Background(), ts.Client(), "", testRequest(ts.URL), &buf)
	if err == nil || !strings.Contains(err.Error(), "Invalid bbox") {
		t.Fatalf("error %v, want the service error detail", err)
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
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("OK"))
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

func TestFetchNoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "nope", http.StatusForbidden)
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
