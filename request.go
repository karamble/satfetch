// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package satfetch

import (
	"fmt"
	"math"
)

// Format selects the output encoding of a product.
type Format string

const (
	FormatPNG   Format = "png"
	FormatJPEG  Format = "jpeg"
	FormatGTiff Format = "gtiff"
)

const (
	productImage = "image"
	productNDVI  = "ndvi"
)

// ImageRequest asks for an imagery product around a point. Zero values take
// the documented defaults; note that means MaxCloud 0 selects the default 20
// (pass a small positive value for a strictly cloudless search).
type ImageRequest struct {
	Lat, Lon    float64
	SizeKM      float64 // AOI edge length, 0.5..50, default 5
	MaxCloud    float64 // percent, 0..100, default 20
	Days        int     // lookback window, 1..365, default 30
	SceneID     string  // pin a specific scene, skips the search
	Format      Format  // default png; NDVI allows png|gtiff
	MaxPx       int     // bound output pixels per side via overview selection; 0 = native
	JPEGQuality int     // 1..100, default 85
}

// ScenesRequest asks for the scene listing around a point.
type ScenesRequest struct {
	Lat, Lon float64
	MaxCloud float64 // default 100
	Days     int     // default 90
	Limit    int     // default 20, max 50
}

// OrthoRequest asks for a server-rendered orthophoto around a point from a
// configured WMS source.
type OrthoRequest struct {
	Lat, Lon float64
	SizeKM   float64 // AOI edge length, 0.1..10, default 1
	Source   string  // WMS source name, required
	Format   Format  // png or jpeg, default jpeg
	Px       int     // output width and height, 64..4096, default 1024
}

func (r *OrthoRequest) normalize() error {
	if err := checkPoint(r.Lat, r.Lon); err != nil {
		return err
	}
	if r.Source == "" {
		return fmt.Errorf("%w: source required", ErrInvalid)
	}
	if r.SizeKM == 0 {
		r.SizeKM = 1
	}
	if r.SizeKM < 0.1 || r.SizeKM > 10 {
		return fmt.Errorf("%w: size_km %v out of range 0.1..10", ErrInvalid, r.SizeKM)
	}
	if r.Format == "" {
		r.Format = FormatJPEG
	}
	switch r.Format {
	case FormatPNG, FormatJPEG:
	default:
		return fmt.Errorf("%w: ortho supports png or jpeg", ErrInvalid)
	}
	if r.Px == 0 {
		r.Px = 1024
	}
	if r.Px < 64 || r.Px > 4096 {
		return fmt.Errorf("%w: px %d out of range 64..4096", ErrInvalid, r.Px)
	}
	return nil
}

func checkPoint(lat, lon float64) error {
	if math.IsNaN(lat) || lat < -90 || lat > 90 {
		return fmt.Errorf("%w: lat %v out of range -90..90", ErrInvalid, lat)
	}
	if math.IsNaN(lon) || lon < -180 || lon > 180 {
		return fmt.Errorf("%w: lon %v out of range -180..180", ErrInvalid, lon)
	}
	return nil
}

func (r *ImageRequest) normalize(product string) error {
	if err := checkPoint(r.Lat, r.Lon); err != nil {
		return err
	}
	if r.SizeKM == 0 {
		r.SizeKM = 5
	}
	if r.SizeKM < 0.5 || r.SizeKM > 50 {
		return fmt.Errorf("%w: size_km %v out of range 0.5..50", ErrInvalid, r.SizeKM)
	}
	if r.MaxCloud == 0 {
		r.MaxCloud = 20
	}
	if r.MaxCloud < 0 || r.MaxCloud > 100 {
		return fmt.Errorf("%w: max_cloud %v out of range 0..100", ErrInvalid, r.MaxCloud)
	}
	if r.Days == 0 {
		r.Days = 30
	}
	if r.Days < 1 || r.Days > 365 {
		return fmt.Errorf("%w: days %d out of range 1..365", ErrInvalid, r.Days)
	}
	if r.Format == "" {
		r.Format = FormatPNG
	}
	switch r.Format {
	case FormatPNG, FormatGTiff:
	case FormatJPEG:
		if product == productNDVI {
			return fmt.Errorf("%w: ndvi supports png or gtiff", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown format %q", ErrInvalid, r.Format)
	}
	if r.MaxPx < 0 || r.MaxPx > 1<<14 {
		return fmt.Errorf("%w: max_px %d out of range 0..%d", ErrInvalid, r.MaxPx, 1<<14)
	}
	if r.JPEGQuality == 0 {
		r.JPEGQuality = 85
	}
	if r.JPEGQuality < 1 || r.JPEGQuality > 100 {
		return fmt.Errorf("%w: jpeg quality %d out of range 1..100", ErrInvalid, r.JPEGQuality)
	}
	return nil
}

func (r *ScenesRequest) normalize() error {
	if err := checkPoint(r.Lat, r.Lon); err != nil {
		return err
	}
	if r.MaxCloud == 0 {
		r.MaxCloud = 100
	}
	if r.MaxCloud < 0 || r.MaxCloud > 100 {
		return fmt.Errorf("%w: max_cloud %v out of range 0..100", ErrInvalid, r.MaxCloud)
	}
	if r.Days == 0 {
		r.Days = 90
	}
	if r.Days < 1 || r.Days > 365 {
		return fmt.Errorf("%w: days %d out of range 1..365", ErrInvalid, r.Days)
	}
	if r.Limit == 0 {
		r.Limit = 20
	}
	if r.Limit < 1 || r.Limit > 50 {
		return fmt.Errorf("%w: limit %d out of range 1..50", ErrInvalid, r.Limit)
	}
	return nil
}
