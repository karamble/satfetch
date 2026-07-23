// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package satfetch

// STACSource describes an orthophoto collection published as Cloud-Optimized
// GeoTIFFs behind a STAC catalog, read the same way as the Sentinel-2 scene
// products rather than rendered by a remote map server. Zero MaxPx means 4096
// and zero Days means ten years, the flight cycle aerial programs run on.
type STACSource struct {
	Name       string
	STACURL    string // STAC API root
	Collection string
	Asset      string // asset key holding the COG
	// SignTokenURL names a keyless endpoint handing out shared access
	// signatures for the asset host. Empty leaves hrefs untouched.
	SignTokenURL string
	GSD          float64 // nominal, for the source listing; items report their own
	Days         int
	MaxPx        int
	Attribution  string
}

// BuiltinSTACSources returns the bundled STAC orthophoto sources, each
// verified with a live keyless fetch.
func BuiltinSTACSources() []STACSource {
	return []STACSource{
		{
			// The Earth Search copy of NAIP sits in a requester-pays
			// bucket, which rules it out; the Planetary Computer copy
			// is keyless.
			Name:         "us-naip",
			STACURL:      "https://planetarycomputer.microsoft.com/api/stac/v1",
			Collection:   "naip",
			Asset:        "image",
			SignTokenURL: "https://planetarycomputer.microsoft.com/api/sas/v1/token/naip",
			GSD:          0.6,
			Attribution:  "USDA NAIP via Microsoft Planetary Computer, public domain",
		},
	}
}
