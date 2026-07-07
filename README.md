# satfetch

Sentinel-2 satellite imagery by geo coordinates, from exclusively free and
keyless sources, in pure Go. No GDAL, no API keys, no accounts, no system
dependencies: one static binary.

satfetch asks the Element84 Earth Search STAC API for the most recent
low-cloud Sentinel-2 L2A scene covering a point, then reads only the tiles of
the scene's Cloud-Optimized GeoTIFFs that overlap the requested window using
HTTP range requests against the public sentinel-cogs bucket. A 5 km window
transfers a few MB instead of the ~800 MB granule. Products are rendered with
the Go standard library and cached on disk.

## Products

- True color (`/image`): PNG, JPEG or GeoTIFF from the 10 m visual asset.
- NDVI (`/ndvi`): vegetation index from the red and nir bands, as a
  color-ramp PNG or a Float32 GeoTIFF.
- Scene listing (`/scenes`): JSON metadata of matching acquisitions.
- Orthophotos (`/ortho`): centimeter-class aerial imagery from keyless
  national WMS services (`/sources` lists them).

## Build and run

```
CGO_ENABLED=0 go build ./cmd/satfetch
./satfetch -listen :8080 -cache-dir ./cache
```

Flags (each overridable by a `SATFETCH_*` environment variable):

| Flag | Default | |
|---|---|---|
| `-listen` | `:8080` | listen address (`SATFETCH_LISTEN`) |
| `-cache-dir` | `./cache` | product cache directory (`SATFETCH_CACHE_DIR`) |
| `-cache-max-mb` | `2048` | cache size cap (`SATFETCH_CACHE_MAX_MB`) |
| `-stac-url` | Earth Search v1 | STAC API root (`SATFETCH_STAC_URL`) |
| `-build-timeout` | `60s` | per-product build timeout (`SATFETCH_BUILD_TIMEOUT`) |
| `-max-concurrent-builds` | `4` | concurrent product builds (`SATFETCH_MAX_CONCURRENT_BUILDS`) |
| `-log-format` | `text` | `text` or `json` (`SATFETCH_LOG_FORMAT`) |

## HTTP API

`GET /image` and `GET /ndvi`:

| Param | Default | Constraints |
|---|---|---|
| `lat` | required | -90..90 |
| `lon` | required | -180..180 |
| `size_km` | 5 | 0.5..50 |
| `max_cloud` | 20 | 0..100 |
| `days` | 30 | 1..365 |
| `format` | `png` | `png`, `jpeg`, `gtiff` (`/ndvi`: no jpeg) |
| `scene_id` | - | pin a specific scene, skips the search |
| `max_px` | 0 | bound output pixels per side via overview selection |

Success responses carry `X-Scene-ID`, `X-Scene-Datetime`,
`X-Scene-Cloud-Cover` and `X-Cache: HIT|MISS`. Errors are JSON
`{"error": "..."}` with 400 (invalid), 404 (no scene), 502 (upstream, with
`Retry-After: 60`) or 504 (timeout).

`GET /scenes` takes `lat`, `lon`, `max_cloud` (default 100), `days` (default
90) and `limit` (default 20, max 50). `GET /healthz` and `GET /metrics`
(plain-text counters) round out the surface.

`GET /ortho` serves centimeter-class orthophotos rendered by national WMS
services instead of Sentinel-2:

| Param | Default | Constraints |
|---|---|---|
| `lat`, `lon` | required | as above |
| `source` | required | a name from `GET /sources` |
| `size_km` | 1 | 0.1..10 |
| `px` | 1024 | 64..4096, output width and height |
| `format` | `jpeg` | `jpeg`, `png` |

Success responses carry `X-Source`, `X-Source-GSD` and `X-Cache`. Built-in
sources (every entry verified with a live keyless fetch). WMS sources pass
the server-rendered image through; tile sources mosaic and crop WebMercator
pyramid tiles client-side and re-encode (jpeg quality 85), with output
bounded by `px` (the result lands between px/2 and px per side):

