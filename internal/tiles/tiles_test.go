// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package tiles

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// Tile indices verified against live basemap.at and maaamet.ee fetches.
func TestTileIndices(t *testing.T) {
	tests := []struct {
		name     string
		lat, lon float64
		zoom     int
		tx, ty   int
	}{
		{"Vienna", 48.2082, 16.3738, 16, 35748, 22724},
		{"Tallinn", 59.4370, 24.7536, 16, 37274, 19234},
		{"Prague", 50.0865, 14.4114, 16, 35391, 22201},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x, y := globalPx(tc.lat, tc.lon, tc.zoom, 256)
			if int(x)/256 != tc.tx || int(y)/256 != tc.ty {
				t.Errorf("tile %d,%d, want %d,%d", int(x)/256, int(y)/256, tc.tx, tc.ty)
			}
		})
	}
}

func TestTileURL(t *testing.T) {
	got := tileURL("https://ex/{z}/{y}/{x}.jpeg", 16, 35748, 22724)
	if got != "https://ex/16/22724/35748.jpeg" {
		t.Errorf("url %s", got)
	}
	got = tileURL("https://ex/{z}/{x}/{-y}.png", 16, 37274, 19234)
	if got != "https://ex/16/37274/46301.png" {
		t.Errorf("tms url %s", got)
	}
}

func TestValidate(t *testing.T) {
	base := Request{
		URLTemplate: "https://ex/{z}/{x}/{y}",
		MinLat:      50, MinLon: 19, MaxLat: 50.01, MaxLon: 19.01,
		Zoom: 16,
	}
	if err := validate(base); err != nil {
		t.Errorf("valid request rejected: %v", err)
	}
	bad := base
	bad.URLTemplate = "https://ex/{z}/{x}"
	if err := validate(bad); err == nil {
		t.Error("template without a row placeholder accepted")
	}
	bad = base
	bad.MaxLat = 86
	if err := validate(bad); err == nil {
		t.Error("latitude beyond WebMercator accepted")
	}
	bad = base
	bad.MinLat, bad.MaxLat = 50.01, 50
	if err := validate(bad); err == nil {
		t.Error("empty box accepted")
	}
}

// inverse of globalPx for building exact test boxes.
func lonOf(px float64, zoom, ts int) float64 {
	n := float64(int64(1)<<zoom) * float64(ts)
	return px/n*360 - 180
}

func latOf(py float64, zoom, ts int) float64 {
	n := float64(int64(1)<<zoom) * float64(ts)
	return math.Atan(math.Sinh(math.Pi*(1-2*py/n))) * 180 / math.Pi
}

// tileServer answers solid-color tiles: red = column, green = row.
func tileServer(t *testing.T, missing map[[2]int]bool, fail *atomic.Int32) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail != nil && fail.Add(-1) >= 0 {
			http.Error(w, "busy", http.StatusInternalServerError)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 3 {
			http.NotFound(w, r)
			return
		}
		x, _ := strconv.Atoi(parts[1])
		y, _ := strconv.Atoi(parts[2])
		if missing[[2]int{x, y}] {
			http.NotFound(w, r)
			return
		}
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

func fetchBox(t *testing.T, ts *httptest.Server, zoom int, px0, py0, px1, py1 float64) *image.NRGBA {
	t.Helper()
	img, _, err := Fetch(context.Background(), ts.Client(), "test", Request{
		URLTemplate: ts.URL + "/{z}/{x}/{y}",
		MinLat:      latOf(py1, zoom, 256),
		MaxLat:      latOf(py0, zoom, 256),
		MinLon:      lonOf(px0, zoom, 256),
		MaxLon:      lonOf(px1, zoom, 256),
		Zoom:        zoom,
		Concurrency: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func pixAt(img *image.NRGBA, x, y int) color.NRGBA {
	return img.NRGBAAt(img.Bounds().Min.X+x, img.Bounds().Min.Y+y)
}

func TestFetchMosaic(t *testing.T) {
	ts := tileServer(t, nil, nil)
	const zoom = 10
	// A box spanning parts of tiles (100,200), (101,200), (100,201),
	// (101,201): from half of tile 100/200 to a quarter into 101/201.
	img := fetchBox(t, ts, zoom, 100*256+128, 200*256+128, 101*256+64, 201*256+64)
	b := img.Bounds()
	if b.Dx() < 190 || b.Dx() > 194 || b.Dy() < 190 || b.Dy() > 194 {
		t.Fatalf("mosaic %dx%d, want ~192x192", b.Dx(), b.Dy())
	}
	if c := pixAt(img, 0, 0); c.R != 100 || c.G != 200 {
		t.Errorf("top-left from tile %d,%d", c.R, c.G)
	}
	if c := pixAt(img, b.Dx()-1, 0); c.R != 101 || c.G != 200 {
		t.Errorf("top-right from tile %d,%d", c.R, c.G)
	}
	if c := pixAt(img, 0, b.Dy()-1); c.R != 100 || c.G != 201 {
		t.Errorf("bottom-left from tile %d,%d", c.R, c.G)
	}
	if c := pixAt(img, b.Dx()-1, b.Dy()-1); c.R != 101 || c.G != 201 {
		t.Errorf("bottom-right from tile %d,%d", c.R, c.G)
	}
}

func TestFetchMissingTileStaysBlack(t *testing.T) {
	ts := tileServer(t, map[[2]int]bool{{101, 200}: true}, nil)
	const zoom = 10
	img := fetchBox(t, ts, zoom, 100*256+128, 200*256+64, 101*256+128, 200*256+192)
	b := img.Bounds()
	if c := pixAt(img, 0, 0); c.R != 100 || c.G != 200 {
		t.Errorf("left half from tile %d,%d", c.R, c.G)
	}
	if c := pixAt(img, b.Dx()-1, 0); c.R != 0 || c.G != 0 || c.B != 0 || c.A != 0xff {
		t.Errorf("missing tile area = %+v, want opaque black", c)
	}
}

func TestFetchRetries(t *testing.T) {
	var fail atomic.Int32
	fail.Store(2)
	ts := tileServer(t, nil, &fail)
	img := fetchBox(t, ts, 10, 100*256, 200*256, 100*256+64, 200*256+64)
	if img.Bounds().Dx() != 64 {
		t.Errorf("width %d", img.Bounds().Dx())
	}
}

func TestFetchDecodeError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>not a tile</html>")
	}))
	defer ts.Close()
	_, _, err := Fetch(context.Background(), ts.Client(), "", Request{
		URLTemplate: ts.URL + "/{z}/{x}/{y}",
		MinLat:      50, MinLon: 19, MaxLat: 50.001, MaxLon: 19.001,
		Zoom: 16,
	})
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("error %v, want decode failure", err)
	}
}

func TestMetersPerPixel(t *testing.T) {
	// At the equator, zoom 0 is one 256px tile for the whole world.
	if mpp := MetersPerPixel(0, 0); math.Abs(mpp-156543.03392) > 0.01 {
		t.Errorf("mpp %f", mpp)
	}
	if mpp := MetersPerPixel(60, 1); math.Abs(mpp-156543.03392*0.5/2) > 0.01 {
		t.Errorf("mpp at 60N z1 %f", mpp)
	}
}
