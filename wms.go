// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package satfetch

// WMSSource describes a keyless WMS orthophoto endpoint. Sources must accept
// WMS 1.3.0 GetMap requests with CRS EPSG:4326, whose BBOX axis order is
// lat,lon. Zero Version, CRS and MaxPx take the defaults 1.3.0, EPSG:4326
// and 4096.
type WMSSource struct {
	Name        string
	BaseURL     string
	Layers      string
	Version     string
	CRS         string
	GSD         float64 // native meters per pixel, informational
	MaxPx       int     // server dimension cap
	Attribution string
}

// BuiltinWMSSources returns the bundled, verified orthophoto sources.
// Orthophotos are flown on multi-year cycles; requests outside a source's
// national coverage come back blank.
func BuiltinWMSSources() []WMSSource {
	return []WMSSource{
		{
			Name:        "pl",
			BaseURL:     "https://mapy.geoportal.gov.pl/wss/service/PZGIK/ORTO/WMS/StandardResolution",
			Layers:      "Raster",
			GSD:         0.25,
			Attribution: "Head Office of Geodesy and Cartography (GUGiK), geoportal.gov.pl",
		},
		{
			Name:        "pl-hires",
			BaseURL:     "https://mapy.geoportal.gov.pl/wss/service/PZGIK/ORTO/WMS/HighResolution",
			Layers:      "Raster",
			GSD:         0.1,
			Attribution: "Head Office of Geodesy and Cartography (GUGiK), geoportal.gov.pl",
		},
		{
			Name:        "nl",
			BaseURL:     "https://service.pdok.nl/hwh/luchtfotorgb/wms/v1_0",
			Layers:      "Actueel_orthoHR",
			GSD:         0.08,
			Attribution: "Beeldmateriaal Nederland via PDOK, CC BY 4.0",
		},
		{
			Name:        "nl-25",
			BaseURL:     "https://service.pdok.nl/hwh/luchtfotorgb/wms/v1_0",
			Layers:      "Actueel_ortho25",
			GSD:         0.25,
			Attribution: "Beeldmateriaal Nederland via PDOK, CC BY 4.0",
		},
	}
}
