// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/karamble/satfetch"
	"github.com/karamble/satfetch/internal/cog"
	"github.com/karamble/satfetch/internal/geo"
	"github.com/karamble/satfetch/internal/httpapi"
	"github.com/karamble/satfetch/internal/tiffw"
)

const (
	testLat  = 50.2649
	testLon  = 19.0238
	testEPSG = 32633
)

type fakeCatalog struct {
	scenes []satfetch.Scene
	err    error
}

func (f *fakeCatalog) Search(context.Context, satfetch.Query) ([]satfetch.Scene, error) {
	return f.scenes, f.err
}

func (f *fakeCatalog) Get(_ context.Context, id string) (satfetch.Scene, error) {
	if f.err != nil {
		return satfetch.Scene{}, f.err
	}
	for _, s := range f.scenes {
		if s.ID == id {
			return s, nil
		}
	}
	return satfetch.Scene{}, satfetch.ErrNoScene
}

// fixtureScene builds a synthetic 256x256 scene centered on the test point:
// a true-color gradient plus constant red=1000 / nir=3000 bands (NDVI 0.5),
// served over an httptest file server with Range support.
func fixtureScene(t *testing.T) satfetch.Scene {
	t.Helper()
	e, n, err := geo.LatLonToUTM(testEPSG, testLat, testLon)
	if err != nil {
		t.Fatal(err)
	}
	g := tiffw.Geo{
		ScaleX: 10, ScaleY: 10,
		OriginX: e - 1280, OriginY: n + 1280,
		KeyDir: []uint16{1, 1, 0, 1, 3072, 0, 1, testEPSG},
		Ascii:  "WGS 84 / UTM zone 33N|WGS 84|",
		NoData: "0",
	}
	const dim = 256
	rgb := make([]uint8, dim*dim*3)
	for y := 0; y < dim; y++ {
		for x := 0; x < dim; x++ {
			rgb[(y*dim+x)*3+0] = uint8(x)
			rgb[(y*dim+x)*3+1] = uint8(y)
			rgb[(y*dim+x)*3+2] = 128
		}
	}
	red := make([]uint16, dim*dim)
	nir := make([]uint16, dim*dim)
	for i := range red {
		red[i] = 1000
		nir[i] = 3000
	}
	files := map[string][]byte{}
	var buf bytes.Buffer
	if err := tiffw.WriteCOG(&buf, tiffw.COGSpec{
		Width: dim, Height: dim, TileSize: 64, SPP: 3, Bits: 8,
		Levels: []int{1, 2}, Geo: g, Pix8: rgb,
	}); err != nil {
		t.Fatal(err)
	}
	files["/visual.tif"] = append([]byte(nil), buf.Bytes()...)
	for name, band := range map[string][]uint16{"/red.tif": red, "/nir.tif": nir} {
		buf.Reset()
		if err := tiffw.WriteCOG(&buf, tiffw.COGSpec{
			Width: dim, Height: dim, TileSize: 64, SPP: 1, Bits: 16,
			Levels: []int{1, 2}, Geo: g, Pix16: band,
		}); err != nil {
			t.Fatal(err)
		}
		files[name] = append([]byte(nil), buf.Bytes()...)
	}
	assets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "", time.Now(), bytes.NewReader(data))
	}))
	t.Cleanup(assets.Close)
	return satfetch.Scene{
		ID:         "S2TEST",
		Datetime:   time.Date(2026, 7, 2, 9, 57, 2, 0, time.UTC),
		CloudCover: 12.5,
		EPSG:       testEPSG,
		Assets: map[string]string{
			"visual":    assets.URL + "/visual.tif",
			"red":       assets.URL + "/red.tif",
			"nir":       assets.URL + "/nir.tif",
			"thumbnail": assets.URL + "/thumb.jpg",
		},
	}
}

