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

// BuiltinWMSSources returns the bundled orthophoto sources, each verified
// with a live keyless GetMap fetch. Orthophotos are flown on multi-year
// cycles; requests outside a source's coverage come back blank.
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
		{
			Name:        "fr",
			BaseURL:     "https://data.geopf.fr/wms-r",
			Layers:      "ORTHOIMAGERY.ORTHOPHOTOS",
			GSD:         0.2,
			Attribution: "IGN France, Geoplateforme",
		},
		{
			Name:        "ch",
			BaseURL:     "https://wms.geo.admin.ch/",
			Layers:      "ch.swisstopo.swissimage",
			GSD:         0.1,
			Attribution: "swisstopo, SWISSIMAGE",
		},
		{
			Name:        "es",
			BaseURL:     "https://www.ign.es/wms-inspire/pnoa-ma",
			Layers:      "OI.OrthoimageCoverage",
			GSD:         0.25,
			Attribution: "IGN Spain, PNOA, CC BY 4.0",
		},
		{
			Name:        "de-nrw",
			BaseURL:     "https://www.wms.nrw.de/geobasis/wms_nw_dop",
			Layers:      "nw_dop_rgb",
			GSD:         0.1,
			Attribution: "Geobasis NRW, dl-zero-de/2.0",
		},
		{
			Name:        "de-by",
			BaseURL:     "https://geoservices.bayern.de/od/wms/dop/v1/dop40",
			Layers:      "by_dop40c",
			GSD:         0.4,
			Attribution: "Bayerische Vermessungsverwaltung, CC BY 4.0",
		},
		{
			Name:        "be-vl",
			BaseURL:     "https://geo.api.vlaanderen.be/omw/wms",
			Layers:      "OMWRGB25VL",
			GSD:         0.25,
			Attribution: "Digitaal Vlaanderen",
		},
		{
			Name:        "lu",
			BaseURL:     "https://wms.geoportail.lu/opendata/service",
			Layers:      "Ortho",
			GSD:         0.2,
			Attribution: "geoportail.lu, Administration du cadastre et de la topographie",
		},
		{
			Name:        "sk",
			BaseURL:     "https://zbgisws.skgeodesy.sk/zbgis_ortofoto_wms/service.svc/get",
			Layers:      "1",
			GSD:         0.2,
			Attribution: "GKU Bratislava, ortofotomozaika SR",
		},
		{
			Name:        "pt",
			BaseURL:     "https://ortos.dgterritorio.gov.pt/wms/ortosat2023",
			Layers:      "ortoSat2023-CorVerdadeira",
			GSD:         0.3,
			Attribution: "DGT Portugal, OrtoSat 2023",
		},
		{
			Name:        "de-bb",
			BaseURL:     "https://isk.geobasis-bb.de/mapproxy/dop20c/service/wms",
			Layers:      "bebb_dop20c",
			GSD:         0.2,
			Attribution: "Geobasis Berlin-Brandenburg (covers Berlin and Brandenburg)",
		},
		{
			Name:        "de-ni",
			BaseURL:     "https://opendata.lgln.niedersachsen.de/doorman/noauth/dop_wms",
			Layers:      "ni_dop20",
			GSD:         0.2,
			Attribution: "LGLN Lower Saxony, CC BY 4.0",
		},
		{
			Name:        "de-he",
			BaseURL:     "https://gds-srv.hessen.de/cgi-bin/lika-services/ogc-free-images.ows",
			Layers:      "he_dop20_rgb",
			GSD:         0.2,
			Attribution: "HVBG Hessen",
		},
		{
			Name:        "de-sn",
			BaseURL:     "https://geodienste.sachsen.de/wms_geosn_dop-rgb/guest",
			Layers:      "sn_dop_020",
			GSD:         0.2,
			Attribution: "GeoSN Saxony",
		},
		{
			Name:        "de-st",
			BaseURL:     "https://www.geodatenportal.sachsen-anhalt.de/wss/service/ST_LVermGeo_DOP_WMS_OpenData/guest",
			Layers:      "lsa_lvermgeo_dop20_2",
			GSD:         0.2,
			Attribution: "LVermGeo Saxony-Anhalt",
		},
		{
			Name:        "de-th",
			BaseURL:     "https://www.geoproxy.geoportal-th.de/geoproxy/services/DOP20",
			Layers:      "th_dop",
			GSD:         0.2,
			Attribution: "TLBG Thuringia",
		},
		{
			Name:        "de-mv",
			BaseURL:     "https://www.geodaten-mv.de/dienste/adv_dop",
			Layers:      "mv_dop",
			GSD:         0.2,
			Attribution: "GeoBasis-DE/M-V Mecklenburg-Vorpommern",
		},
		{
			Name:        "de-sh",
			BaseURL:     "https://dienste.gdi-sh.de/WMS_SH_DOP20col_OpenGBD",
			Layers:      "sh_dop20_rgb",
			GSD:         0.2,
			Attribution: "GDI Schleswig-Holstein",
		},
		{
			Name:        "de-rp",
			BaseURL:     "https://geo4.service24.rlp.de/wms/rp_dop20.fcgi",
			Layers:      "rp_dop20",
			GSD:         0.2,
			Attribution: "LVermGeo Rhineland-Palatinate",
		},
		{
			Name:        "be-wa",
			BaseURL:     "https://geoservices.wallonie.be/arcgis/services/IMAGERIE/ORTHO_LAST/MapServer/WMSServer",
			Layers:      "0",
			GSD:         0.25,
			Attribution: "Service public de Wallonie (SPW)",
		},
		{
			Name:        "es-ct",
			BaseURL:     "https://geoserveis.icgc.cat/servei/catalunya/orto-territorial/wms",
			Layers:      "ortofoto_color_vigent",
			GSD:         0.25,
			Attribution: "ICGC Catalonia, CC BY 4.0",
		},
		{
			Name:        "us",
			BaseURL:     "https://basemap.nationalmap.gov/arcgis/services/USGSImageryOnly/MapServer/WmsServer",
			Layers:      "0",
			GSD:         0.6,
			Attribution: "USGS The National Map, public domain",
		},
	}
}
