// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Package render turns decoded rasters into deliverable images: true-color
// RGB, NDVI band math and its color-ramp visualization.
package render

import (
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"

	"github.com/karamble/satfetch/internal/cog"
)

// TrueColor converts a 3-sample uint8 raster into an opaque image.
func TrueColor(r *cog.Raster) (*image.NRGBA, error) {
	if r.Bits != 8 || r.SPP != 3 {
		return nil, fmt.Errorf("render: expected 8-bit RGB raster, have %d-bit x%d", r.Bits, r.SPP)
	}
	img := image.NewNRGBA(image.Rect(0, 0, r.W, r.H))
	for i := 0; i < r.W*r.H; i++ {
		img.Pix[i*4+0] = r.U8[i*3+0]
		img.Pix[i*4+1] = r.U8[i*3+1]
		img.Pix[i*4+2] = r.U8[i*3+2]
		img.Pix[i*4+3] = 0xff
	}
	return img, nil
}

// NDVI computes (nir-red)/(nir+red) from two single-band uint16 rasters of
// identical dimensions. Pixels where both bands are 0 (the Sentinel-2 nodata
// value) or the denominator is 0 come back as NaN.
func NDVI(red, nir *cog.Raster) ([]float32, error) {
	if red.Bits != 16 || red.SPP != 1 || nir.Bits != 16 || nir.SPP != 1 {
		return nil, fmt.Errorf("render: NDVI needs single-band 16-bit rasters")
	}
	if red.W != nir.W || red.H != nir.H {
		return nil, fmt.Errorf("render: band dimensions differ: %dx%d vs %dx%d", red.W, red.H, nir.W, nir.H)
	}
	nan := float32(math.NaN())
	out := make([]float32, red.W*red.H)
	for i := range out {
		r := float32(red.U16[i])
		n := float32(nir.U16[i])
		if (r == 0 && n == 0) || r+n == 0 {
			out[i] = nan
			continue
		}
		out[i] = (n - r) / (n + r)
	}
	return out, nil
}

type rampStop struct {
	v       float32
	r, g, b uint8
}

// ndviRamp colors vegetation indexes: bare/soil browns and beiges into
// greens. NaN renders transparent.
var ndviRamp = []rampStop{
	{-1, 139, 90, 43},
	{0, 222, 214, 180},
	{0.3, 145, 200, 120},
	{0.8, 20, 90, 30},
}

// NDVIImage colorizes NDVI values with the fixed ramp.
func NDVIImage(vals []float32, w, h int) (*image.NRGBA, error) {
	if len(vals) != w*h {
		return nil, fmt.Errorf("render: have %d values, expected %d", len(vals), w*h)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i, v := range vals {
		if math.IsNaN(float64(v)) {
			continue // zero pixel = transparent
		}
		r, g, b := rampColor(v)
		img.Pix[i*4+0] = r
		img.Pix[i*4+1] = g
		img.Pix[i*4+2] = b
		img.Pix[i*4+3] = 0xff
	}
	return img, nil
}

func rampColor(v float32) (uint8, uint8, uint8) {
	if v <= ndviRamp[0].v {
		s := ndviRamp[0]
		return s.r, s.g, s.b
	}
	last := ndviRamp[len(ndviRamp)-1]
	if v >= last.v {
		return last.r, last.g, last.b
	}
	for i := 1; i < len(ndviRamp); i++ {
		if v <= ndviRamp[i].v {
			lo, hi := ndviRamp[i-1], ndviRamp[i]
			t := (v - lo.v) / (hi.v - lo.v)
			lerp := func(a, b uint8) uint8 {
				return uint8(float32(a) + t*(float32(b)-float32(a)) + 0.5)
			}
			return lerp(lo.r, hi.r), lerp(lo.g, hi.g), lerp(lo.b, hi.b)
		}
	}
	return last.r, last.g, last.b
}

// EncodePNG writes img as PNG.
func EncodePNG(w io.Writer, img image.Image) error {
	return png.Encode(w, img)
}

// EncodeJPEG writes img as JPEG at the given quality (1-100).
func EncodeJPEG(w io.Writer, img image.Image, quality int) error {
	if quality <= 0 || quality > 100 {
		quality = 85
	}
	return jpeg.Encode(w, img, &jpeg.Options{Quality: quality})
}