func newAPI(t *testing.T, cat satfetch.Catalog, wmsSources ...satfetch.WMSSource) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := satfetch.New(satfetch.Options{
		Catalog:       cat,
		CacheDir:      t.TempDir(),
		Logger:        log,
		WMSSources:    wmsSources,
		TileSources:   []satfetch.TileSource{},
		ArcGISSources: []satfetch.ArcGISSource{},
		STACSources:   []satfetch.STACSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	ts := httptest.NewServer(httpapi.New(svc, log))
	t.Cleanup(ts.Close)
	return ts
}

// fixtureWMS serves a fixed JPEG payload for any GetMap request.
func fixtureWMS(t *testing.T, payload []byte) satfetch.WMSSource {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(payload)
	}))
	t.Cleanup(ts.Close)
	return satfetch.WMSSource{Name: "test", BaseURL: ts.URL, Layers: "L", GSD: 0.25, Attribution: "test data"}
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func productURL(base, endpoint, extra string) string {
	return fmt.Sprintf("%s/%s?lat=%f&lon=%f&size_km=1%s", base, endpoint, testLat, testLon, extra)
}

func TestImagePNGAndCache(t *testing.T) {
	ts := newAPI(t, &fakeCatalog{scenes: []satfetch.Scene{fixtureScene(t)}})

	resp, body := get(t, productURL(ts.URL, "image", ""))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("content type %s", ct)
	}
	if resp.Header.Get("X-Scene-ID") != "S2TEST" {
		t.Errorf("scene header %q", resp.Header.Get("X-Scene-ID"))
	}
	if resp.Header.Get("X-Cache") != "MISS" {
		t.Errorf("first call X-Cache %q", resp.Header.Get("X-Cache"))
	}
	if resp.Header.Get("X-Scene-Cloud-Cover") != "12.5" {
		t.Errorf("cloud header %q", resp.Header.Get("X-Scene-Cloud-Cover"))
	}
	img, kind, err := image.Decode(bytes.NewReader(body))
	if err != nil || kind != "png" {
		t.Fatalf("decode: %v (%s)", err, kind)
	}
	b := img.Bounds()
	if b.Dx() < 95 || b.Dx() > 108 || b.Dy() < 95 || b.Dy() > 108 {
		t.Errorf("dimensions %dx%d, want ~100x100", b.Dx(), b.Dy())
	}
	// The gradient makes pixels vary; a fully black image would mean a
	// misplaced window.
	c := color.NRGBAModel.Convert(img.At(b.Dx()/2, b.Dy()/2)).(color.NRGBA)
	if c.B != 128 {
		t.Errorf("blue channel %d, want 128", c.B)
	}

	resp, _ = get(t, productURL(ts.URL, "image", ""))
	if resp.Header.Get("X-Cache") != "HIT" {
		t.Errorf("second call X-Cache %q", resp.Header.Get("X-Cache"))
	}
}

