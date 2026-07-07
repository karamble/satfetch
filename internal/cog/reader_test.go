// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package cog_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/karamble/satfetch/internal/cog"
	"github.com/karamble/satfetch/internal/tiffw"
)

var testGeo = tiffw.Geo{
	ScaleX:  10,
	ScaleY:  10,
	OriginX: 699960,
	OriginY: 5600040,
	KeyDir:  []uint16{1, 1, 0, 1, 3072, 0, 1, 32633},
	Ascii:   "WGS 84 / UTM zone 33N|WGS 84|",
	NoData:  "0",
}

func rgbAt(x, y, c int) uint8 {
	return uint8((x*7 + y*3 + c*11) % 251)
}

func buildRGBCOG(t *testing.T, w, h, tile int, levels []int, sparse map[[2]int]bool) []byte {
	t.Helper()
	pix := make([]uint8, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				pix[(y*w+x)*3+c] = rgbAt(x, y, c)
			}
		}
	}
	var buf bytes.Buffer
	err := tiffw.WriteCOG(&buf, tiffw.COGSpec{
		Width: w, Height: h, TileSize: tile, SPP: 3, Bits: 8,
		Levels: levels, Geo: testGeo, Pix8: pix, SparseTiles: sparse,
	})
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func openCOG(t *testing.T, data []byte) *cog.File {
	t.Helper()
	f, err := cog.Open(context.Background(), &cog.BytesSource{Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestOpenMeta(t *testing.T) {
	f := openCOG(t, buildRGBCOG(t, 48, 40, 16, []int{1, 2}, nil))
	if len(f.IFDs) != 2 {
		t.Fatalf("IFD count %d, want 2", len(f.IFDs))
	}
	full := f.IFDs[0]
	if full.Width != 48 || full.Height != 40 || full.SPP != 3 || full.Bits != 8 || !full.Tiled {
		t.Errorf("unexpected full IFD: %+v", full)
	}
	if f.Grid.EPSG != 32633 || f.Grid.OriginX != 699960 || f.Grid.OriginY != 5600040 ||
		f.Grid.ScaleX != 10 || f.Grid.ScaleY != 10 {
		t.Errorf("unexpected grid: %+v", f.Grid)
	}
	if f.NoData != "0" {
		t.Errorf("nodata %q, want 0", f.NoData)
	}
	if f.GeoAscii != testGeo.Ascii {
		t.Errorf("geo ascii %q", f.GeoAscii)
	}
	if len(f.KeyDir) != len(testGeo.KeyDir) {
		t.Errorf("keydir %v", f.KeyDir)
	}
	if d := f.Decimation(1); d != 2 {
		t.Errorf("decimation %d, want 2", d)
	}
	lg := f.LevelGrid(1)
	if lg.ScaleX != 20 || lg.OriginX != 699960 {
		t.Errorf("level grid: %+v", lg)
	}
}

func TestReadWindowFullRes(t *testing.T) {
	f := openCOG(t, buildRGBCOG(t, 48, 40, 16, []int{1, 2}, nil))
	ras, err := f.ReadWindow(context.Background(), 0, 5, 7, 20, 18, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ras.W != 20 || ras.H != 18 || ras.SPP != 3 || ras.Bits != 8 {
		t.Fatalf("raster %dx%d spp %d bits %d", ras.W, ras.H, ras.SPP, ras.Bits)
	}
	for y := 0; y < ras.H; y++ {
		for x := 0; x < ras.W; x++ {
			for c := 0; c < 3; c++ {
				want := rgbAt(x+5, y+7, c)
				got := ras.U8[(y*ras.W+x)*3+c]
				if got != want {
					t.Fatalf("pixel %d,%d,%d = %d, want %d", x, y, c, got, want)
				}
			}
		}
	}
}

func TestReadWindowSparseTile(t *testing.T) {
	// Tile (2,1) spans x 32..47, y 16..31 and is sparse: nodata zeros.
	f := openCOG(t, buildRGBCOG(t, 48, 40, 16, []int{1}, map[[2]int]bool{{2, 1}: true}))
	ras, err := f.ReadWindow(context.Background(), 0, 20, 10, 20, 15, 4)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < ras.H; y++ {
		for x := 0; x < ras.W; x++ {
			gx, gy := x+20, y+10
			for c := 0; c < 3; c++ {
				want := rgbAt(gx, gy, c)
				if gx >= 32 && gy >= 16 {
					want = 0
				}
				got := ras.U8[(y*ras.W+x)*3+c]
				if got != want {
					t.Fatalf("pixel %d,%d,%d = %d, want %d", gx, gy, c, got, want)
				}
			}
		}
	}
}

func TestReadWindowOverview(t *testing.T) {
	w, h := 48, 40
	f := openCOG(t, buildRGBCOG(t, w, h, 16, []int{1, 2}, nil))
	lw, lh := (w+1)/2, (h+1)/2
	ras, err := f.ReadWindow(context.Background(), 1, 0, 0, lw, lh, 1)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < lh; y++ {
		for x := 0; x < lw; x++ {
			sx, sy := min(x*2, w-1), min(y*2, h-1)
			for c := 0; c < 3; c++ {
				want := rgbAt(sx, sy, c)
				got := ras.U8[(y*ras.W+x)*3+c]
				if got != want {
					t.Fatalf("overview pixel %d,%d,%d = %d, want %d", x, y, c, got, want)
				}
			}
		}
	}
}

func TestReadWindowUint16(t *testing.T) {
	w, h := 40, 32
	pix := make([]uint16, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pix[y*w+x] = uint16(x*131 + y*7)
		}
	}
	var buf bytes.Buffer
	err := tiffw.WriteCOG(&buf, tiffw.COGSpec{
		Width: w, Height: h, TileSize: 16, SPP: 1, Bits: 16,
		Levels: []int{1}, Geo: testGeo, Pix16: pix,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := openCOG(t, buf.Bytes())
	ras, err := f.ReadWindow(context.Background(), 0, 3, 5, 30, 20, 3)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < ras.H; y++ {
		for x := 0; x < ras.W; x++ {
			want := uint16((x+3)*131 + (y+5)*7)
			if got := ras.U16[y*ras.W+x]; got != want {
				t.Fatalf("pixel %d,%d = %d, want %d", x, y, got, want)
			}
		}
	}
}

func TestPickLevel(t *testing.T) {
	f := openCOG(t, buildRGBCOG(t, 64, 64, 16, []int{1, 2, 4}, nil))
	tests := []struct {
		maxPx, winW, winH, want int
	}{
		{0, 64, 64, 0},
		{1000, 64, 64, 0},
		{32, 64, 64, 1},
		{16, 64, 64, 2},
		{1, 64, 64, 2}, // nothing fits: coarsest
		{16, 16, 16, 0},
	}
	for _, tc := range tests {
		if got := f.PickLevel(tc.maxPx, tc.winW, tc.winH); got != tc.want {
			t.Errorf("PickLevel(%d, %d, %d) = %d, want %d", tc.maxPx, tc.winW, tc.winH, got, tc.want)
		}
	}
}

func TestGridWindow(t *testing.T) {
	g := cog.Grid{EPSG: 32633, OriginX: 1000, OriginY: 2000, ScaleX: 10, ScaleY: 10}
	tests := []struct {
		name                   string
		minX, minY, maxX, maxY float64
		x0, y0, w, h           int
		ok                     bool
	}{
		{"interior", 1100, 1800, 1200, 1900, 10, 10, 10, 10, true},
		{"clamped", 900, 1950, 1050, 2100, 0, 0, 5, 5, true},
		{"outside", 3000, 3000, 3100, 3100, 0, 0, 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x0, y0, w, h, ok := g.Window(100, 100, tc.minX, tc.minY, tc.maxX, tc.maxY)
			if ok != tc.ok || x0 != tc.x0 || y0 != tc.y0 || w != tc.w || h != tc.h {
				t.Errorf("got %d,%d %dx%d %v; want %d,%d %dx%d %v",
					x0, y0, w, h, ok, tc.x0, tc.y0, tc.w, tc.h, tc.ok)
			}
		})
	}
}

// mkMinimalTIFF hand-crafts a tiny striped TIFF so structural rejects can be
// exercised without the writer (which never produces them).
func mkMinimalTIFF(compression uint16) []byte {
	type e struct {
		tag, typ uint16
		count    uint32
		val      uint32
	}
	entries := []e{
		{256, 3, 1, 4}, // width 4
		{257, 3, 1, 2}, // height 2
		{258, 3, 1, 8}, // 8 bits
		{259, 3, 1, uint32(compression)},
		{273, 4, 1, 0}, // strip offset, patched below
		{277, 3, 1, 1}, // 1 sample
		{278, 3, 1, 2}, // rows per strip
		{279, 4, 1, 8}, // strip byte count
	}
	ifdOff := 8
	dataOff := ifdOff + 2 + len(entries)*12 + 4
	entries[4].val = uint32(dataOff)
	buf := make([]byte, dataOff+8)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 42)
	binary.LittleEndian.PutUint32(buf[4:], uint32(ifdOff))
	binary.LittleEndian.PutUint16(buf[ifdOff:], uint16(len(entries)))
	for i, en := range entries {
		off := ifdOff + 2 + i*12
		binary.LittleEndian.PutUint16(buf[off:], en.tag)
		binary.LittleEndian.PutUint16(buf[off+2:], en.typ)
		binary.LittleEndian.PutUint32(buf[off+4:], en.count)
		binary.LittleEndian.PutUint32(buf[off+8:], en.val)
	}
	for i := 0; i < 8; i++ {
		buf[dataOff+i] = byte(i + 1)
	}
	return buf
}

func TestOpenRejects(t *testing.T) {
	bigEndian := []byte{'M', 'M', 0, 42, 0, 0, 0, 8}
	bigTIFF := []byte{'I', 'I', 43, 0, 8, 0, 0, 0}
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"big endian", bigEndian, "big-endian"},
		{"bigtiff", bigTIFF, "BigTIFF"},
		{"not a tiff", []byte("hello world, not a tiff"), "not a TIFF"},
		{"lzw compression", mkMinimalTIFF(5), "unsupported compression 5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cog.Open(context.Background(), &cog.BytesSource{Data: tc.data})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestUncompressedStrips(t *testing.T) {
	f := openCOG(t, mkMinimalTIFF(1))
	ras, err := f.ReadWindow(context.Background(), 0, 0, 0, 4, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if ras.U8[i] != byte(i+1) {
			t.Fatalf("pixel %d = %d, want %d", i, ras.U8[i], i+1)
		}
	}
}

// TestHeaderGrowth places tag data past the initial 64 KiB prefix so Open
// must fetch more of the header.
func TestHeaderGrowth(t *testing.T) {
	const arrOff = 70000
	const dataOff = arrOff + 16
	type e struct {
		tag, typ uint16
		count    uint32
		val      uint32
	}
	entries := []e{
		{256, 3, 1, 4},
		{257, 3, 1, 2},
		{258, 3, 1, 8},
		{259, 3, 1, 1},
		{273, 4, 2, arrOff}, // two strip offsets, stored out-of-line far away
		{277, 3, 1, 1},
		{278, 3, 1, 1},
		{279, 4, 2, arrOff + 8}, // two strip byte counts
	}
	buf := make([]byte, dataOff+8)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 42)
	binary.LittleEndian.PutUint32(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[8:], uint16(len(entries)))
	for i, en := range entries {
		off := 10 + i*12
		binary.LittleEndian.PutUint16(buf[off:], en.tag)
		binary.LittleEndian.PutUint16(buf[off+2:], en.typ)
		binary.LittleEndian.PutUint32(buf[off+4:], en.count)
		binary.LittleEndian.PutUint32(buf[off+8:], en.val)
	}
	binary.LittleEndian.PutUint32(buf[arrOff:], dataOff)
	binary.LittleEndian.PutUint32(buf[arrOff+4:], dataOff+4)
	binary.LittleEndian.PutUint32(buf[arrOff+8:], 4)
	binary.LittleEndian.PutUint32(buf[arrOff+12:], 4)
	for i := 0; i < 8; i++ {
		buf[dataOff+i] = byte(10 + i)
	}
	f := openCOG(t, buf)
	ras, err := f.ReadWindow(context.Background(), 0, 0, 0, 4, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if ras.U8[i] != byte(10+i) {
			t.Fatalf("pixel %d = %d, want %d", i, ras.U8[i], 10+i)
		}
	}
}

func TestReadWindowOutOfBounds(t *testing.T) {
	f := openCOG(t, buildRGBCOG(t, 48, 40, 16, []int{1}, nil))
	if _, err := f.ReadWindow(context.Background(), 0, 40, 0, 20, 10, 1); err == nil {
		t.Error("expected out of bounds error")
	}
	if _, err := f.ReadWindow(context.Background(), 0, 0, 0, 0, 0, 1); err == nil {
		t.Error("expected empty window error")
	}
}
