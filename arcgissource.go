// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package satfetch

// ArcGISSource describes a keyless ArcGIS MapServer whose export operation
// renders orthophoto windows. Zero MaxPx means 4096.
type ArcGISSource struct {
	Name        string
	BaseURL     string // service URL ending in /MapServer
	GSD         float64
	MaxPx       int
	Attribution string
}

// BuiltinArcGISSources returns the bundled ArcGIS export sources, each
// verified with a live keyless fetch.
func BuiltinArcGISSources() []ArcGISSource {
	return []ArcGISSource{
		{
			Name:        "si",
			BaseURL:     "https://gis.arso.gov.si/arcgis/rest/services/DOF_D96TM_2022_2023_2024/MapServer",
			GSD:         0.26,
			Attribution: "GURS DOF via ARSO, Slovenia",
		},
		{
			Name:        "au-nsw",
			BaseURL:     "https://maps.six.nsw.gov.au/arcgis/rest/services/public/NSW_Imagery/MapServer",
			GSD:         0.1,
			Attribution: "NSW Spatial Services, CC BY 4.0",
		},
	}
}