func TestImageJPEGMaxPx(t *testing.T) {
	ts := newAPI(t, &fakeCatalog{scenes: []satfetch.Scene{fixtureScene(t)}})
	resp, body := get(t, productURL(ts.URL, "image", "&format=jpeg&max_px=60"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content type %s", ct)
	}
	img, kind, err := image.Decode(bytes.NewReader(body))
	if err != nil || kind != "jpeg" {
		t.Fatalf("decode: %v (%s)", err, kind)
	}
	b := img.Bounds()
	if b.Dx() < 45 || b.Dx() > 56 || b.Dy() < 45 || b.Dy() > 56 {
		t.Errorf("dimensions %dx%d, want ~50x50 via the /2 overview", b.Dx(), b.Dy())
	}
}

func TestImageGTiff(t *testing.T) {
	ts := newAPI(t, &fakeCatalog{scenes: []satfetch.Scene{fixtureScene(t)}})
	resp, body := get(t, productURL(ts.URL, "image", "&format=gtiff"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/tiff" {
		t.Errorf("content type %s", ct)
	}
	f, err := cog.Open(context.Background(), &cog.BytesSource{Data: body})
	if err != nil {
		t.Fatal(err)
	}
	if f.Grid.EPSG != testEPSG {
		t.Errorf("epsg %d", f.Grid.EPSG)
	}
	e, _, _ := geo.LatLonToUTM(testEPSG, testLat, testLon)
	if f.Grid.OriginX < e-1280 || f.Grid.OriginX > e+1280 {
		t.Errorf("origin %f outside source extent around %f", f.Grid.OriginX, e)
	}
	if f.IFDs[0].Width < 95 || f.IFDs[0].Width > 108 {
		t.Errorf("width %d", f.IFDs[0].Width)
	}
}

func TestNDVIPNG(t *testing.T) {
	ts := newAPI(t, &fakeCatalog{scenes: []satfetch.Scene{fixtureScene(t)}})
	resp, body := get(t, productURL(ts.URL, "ndvi", ""))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	// Constant bands make NDVI 0.5 everywhere: ramp midpoint of the
	// light-to-dark green segment.
	c := color.NRGBAModel.Convert(img.At(b.Dx()/2, b.Dy()/2)).(color.NRGBA)
	want := color.NRGBA{R: 95, G: 156, B: 84, A: 255}
	if c != want {
		t.Errorf("center pixel %+v, want %+v", c, want)
	}
}

func TestNDVIGTiff(t *testing.T) {
	ts := newAPI(t, &fakeCatalog{scenes: []satfetch.Scene{fixtureScene(t)}})
	resp, body := get(t, productURL(ts.URL, "ndvi", "&format=gtiff"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	f, err := cog.Open(context.Background(), &cog.BytesSource{Data: body})
	if err != nil {
		t.Fatal(err)
	}
	if f.NoData != "nan" {
		t.Errorf("nodata %q, want nan", f.NoData)
	}
	ras, err := f.ReadWindow(context.Background(), 0, 0, 0, f.IFDs[0].Width, f.IFDs[0].Height, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, idx := range []int{0, len(ras.F32) / 2, len(ras.F32) - 1} {
		if v := ras.F32[idx]; v != 0.5 {
			t.Errorf("ndvi[%d] = %f, want 0.5", idx, v)
		}
	}
}

func TestOrtho(t *testing.T) {
	payload := []byte("FAKEJPEGBYTES")
	ts := newAPI(t, &fakeCatalog{}, fixtureWMS(t, payload))

	url := fmt.Sprintf("%s/ortho?lat=%f&lon=%f&source=test&size_km=0.5&px=256", ts.URL, testLat, testLon)
	resp, body := get(t, url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content type %s", ct)
	}
	if resp.Header.Get("X-Source") != "test" {
		t.Errorf("X-Source %q", resp.Header.Get("X-Source"))
	}
	if resp.Header.Get("X-Source-GSD") != "0.25" {
		t.Errorf("X-Source-GSD %q", resp.Header.Get("X-Source-GSD"))
	}
	if resp.Header.Get("X-Scene-ID") != "" {
		t.Errorf("unexpected scene header %q", resp.Header.Get("X-Scene-ID"))
	}
	if resp.Header.Get("X-Cache") != "MISS" {
		t.Errorf("X-Cache %q", resp.Header.Get("X-Cache"))
	}
	if !bytes.Equal(body, payload) {
		t.Error("body differs from the WMS payload")
	}

	resp, _ = get(t, url)
	if resp.Header.Get("X-Cache") != "HIT" {
		t.Errorf("second call X-Cache %q", resp.Header.Get("X-Cache"))
	}

	_, body = get(t, ts.URL+"/metrics")
	if !strings.Contains(string(body), "satfetch_upstream_bytes_total 13") {
		t.Errorf("metrics missing upstream bytes:\n%s", body)
	}
}

func TestOrthoValidation(t *testing.T) {
	ts := newAPI(t, &fakeCatalog{}, fixtureWMS(t, []byte("X")))
	tests := []struct {
		name string
		url  string
	}{
		{"missing source", fmt.Sprintf("%s/ortho?lat=%f&lon=%f", ts.URL, testLat, testLon)},
		{"unknown source", fmt.Sprintf("%s/ortho?lat=%f&lon=%f&source=xx", ts.URL, testLat, testLon)},
		{"bad px", fmt.Sprintf("%s/ortho?lat=%f&lon=%f&source=test&px=9999", ts.URL, testLat, testLon)},
		{"bad size", fmt.Sprintf("%s/ortho?lat=%f&lon=%f&source=test&size_km=50", ts.URL, testLat, testLon)},
		{"gtiff format", fmt.Sprintf("%s/ortho?lat=%f&lon=%f&source=test&format=gtiff", ts.URL, testLat, testLon)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := get(t, tc.url)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status %d, want 400 (%s)", resp.StatusCode, body)
			}
		})
	}
	resp, body := get(t, fmt.Sprintf("%s/ortho?lat=%f&lon=%f&source=xx", ts.URL, testLat, testLon))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "available: test") {
		t.Errorf("unknown source response %d %s", resp.StatusCode, body)
	}
}

func TestSources(t *testing.T) {
	ts := newAPI(t, &fakeCatalog{}, fixtureWMS(t, []byte("X")))
	resp, body := get(t, ts.URL+"/sources")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out struct {
		Sources []struct {
			Name        string  `json:"name"`
			Type        string  `json:"type"`
			GSD         float64 `json:"gsd"`
			Attribution string  `json:"attribution"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sources) != 1 || out.Sources[0].Name != "test" || out.Sources[0].Type != "wms" ||
		out.Sources[0].GSD != 0.25 || out.Sources[0].Attribution != "test data" {
		t.Errorf("sources %+v", out.Sources)
	}
}

// newAPIWithTiles serves the API over a tile-source-only registry.
func newAPIWithTiles(t *testing.T, cat satfetch.Catalog, tileSources ...satfetch.TileSource) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := satfetch.New(satfetch.Options{
		Catalog:       cat,
		CacheDir:      t.TempDir(),
		Logger:        log,
		WMSSources:    []satfetch.WMSSource{},
		TileSources:   tileSources,
		ArcGISSources: []satfetch.ArcGISSource{},
		STACSources:   []satfetch.STACSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	ts := httptest.NewServer(httpapi.New(svc, log))
	t.Cleanup(ts.Close)
	return ts
}

func newAPIWithSTAC(t *testing.T, stacSources ...satfetch.STACSource) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := satfetch.New(satfetch.Options{
		Catalog:       &fakeCatalog{},
		CacheDir:      t.TempDir(),
		Logger:        log,
		WMSSources:    []satfetch.WMSSource{},
		TileSources:   []satfetch.TileSource{},
		ArcGISSources: []satfetch.ArcGISSource{},
		STACSources:   stacSources,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	ts := httptest.NewServer(httpapi.New(svc, log))
	t.Cleanup(ts.Close)
	return ts
}

// NAIP-shaped fixture: NAD83 UTM zone 19, 0.3 m pixels, four uint8 bands
// where the fourth is near-infrared and must not reach the output.
const (
	naipLat  = 42.3601
	naipLon  = -71.0589
	naipEPSG = 26919
)

// fixtureSTACSource stands up a STAC endpoint and a COG host, and returns the
// source pointing at them.
func fixtureSTACSource(t *testing.T) satfetch.STACSource {
	t.Helper()
	e, n, err := geo.LatLonToUTM(naipEPSG, naipLat, naipLon)
	if err != nil {
		t.Fatal(err)
	}
	const dim = 512
	// Distinct constant bands: NIR is brightest, so a band-ordering slip
	// shows up as the wrong channel rather than a plausible image.
	pix := make([]uint8, dim*dim*4)
	for i := 0; i < dim*dim; i++ {
		pix[i*4+0] = 10
		pix[i*4+1] = 20
		pix[i*4+2] = 30
		pix[i*4+3] = 240
	}
	var buf bytes.Buffer
	if err := tiffw.WriteCOG(&buf, tiffw.COGSpec{
		Width: dim, Height: dim, TileSize: 128, SPP: 4, Bits: 8,
		Levels: []int{1, 2}, Pix8: pix,
		Geo: tiffw.Geo{
			ScaleX: 0.3, ScaleY: 0.3,
			OriginX: e - dim/2*0.3, OriginY: n + dim/2*0.3,
			KeyDir: []uint16{1, 1, 0, 1, 3072, 0, 1, naipEPSG},
			Ascii:  "NAD83 / UTM zone 19N|NAD83|",
		},
	}); err != nil {
		t.Fatal(err)
	}
	cog := buf.Bytes()

	assets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "", time.Now(), bytes.NewReader(cog))
	}))
	t.Cleanup(assets.Close)

	stac := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if q, ok := body["query"]; ok && q != nil {
			t.Errorf("cloud filter sent to a collection without cloud metadata: %v", q)
		}
		fmt.Fprintf(w, `{"features":[{"id":"ma_test_2023","properties":{`+
			`"datetime":"2023-07-07T16:00:00Z","gsd":0.3,"proj:epsg":%d},`+
			`"assets":{"image":{"href":%q}}}]}`, naipEPSG, assets.URL+"/a.tif")
	}))
	t.Cleanup(stac.Close)

	return satfetch.STACSource{
		Name: "nt", STACURL: stac.URL, Collection: "naip", Asset: "image",
		GSD: 0.6, Attribution: "stac test",
	}
}

func TestOrthoSTACEndpoint(t *testing.T) {
	ts := newAPIWithSTAC(t, fixtureSTACSource(t))

	url := fmt.Sprintf("%s/ortho?lat=%f&lon=%f&source=nt&size_km=0.1&px=512", ts.URL, naipLat, naipLon)
	resp, body := get(t, url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content type %s", ct)
	}
	if got := resp.Header.Get("X-Source"); got != "nt" {
		t.Errorf("X-Source %q", got)
	}
	// The item reports 0.3, which must win over the source's nominal 0.6.
	if got := resp.Header.Get("X-Source-GSD"); got != "0.3" {
		t.Errorf("X-Source-GSD %q, want 0.3", got)
	}
	if got := resp.Header.Get("X-Scene-ID"); got != "ma_test_2023" {
		t.Errorf("X-Scene-ID %q", got)
	}
	if got := resp.Header.Get("X-Cache"); got != "MISS" {
		t.Errorf("X-Cache %q", got)
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() < 300 || b.Dy() < 300 {
		t.Errorf("image %dx%d, want roughly 100m at 0.3m", b.Dx(), b.Dy())
	}
	// RGB survives, NIR is dropped: a raster read as RGB+A or shifted by a
	// band would land far from these values.
	r, g, b, _ := img.At(img.Bounds().Dx()/2, img.Bounds().Dy()/2).RGBA()
	for i, got := range []uint32{r >> 8, g >> 8, b >> 8} {
		if want := uint32([]int{10, 20, 30}[i]); got < want-4 || got > want+4 {
			t.Errorf("channel %d = %d, want about %d (NIR leaked?)", i, got, want)
		}
	}

	resp, _ = get(t, url)
	if got := resp.Header.Get("X-Cache"); got != "HIT" {
		t.Errorf("second request X-Cache %q, want HIT", got)
	}
}

func TestOrthoSTACNoCoverage(t *testing.T) {
	stac := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"features":[]}`))
	}))
	t.Cleanup(stac.Close)
	ts := newAPIWithSTAC(t, satfetch.STACSource{
		Name: "nt", STACURL: stac.URL, Collection: "naip", Asset: "image",
		GSD: 0.6, Attribution: "stac test",
	})

	url := fmt.Sprintf("%s/ortho?lat=%f&lon=%f&source=nt", ts.URL, naipLat, naipLon)
	resp, body := get(t, url)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
}