| name | type | coverage | native GSD | data |
|---|---|---|---|---|
| `pl` | wms | Poland | 0.25 m | GUGiK geoportal.gov.pl |
| `pl-hires` | wms | Poland (cities) | 0.10 m | GUGiK geoportal.gov.pl |
| `nl` | wms | Netherlands | 0.08 m | Beeldmateriaal Nederland via PDOK, CC BY 4.0 |
| `nl-25` | wms | Netherlands | 0.25 m | Beeldmateriaal Nederland via PDOK, CC BY 4.0 |
| `fr` | wms | France | 0.20 m | IGN France, Geoplateforme |
| `ch` | wms | Switzerland | 0.10 m | swisstopo SWISSIMAGE |
| `es` | wms | Spain | 0.25 m | IGN Spain PNOA, CC BY 4.0 |
| `de-nrw` | wms | Germany, North Rhine-Westphalia | 0.10 m | Geobasis NRW, dl-zero-de/2.0 |
| `de-by` | wms | Germany, Bavaria | 0.40 m | Bayerische Vermessungsverwaltung, CC BY 4.0 |
| `be-vl` | wms | Belgium, Flanders | 0.25 m | Digitaal Vlaanderen |
| `lu` | wms | Luxembourg | 0.20 m | geoportail.lu |
| `sk` | wms | Slovakia | 0.20 m | GKU Bratislava |
| `pt` | wms | Portugal | 0.30 m | DGT OrtoSat 2023 |
| `us` | wms | USA | 0.6-1 m | USGS The National Map, public domain |
| `de-bb` | wms | Germany, Berlin + Brandenburg | 0.20 m | Geobasis Berlin-Brandenburg |
| `de-ni` | wms | Germany, Lower Saxony | 0.20 m | LGLN, CC BY 4.0 |
| `de-he` | wms | Germany, Hessen | 0.20 m | HVBG |
| `de-sn` | wms | Germany, Saxony | 0.20 m | GeoSN |
| `de-st` | wms | Germany, Saxony-Anhalt | 0.20 m | LVermGeo ST |
| `de-th` | wms | Germany, Thuringia | 0.20 m | TLBG |
| `de-mv` | wms | Germany, Mecklenburg-Vorpommern | 0.20 m | GeoBasis-DE/M-V |
| `de-sh` | wms | Germany, Schleswig-Holstein | 0.20 m | GDI-SH |
| `de-rp` | wms | Germany, Rhineland-Palatinate | 0.20 m | LVermGeo RP |
| `be-wa` | wms | Belgium, Wallonia | 0.25 m | Service public de Wallonie |
| `es-ct` | wms | Spain, Catalonia | 0.25 m | ICGC, CC BY 4.0 |
| `at` | tiles | Austria | 0.30 m | basemap.at, CC BY 4.0 |
| `cz` | tiles | Czechia | 0.20 m | CUZK |
| `ee` | tiles | Estonia | 0.16 m | Estonian Land Board (Maa-amet) |
| `jp` | tiles | Japan | ~0.20 m | GSI Japan seamless photo |
| `tw` | tiles | Taiwan | ~0.25 m | NLSC Taiwan |
| `za` | tiles | South Africa | 0.25 m | NGI via openstreetmap.org.za |
| `si` | arcgis | Slovenia | 0.26 m | GURS DOF via ARSO |
| `au-nsw` | arcgis | Australia, New South Wales | ~0.10 m | NSW Spatial Services, CC BY 4.0 |

Orthophotos are flown on multi-year cycles (not current like Sentinel-2), and
requests outside a source's coverage come back blank. For native detail keep
`size_km * 1000 / px` near the source GSD. Library callers can register any
WMS 1.3.0 endpoint that serves EPSG:4326 via `Options.WMSSources`, any
WebMercator tile pyramid (WMTS/XYZ templates with `{z}/{x}/{y}`, TMS via
`{-y}`) via `Options.TileSources`, and any keyless ArcGIS MapServer export
endpoint via `Options.ArcGISSources`.

Countries and regions checked but not included, and why: Denmark, Finland,
Norway and Lithuania gate their services behind (free) tokens or accounts;
Sweden, the UK, Ireland and Hungary have no open national orthophoto
service; Latvia's and Brussels' servers were unreachable; South Tyrol's WMS
refuses EPSG:4326; Indonesia publishes only dozens of per-region ArcGIS
Image Services (no national mosaic, different export operation); Thailand's
GISTDA sphere platform is key-gated; Egypt has no open national imagery
service; Australia beyond NSW (Victoria, Queensland) uses ArcGIS Image
Services too. Baden-Wuerttemberg charges fees; Berlin is covered by the
joint Berlin-Brandenburg mosaic (de-bb). Non-WebMercator tile grids are not
supported. (Slovenia's canonical GURS WMS was failing server-side when
checked; the si source uses the ARSO-hosted ArcGIS mirror of the same DOF
data instead.)

Smoke test:

```
curl -v 'localhost:8080/image?lat=50.2649&lon=19.0238&size_km=5&max_cloud=20' -o kat.png
curl -s 'localhost:8080/scenes?lat=50.2649&lon=19.0238&days=60'
curl -v 'localhost:8080/ndvi?lat=50.2649&lon=19.0238&size_km=5' -o kat-ndvi.png
```

## Library use

The root package is importable; the HTTP server is a thin adapter over it.

```go
svc, err := satfetch.New(satfetch.Options{
        Catalog:  satfetch.NewEarthSearch(satfetch.EarthSearchOptions{}),
        CacheDir: "/var/cache/satfetch",
})
res, err := svc.Image(ctx, satfetch.ImageRequest{
        Lat: 50.2649, Lon: 19.0238, SizeKM: 5,
        Format: satfetch.FormatJPEG, MaxPx: 1024,
})
// res.Path is the rendered file; res.Scene carries id, datetime, cloud cover.
```

`Catalog` is an interface, so an alternative STAC provider (for example
Microsoft Planetary Computer, whose asset hrefs need SAS signing via the
`HrefHook` seam) can be added without touching the pipeline.

## Notes and limitations

- Earth Search is a public best-effort service with no SLA. Requests retry
  with backoff and surface 502 with a Retry-After hint when it is down.
- Windows crossing the antimeridian are rejected; windows partially outside
  the selected granule are clamped (expect black fill at granule edges).
- NDVI is computed on raw digital numbers without the processing-baseline
  offset, which is fine for visualization.
- The design details live in docs/SPEC.md.

## License

ISC. See LICENSE.
