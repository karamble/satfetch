// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package satfetch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func fixtureJSON(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEarthSearchSearch(t *testing.T) {
	fixture := fixtureJSON(t)
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Error(err)
		}
		w.Write(fixture)
	}))
	defer ts.Close()

	es := NewEarthSearch(EarthSearchOptions{BaseURL: ts.URL})
	scenes, err := es.Search(context.Background(), Query{
		Lon: 19.0238, Lat: 50.2649, MaxCloud: 20, Days: 30, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}

	if c := gotBody["collections"].([]any); c[0] != "sentinel-2-l2a" {
		t.Errorf("collections %v", c)
	}
	coords := gotBody["intersects"].(map[string]any)["coordinates"].([]any)
	if coords[0].(float64) != 19.0238 || coords[1].(float64) != 50.2649 {
		t.Errorf("coordinates %v", coords)
	}
	lt := gotBody["query"].(map[string]any)["eo:cloud_cover"].(map[string]any)["lt"].(float64)
	if lt != 20 {
		t.Errorf("cloud filter %v", lt)
	}
	if gotBody["limit"].(float64) != 50 {
		t.Errorf("limit %v", gotBody["limit"])
	}
	if gotBody["datetime"] == nil {
		t.Error("datetime range missing")
	}

	if len(scenes) != 2 {
		t.Fatalf("scene count %d", len(scenes))
	}
	first := scenes[0]
	if first.ID != "S2A_33UYR_20260702_1_L2A" {
		t.Errorf("not sorted newest first: %s", first.ID)
	}
	if first.Datetime.Year() != 2026 || first.Datetime.Month() != 7 {
		t.Errorf("datetime %v", first.Datetime)
	}
	if first.CloudCover != 15.443732 {
		t.Errorf("cloud cover %f", first.CloudCover)
	}
	if first.EPSG != 32633 {
		t.Errorf("epsg %d", first.EPSG)
	}
	if first.Assets["visual"] == "" || first.Assets["red"] == "" || first.Assets["nir"] == "" {
		t.Errorf("assets %v", first.Assets)
	}
}

func TestEarthSearchHrefHook(t *testing.T) {
	fixture := fixtureJSON(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(fixture)
	}))
	defer ts.Close()

	es := NewEarthSearch(EarthSearchOptions{
		BaseURL:  ts.URL,
		HrefHook: func(h string) (string, error) { return h + "?signed", nil },
	})
	scenes, err := es.Search(context.Background(), Query{Lon: 19, Lat: 50, MaxCloud: 100, Days: 30})
	if err != nil {
		t.Fatal(err)
	}
	for _, href := range scenes[0].Assets {
		if href[len(href)-7:] != "?signed" {
			t.Fatalf("href not signed: %s", href)
		}
	}
}

func TestEarthSearchGet(t *testing.T) {
	fixture := fixtureJSON(t)
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		if ids, ok := gotBody["ids"].([]any); ok && len(ids) == 1 {
			w.Write(fixture)
			return
		}
		w.Write([]byte(`{"features":[]}`))
	}))
	defer ts.Close()

	es := NewEarthSearch(EarthSearchOptions{BaseURL: ts.URL})
	sc, err := es.Get(context.Background(), "S2A_33UYR_20260625_0_L2A")
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["ids"].([]any)[0] != "S2A_33UYR_20260625_0_L2A" {
		t.Errorf("ids body %v", gotBody["ids"])
	}
	if gotBody["intersects"] != nil {
		t.Error("id lookup must not send intersects")
	}
	if sc.ID == "" {
		t.Error("empty scene")
	}
}

func TestEarthSearchGetMissing(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"features":[]}`))
	}))
	defer ts.Close()

	es := NewEarthSearch(EarthSearchOptions{BaseURL: ts.URL})
	if _, err := es.Get(context.Background(), "nope"); !errors.Is(err, ErrNoScene) {
		t.Errorf("error %v, want ErrNoScene", err)
	}
}

func TestEarthSearchRetries(t *testing.T) {
	fixture := fixtureJSON(t)
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			http.Error(w, "flaky", http.StatusInternalServerError)
			return
		}
		w.Write(fixture)
	}))
	defer ts.Close()

	es := NewEarthSearch(EarthSearchOptions{BaseURL: ts.URL})
	if _, err := es.Search(context.Background(), Query{Lon: 19, Lat: 50, MaxCloud: 20, Days: 30}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("attempts %d, want 3", calls.Load())
	}
}

func TestEarthSearchRetriesExhausted(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer ts.Close()

	es := NewEarthSearch(EarthSearchOptions{BaseURL: ts.URL})
	_, err := es.Search(context.Background(), Query{Lon: 19, Lat: 50, MaxCloud: 20, Days: 30})
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("error %v, want ErrUpstream", err)
	}
	if calls.Load() != 4 {
		t.Errorf("attempts %d, want 4", calls.Load())
	}
}

func TestEarthSearchNoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer ts.Close()

	es := NewEarthSearch(EarthSearchOptions{BaseURL: ts.URL})
	if _, err := es.Search(context.Background(), Query{Lon: 19, Lat: 50, MaxCloud: 20, Days: 30}); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Errorf("attempts %d, want 1", calls.Load())
	}
}