func TestOrthoTilesEndpoint(t *testing.T) {
	tileHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		img := image.NewNRGBA(image.Rect(0, 0, 256, 256))
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i-1] = 0x80
			img.Pix[i] = 0xff
		}
		w.Header().Set("Content-Type", "image/png")
		png.Encode(w, img)
	}))
	t.Cleanup(tileHost.Close)
	src := satfetch.TileSource{
		Name: "tt", URLTemplate: tileHost.URL + "/{z}/{x}/{y}",
		GSD: 0.3, MaxZoom: 19, Attribution: "tile test",
	}
	ts := newAPIWithTiles(t, &fakeCatalog{}, src)

	url := fmt.Sprintf("%s/ortho?lat=%f&lon=%f&source=tt&size_km=0.5&px=512&format=png", ts.URL, testLat, testLon)
	resp, body := get(t, url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("content type %s", ct)
	}
	if resp.Header.Get("X-Source") != "tt" {
		t.Errorf("X-Source %q", resp.Header.Get("X-Source"))
	}
	if resp.Header.Get("X-Cache") != "MISS" {
		t.Errorf("X-Cache %q", resp.Header.Get("X-Cache"))
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() <= 256 || b.Dx() > 512 {
		t.Errorf("width %d, want within (256,512]", b.Dx())
	}

	resp, _ = get(t, url)
	if resp.Header.Get("X-Cache") != "HIT" {
		t.Errorf("second call X-Cache %q", resp.Header.Get("X-Cache"))
	}

	_, body = get(t, ts.URL+"/sources")
	if !strings.Contains(string(body), `"type":"tiles"`) {
		t.Errorf("sources missing tiles type: %s", body)
	}
}

