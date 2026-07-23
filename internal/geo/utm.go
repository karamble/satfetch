// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Package geo projects WGS84 coordinates into UTM zones and derives crop
// windows. Accuracy is sub-meter (Snyder 1987 series), far tighter than the
// pixels the windows address.
package geo

import (
	"fmt"
	"math"
)

const (
	wgs84A = 6378137.0
	wgs84F = 1 / 298.257223563
	utmK0  = 0.9996
)

// LatLonToUTM projects a WGS84 coordinate into the UTM CRS identified by
// epsg (326xx = WGS84 north, 327xx = WGS84 south, 269xx = NAD83 north).
//
// NAD83 is projected on the same WGS84 ellipsoid here: GRS80 and WGS84 differ
// in flattening by well under a millimeter, and the NAD83 datum shift proper is
// 1-2 m across the conterminous US. At the 0.3-1 m pixels of NAIP, the imagery
// this affects, that is a two-to-three pixel offset - acceptable for a visual
// product, but not a substitute for a real datum transformation.
func LatLonToUTM(epsg int, lat, lon float64) (easting, northing float64, err error) {
	zone := epsg % 100
	south := false
	switch epsg / 100 {
	case 326, 269:
	case 327:
		south = true
	default:
		return 0, 0, fmt.Errorf("geo: unsupported CRS EPSG:%d", epsg)
	}
	if zone < 1 || zone > 60 {
		return 0, 0, fmt.Errorf("geo: bad UTM zone in EPSG:%d", epsg)
	}
	if lat < -85 || lat > 85 {
		return 0, 0, fmt.Errorf("geo: latitude %.4f out of UTM range", lat)
	}

	phi := lat * math.Pi / 180
	lam0 := float64(zone*6-183) * math.Pi / 180
	dl := lon*math.Pi/180 - lam0
	for dl > math.Pi {
		dl -= 2 * math.Pi
	}
	for dl < -math.Pi {
		dl += 2 * math.Pi
	}

	e2 := wgs84F * (2 - wgs84F)
	e4 := e2 * e2
	e6 := e4 * e2
	ep2 := e2 / (1 - e2)
	sinp, cosp := math.Sincos(phi)
	n := wgs84A / math.Sqrt(1-e2*sinp*sinp)
	t := (sinp / cosp) * (sinp / cosp)
	c := ep2 * cosp * cosp
	a := dl * cosp
	m := wgs84A * ((1-e2/4-3*e4/64-5*e6/256)*phi -
		(3*e2/8+3*e4/32+45*e6/1024)*math.Sin(2*phi) +
		(15*e4/256+45*e6/1024)*math.Sin(4*phi) -
		(35*e6/3072)*math.Sin(6*phi))

	a2 := a * a
	a3 := a2 * a
	easting = utmK0*n*(a+(1-t+c)*a3/6+(5-18*t+t*t+72*c-58*ep2)*a2*a3/120) + 500000
	northing = utmK0 * (m + n*(sinp/cosp)*(a2/2+(5-t+9*c+4*c*c)*a2*a2/24+(61-58*t+t*t+600*c-330*ep2)*a3*a3/720))
	if south {
		northing += 1e7
	}
	return easting, northing, nil
}

// AOIDegrees returns the half-extents in degrees of a sizeKM x sizeKM square
// centered on lat/lon.
func AOIDegrees(lat, lon, sizeKM float64) (dLat, dLon float64, err error) {
	dLat = (sizeKM / 2) / 110.574
	dLon = (sizeKM / 2) / (111.320 * math.Cos(lat*math.Pi/180))
	if lon-dLon < -180 || lon+dLon > 180 {
		return 0, 0, fmt.Errorf("geo: window crosses the antimeridian")
	}
	return dLat, dLon, nil
}

// AOIBBox returns the projected bounding box of a sizeKM x sizeKM square
// centered on lat/lon, by projecting its four corners into the given CRS.
func AOIBBox(epsg int, lat, lon, sizeKM float64) (minX, minY, maxX, maxY float64, err error) {
	dLat, dLon, err := AOIDegrees(lat, lon, sizeKM)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	corners := [4][2]float64{
		{lat + dLat, lon - dLon},
		{lat + dLat, lon + dLon},
		{lat - dLat, lon - dLon},
		{lat - dLat, lon + dLon},
	}
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, c := range corners {
		x, y, err := LatLonToUTM(epsg, c[0], c[1])
		if err != nil {
			return 0, 0, 0, 0, err
		}
		minX = math.Min(minX, x)
		maxX = math.Max(maxX, x)
		minY = math.Min(minY, y)
		maxY = math.Max(maxY, y)
	}
	return minX, minY, maxX, maxY, nil
}
