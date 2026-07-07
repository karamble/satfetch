// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package satfetch

// TileSource describes a keyless WebMercator tile pyramid (WMTS, XYZ or
// TMS). The URL template must contain {z}, {x} and {y} (or {-y} for TMS
// row order). Zero TileSize means 256.
type TileSource struct {
	Name        string
	URLTemplate string
	GSD         float64 // best native meters per pixel, informational
	MaxZoom     int     // deepest zoom level the pyramid serves
	TileSize    int
	Attribution string
}

// BuiltinTileSources returns the bundled tile pyramids, each verified with
// a live keyless tile fetch.
func BuiltinTileSources() []TileSource {
	return []TileSource{
		{
			Name:        "at",
			URLTemplate: "https://mapsneu.wien.gv.at/basemap/bmaporthofoto30cm/normal/google3857/{z}/{y}/{x}.jpeg",
			GSD:         0.3,
			MaxZoom:     19,
			Attribution: "basemap.at, CC BY 4.0",
		},
		{
			Name:        "cz",
			URLTemplate: "https://ags.cuzk.cz/arcgis1/rest/services/ORTOFOTO_WM/MapServer/WMTS/tile/1.0.0/ORTOFOTO_WM/default/default028mm/{z}/{y}/{x}",
			GSD:         0.2,
			MaxZoom:     19,
			Attribution: "CUZK, Czech Office for Surveying, Mapping and Cadastre",
		},
		{
			Name:        "ee",
			URLTemplate: "https://tiles.maaamet.ee/tm/tms/1.0.0/foto@GMC/{z}/{x}/{-y}.png",
			GSD:         0.16,
			MaxZoom:     18,
			Attribution: "Estonian Land Board (Maa-amet)",
		},
	}
}