func TestScenes(t *testing.T) {
	ts := newAPI(t, &fakeCatalog{scenes: []satfetch.Scene{fixtureScene(t)}})
	resp, body := get(t, fmt.Sprintf("%s/scenes?lat=%f&lon=%f", ts.URL, testLat, testLon))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Scenes []struct {
			ID         string   `json:"id"`
			Datetime   string   `json:"datetime"`
			CloudCover float64  `json:"cloud_cover"`
			Thumbnail  string   `json:"thumbnail"`
			Assets     []string `json:"assets"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Scenes) != 1 || out.Scenes[0].ID != "S2TEST" {
		t.Fatalf("scenes %+v", out.Scenes)
	}
	sc := out.Scenes[0]
	if sc.CloudCover != 12.5 || sc.Thumbnail == "" || sc.Datetime == "" {
		t.Errorf("scene fields %+v", sc)
	}
	if strings.Join(sc.Assets, ",") != "visual,red,nir" {
		t.Errorf("assets %v", sc.Assets)
	}
}

func TestValidation(t *testing.T) {
	ts := newAPI(t, &fakeCatalog{scenes: []satfetch.Scene{fixtureScene(t)}})
	tests := []struct {
		name string
		url  string
	}{
		{"missing lat", ts.URL + "/image?lon=19"},
		{"size out of range", fmt.Sprintf("%s/image?lat=%f&lon=%f&size_km=100", ts.URL, testLat, testLon)},
		{"bad format", productURL(ts.URL, "image", "&format=bmp")},
		{"ndvi jpeg", productURL(ts.URL, "ndvi", "&format=jpeg")},
		{"bad days", productURL(ts.URL, "image", "&days=9999")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := get(t, tc.url)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status %d, want 400", resp.StatusCode)
			}
			var e map[string]string
			if err := json.Unmarshal(body, &e); err != nil || e["error"] == "" {
				t.Errorf("error body %q", body)
			}
		})
	}
}

func TestNoScene(t *testing.T) {
	ts := newAPI(t, &fakeCatalog{})
	resp, _ := get(t, productURL(ts.URL, "image", ""))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}

func TestUpstreamFailure(t *testing.T) {
	cat := &fakeCatalog{err: fmt.Errorf("%w: catalog down", satfetch.ErrUpstream)}
	ts := newAPI(t, cat)
	resp, _ := get(t, productURL(ts.URL, "image", ""))
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %d, want 502", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") != "60" {
		t.Errorf("Retry-After %q", resp.Header.Get("Retry-After"))
	}
	_, body := get(t, ts.URL+"/metrics")
	if !strings.Contains(string(body), "satfetch_stac_errors_total 1") {
		t.Errorf("metrics missing stac error count:\n%s", body)
	}
	if !strings.Contains(string(body), `satfetch_requests_total{endpoint="image",status="502"} 1`) {
		t.Errorf("metrics missing request count:\n%s", body)
	}
}

func TestHealthz(t *testing.T) {
	ts := newAPI(t, &fakeCatalog{})
	resp, body := get(t, ts.URL+"/healthz")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "ok") {
		t.Errorf("healthz %d %s", resp.StatusCode, body)
	}
}
