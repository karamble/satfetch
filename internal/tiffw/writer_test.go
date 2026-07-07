// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package tiffw_test

import (
	"bytes"
	"context"
	"math"
	"testing"

	"github.com/karamble/satfetch/internal/cog"
	"github.com/karamble/satfetch/internal/tiffw"
)

var testGeo = tiffw.Geo{
	ScaleX:  20,
	ScaleY:  20,
	OriginX: 701240,
	OriginY: 5598760,
	KeyDir:  []uint16{1, 1, 0, 1, 3072, 0, 1, 32633},
	Ascii:   "WGS 84 / UTM zone 33N|WGS 84|",
	NoData:  "0",
}

func TestWriteRGB8RoundTrip(t *testing.T) {
	w, h := 130, 70
	pix := make([]uint8, w*h*3)
	for i := range pix {
		pix[i] = uint8((i*13 + 7) % 256)
	}
	var buf bytes.Buffer
	if err := tiffw.WriteRGB8(&buf, w, h, pix, testGeo); err != nil {
		t.Fatal(err)
	}
	f, err := cog.Open(context.Background(), &cog.BytesSource{Data: buf.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	if f.Grid.EPSG != 32633 || f.Grid.OriginX != testGeo.OriginX || f.Grid.ScaleX != 20 {
		t.Errorf("grid: %+v", f.Grid)
	}
	if f.NoData != "0" || f.GeoAscii != testGeo.Ascii {
		t.Errorf("nodata %q ascii %q", f.NoData, f.GeoAscii)
	}
	ras, err := f.ReadWindow(context.Background(), 0, 0, 0, w, h, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ras.U8, pix) {
		t.Error("pixels do not round-trip")
	}
}

func TestWriteRGB8MultiStrip(t *testing.T) {
	// 350*3 = 1050 row bytes puts ~998 rows per strip; 3000 rows = 4 strips.
	w, h := 350, 3000
	pix := make([]uint8, w*h*3)
	for i := range pix {
		pix[i] = uint8(i % 251)
	}
	var buf bytes.Buffer
	if err := tiffw.WriteRGB8(&buf, w, h, pix, tiffw.Geo{}); err != nil {
		t.Fatal(err)
	}
	f, err := cog.Open(context.Background(), &cog.BytesSource{Data: buf.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.IFDs[0].Offsets) < 3 {
		t.Fatalf("expected several strips, have %d", len(f.IFDs[0].Offsets))
	}
	ras, err := f.ReadWindow(context.Background(), 0, 100, 990, 30, 30, 2)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 30; y++ {
		for x := 0; x < 30; x++ {
			for c := 0; c < 3; c++ {
				want := uint8((((y+990)*w+(x+100))*3 + c) % 251)
				got := ras.U8[(y*30+x)*3+c]
				if got != want {
					t.Fatalf("pixel %d,%d,%d = %d, want %d", x, y, c, got, want)
				}
			}
		}
	}
}

func TestWriteFloat32RoundTrip(t *testing.T) {
	w, h := 33, 21
	vals := make([]float32, w*h)
	for i := range vals {
		vals[i] = float32(i)/100 - 1
	}
	vals[5] = float32(math.NaN())
	g := testGeo
	g.NoData = "nan"
	var buf bytes.Buffer
	if err := tiffw.WriteFloat32(&buf, w, h, vals, g); err != nil {
		t.Fatal(err)
	}
	f, err := cog.Open(context.Background(), &cog.BytesSource{Data: buf.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	if f.IFDs[0].Bits != 32 || f.IFDs[0].SampleFormat != 3 {
		t.Fatalf("bits %d sample format %d", f.IFDs[0].Bits, f.IFDs[0].SampleFormat)
	}
	if f.NoData != "nan" {
		t.Errorf("nodata %q, want nan", f.NoData)
	}
	ras, err := f.ReadWindow(context.Background(), 0, 0, 0, w, h, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range vals {
		got := ras.F32[i]
		if math.IsNaN(float64(want)) {
			if !math.IsNaN(float64(got)) {
				t.Fatalf("value %d = %f, want NaN", i, got)
			}
			continue
		}
		if got != want {
			t.Fatalf("value %d = %f, want %f", i, got, want)
		}
	}
}

func TestWriteSizeMismatch(t *testing.T) {
	if err := tiffw.WriteRGB8(&bytes.Buffer{}, 10, 10, make([]uint8, 5), tiffw.Geo{}); err == nil {
		t.Error("expected pixel count error")
	}
	if err := tiffw.WriteFloat32(&bytes.Buffer{}, 10, 10, make([]float32, 5), tiffw.Geo{}); err == nil {
		t.Error("expected value count error")
	}
}
