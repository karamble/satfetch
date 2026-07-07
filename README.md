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
sources (all keyless, verified):

| name | coverage | native GSD | data |
|---|---|---|---|
| `pl` | Poland | 0.25 m | GUGiK geoportal.gov.pl |
| `pl-hires` | Poland (cities) | 0.10 m | GUGiK geoportal.gov.pl |
| `nl` | Netherlands | 0.08 m | Beeldmateriaal Nederland via PDOK, CC BY 4.0 |
| `nl-25` | Netherlands | 0.25 m | Beeldmateriaal Nederland via PDOK, CC BY 4.0 |

Orthophotos are flown on multi-year cycles (not current like Sentinel-2), and
requests outside a source's national coverage come back blank. For native
detail keep `size_km * 1000 / px` near the source GSD. Library callers can
register any WMS 1.3.0 endpoint via `Options.WMSSources`.

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
