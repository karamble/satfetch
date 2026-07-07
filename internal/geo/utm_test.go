// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package geo

import (
	"math"
	"testing"
)

// Northing goldens on the central meridian equal k0 times the meridian arc,
// computed independently by numeric integration of the WGS84 arc integral.
func TestLatLonToUTM(t *testing.T) {
	tests := []struct {
		name         string
		epsg         int
		lat, lon     float64
		wantE, wantN float64
		tolE, tolN   float64
	}{
		{"equator on central meridian", 32633, 0, 15, 500000, 0, 1e-6, 1e-6},
		{"meridian arc 45N", 32633, 45, 15, 500000, 4982950.400, 1e-6, 0.5},
		{"meridian arc 50.2649N", 32633, 50.2649, 15, 500000, 5568084.172, 1e-6, 0.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, n, err := LatLonToUTM(tc.epsg, tc.lat, tc.lon)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(e-tc.wantE) > tc.tolE {
				t.Errorf("easting %f, want %f", e, tc.wantE)
			}
			if math.Abs(n-tc.wantN) > tc.tolN {
				t.Errorf("northing %f, want %f", n, tc.wantN)
			}
		})
	}
}

func TestLatLonToUTMSymmetry(t *testing.T) {
	e1, n1, err := LatLonToUTM(32633, 50, 16)
	if err != nil {
		t.Fatal(err)
	}
	e2, n2, err := LatLonToUTM(32633, 50, 14)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(e1+e2-1e6) > 1e-4 {
		t.Errorf("eastings not symmetric about the central meridian: %f + %f", e1, e2)
	}
	if math.Abs(n1-n2) > 1e-4 {
		t.Errorf("northings differ across the central meridian: %f vs %f", n1, n2)
	}
}

func TestLatLonToUTMSouth(t *testing.T) {
	_, nn, err := LatLonToUTM(32633, 10, 15)
	if err != nil {
		t.Fatal(err)
	}
	_, ns, err := LatLonToUTM(32733, -10, 15)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(ns-(1e7-nn)) > 1e-4 {
		t.Errorf("south northing %f, want %f", ns, 1e7-nn)
	}
}

// The probed scene S2A_33UYR_20260702_1_L2A reports proj:centroid
// 50.00314,18.55689 and covers eastings 699960..809760, northings
// 5490240..5600040 in EPSG:32633.
func TestLatLonToUTMSceneCentroid(t *testing.T) {
	e, n, err := LatLonToUTM(32633, 50.00314, 18.55689)
	if err != nil {
		t.Fatal(err)
	}
	if e < 699960 || e > 809760 {
		t.Errorf("easting %f outside granule extent", e)
	}
	if n < 5490240 || n > 5600040 {
		t.Errorf("northing %f outside granule extent", n)
	}
}

func TestLatLonToUTMErrors(t *testing.T) {
	tests := []struct {
		name     string
		epsg     int
		lat, lon float64
	}{
		{"not a UTM CRS", 4326, 50, 19},
		{"zone zero", 32600, 50, 19},
		{"zone beyond 60", 32661, 50, 19},
		{"latitude beyond UTM", 32633, 89, 15},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := LatLonToUTM(tc.epsg, tc.lat, tc.lon); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestAOIBBox(t *testing.T) {
	minX, minY, maxX, maxY, err := AOIBBox(32633, 50.2649, 19.0238, 5)
	if err != nil {
		t.Fatal(err)
	}
	if w := maxX - minX; w < 4800 || w > 5400 {
		t.Errorf("bbox width %f, want ~5000", w)
	}
	if h := maxY - minY; h < 4800 || h > 5400 {
		t.Errorf("bbox height %f, want ~5000", h)
	}
	e, n, err := LatLonToUTM(32633, 50.2649, 19.0238)
	if err != nil {
		t.Fatal(err)
	}
	if e < minX || e > maxX || n < minY || n > maxY {
		t.Error("center point outside its own bbox")
	}
}

func TestAOIBBoxAntimeridian(t *testing.T) {
	if _, _, _, _, err := AOIBBox(32601, 60, 179.99, 50); err == nil {
		t.Error("expected antimeridian rejection")
	}
}
