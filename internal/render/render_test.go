// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package render

import (
	"math"
	"testing"

	"github.com/karamble/satfetch/internal/cog"
)

func band(w, h int, vals []uint16) *cog.Raster {
	return &cog.Raster{W: w, H: h, SPP: 1, Bits: 16, U16: vals}
}

func TestNDVI(t *testing.T) {
	red := band(3, 1, []uint16{1000, 0, 2000})
	nir := band(3, 1, []uint16{3000, 0, 2000})
	vals, err := NDVI(red, nir)
	if err != nil {
		t.Fatal(err)
	}
	if vals[0] != 0.5 {
		t.Errorf("vals[0] = %f, want 0.5", vals[0])
	}
	if !math.IsNaN(float64(vals[1])) {
		t.Errorf("vals[1] = %f, want NaN (nodata)", vals[1])
	}
	if vals[2] != 0 {
		t.Errorf("vals[2] = %f, want 0", vals[2])
	}
}

func TestNDVIDimensionMismatch(t *testing.T) {
	if _, err := NDVI(band(2, 1, make([]uint16, 2)), band(3, 1, make([]uint16, 3))); err == nil {
		t.Error("expected dimension error")
	}
	rgb := &cog.Raster{W: 1, H: 1, SPP: 3, Bits: 8, U8: make([]uint8, 3)}
	if _, err := NDVI(rgb, rgb); err == nil {
		t.Error("expected band type error")
	}
}

func TestNDVIImageRamp(t *testing.T) {
	nan := float32(math.NaN())
	vals := []float32{-2, -1, 0, 0.15, 0.5, 0.8, 2, nan}
	img, err := NDVIImage(vals, len(vals), 1)
	if err != nil {
		t.Fatal(err)
	}
	want := [][4]uint8{
		{139, 90, 43, 255},   // clamped below
		{139, 90, 43, 255},   // brown endpoint
		{222, 214, 180, 255}, // beige stop
		{184, 207, 150, 255}, // midpoint beige..light green
		{95, 156, 84, 255},   // 0.4 of the way light..dark green
		{20, 90, 30, 255},    // dark green endpoint
		{20, 90, 30, 255},    // clamped above
		{0, 0, 0, 0},         // nodata transparent
	}
	for i, w := range want {
		got := [4]uint8{img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3]}
		if got != w {
			t.Errorf("pixel %d (v=%f) = %v, want %v", i, vals[i], got, w)
		}
	}
}

func TestTrueColor(t *testing.T) {
	ras := &cog.Raster{W: 2, H: 1, SPP: 3, Bits: 8, U8: []uint8{1, 2, 3, 250, 251, 252}}
	img, err := TrueColor(ras)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint8{1, 2, 3, 255, 250, 251, 252, 255}
	for i, w := range want {
		if img.Pix[i] != w {
			t.Errorf("pix[%d] = %d, want %d", i, img.Pix[i], w)
		}
	}
	if _, err := TrueColor(band(1, 1, make([]uint16, 1))); err == nil {
		t.Error("expected raster type error")
	}
}

// RGB+NIR imagery such as NAIP carries a fourth band, which true color drops.
func TestTrueColorDropsNIR(t *testing.T) {
	ras := &cog.Raster{W: 2, H: 1, SPP: 4, Bits: 8, U8: []uint8{1, 2, 3, 200, 250, 251, 252, 201}}
	img, err := TrueColor(ras)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint8{1, 2, 3, 255, 250, 251, 252, 255}
	for i, w := range want {
		if img.Pix[i] != w {
			t.Errorf("pix[%d] = %d, want %d", i, img.Pix[i], w)
		}
	}
}
