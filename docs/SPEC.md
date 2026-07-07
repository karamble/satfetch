# satfetch - Sentinel-2 Imagery Backend (Design)

Go backend returning satellite imagery and derived products (true-color
PNG/JPEG, NDVI, GeoTIFF) for arbitrary geo coordinates, using exclusively
free, keyless data sources. Single deployable static binary with reusable
library packages. 100% Go: the original GDAL-subprocess design was replaced
by a native COG reader before implementation.

## 1. Goals / Non-Goals

Goals
- `GET /image?lat&lon` returns a true-color crop of the most recent low-cloud
  Sentinel-2 scene around the point.
- `GET /ndvi?lat&lon` returns an NDVI raster (colorized PNG or Float32
  GeoTIFF).
- `GET /scenes?lat&lon` returns the matching scene metadata as JSON.
- Windowed COG reads only: never download full granules.
- Disk cache keyed on request identity, LRU-evicted to a size cap.
- Catalog behind an interface so a second STAC provider can be added.
- The core is an importable library (root package); the HTTP server is a
  thin adapter. A Bison Relay bot imports the library and serves the same
  capabilities as MCP tools.

Non-Goals (v1)
- No commercial imagery, no tasking, no Landsat (requester-pays bucket).
- No user auth (runs behind a reverse proxy or on localhost).
- No change detection.

## 2. Data sources

Primary catalog: Element84 Earth Search STAC API,
`https://earth-search.aws.element84.com/v1`, collection `sentinel-2-l2a`
(complete; the `sentinel-2-c1-l2a` collection has historical gaps). POST
`/search` with a GeoJSON point intersect, RFC3339 datetime range and an
`eo:cloud_cover` `lt` query. No auth, no key, no published rate limit, no
SLA.

Pixel data: Sentinel-2 L2A COGs on the public `sentinel-cogs` bucket
(us-west-2), unsigned anonymous reads, NOT requester-pays. STAC items carry
direct https hrefs. Relevant assets: `visual` (true-color 10 m), `red` (B04),
`nir` (B08), `scl`, `thumbnail`.

Verified structure of these COGs (probed 2026-07-07): classic little-endian
TIFF, DEFLATE (zlib) compression, predictor 2, chunky planar layout,
1024x1024 tiles at full resolution, four overview IFDs (/2 /4 /8 /16) with
512x512 tiles, geo tags (ModelPixelScale, ModelTiepoint, GeoKeyDirectory,
GDALNoData "0") on the full-resolution IFD.

Fallback catalog (interface only in v1): Microsoft Planetary Computer STAC.
Free, but asset hrefs require SAS-token signing; the `Catalog` interface and
the `HrefHook` option accommodate that without pipeline changes.

## 3. Architecture

```
satfetch.go / scene.go /        root package: Service facade, Options,
request.go / errors.go          request validation, Result
earthsearch.go                  STAC client (root package to keep the
                                facade one import for consumers)
internal/cog/                   COG reader: Source abstraction (HTTP range
                                requests with retries), IFD parser, overview
                                selection, concurrent tile fetch, zlib
                                inflate, predictor-2 undo, window assembly
internal/geo/                   WGS84 to UTM (Snyder series), AOI to
                                projected bbox to pixel window
internal/tiffw/                 GeoTIFF writer (striped deflate; uint8 RGB
                                and Float32) plus a tiled COG builder used
                                to make hermetic test fixtures
internal/render/                NDVI band math, color ramp, PNG/JPEG encode
internal/cache/                 disk cache: sha256 key, two-level fanout,
                                atomic temp+rename writes, mtime-LRU
                                eviction on start and every 10 minutes
internal/httpapi/               mux, handlers, validation, error mapping,
                                logging/recover middleware, /metrics
cmd/satfetch/                   flags + SATFETCH_* env, slog, graceful
                                shutdown
```

### Raster path (pure Go)

1. Open the COG: fetch the first 64 KiB (growing on demand, capped 4 MiB),
   parse the IFD chain and the full-resolution geo tags.
2. Project the AOI corners into the scene's UTM CRS (Snyder 1987 transverse
   Mercator series; sub-meter accuracy against a numerically integrated
   meridian arc) and derive the clamped pixel window.
3. Pick the finest overview level whose output fits `max_px` (never
   upsample); this bounds both output size and bytes fetched.
4. Fetch only the tiles intersecting the window (bounded concurrency, HTTP
   Range requests with retry), inflate, undo predictor 2, blit into the
   output raster. Sparse tiles (byte count 0) fill with nodata.
5. Render: PNG/JPEG via the standard library; GeoTIFF via the internal
   writer with the pixel scale, a tiepoint recomputed for the window origin,
   and the GeoKeyDirectory copied verbatim from the source. NDVI is
   (nir-red)/(nir+red) in float32 with 0/0 and both-bands-zero as nodata,
   colorized brown-beige-green (-1, 0, 0.3, 0.8 stops) with transparent
   nodata.

The reader rejects clearly: big-endian TIFF, BigTIFF, unsupported
compression/predictor/planar layout/bit depths.

### Cache

Key = sha256 of scene id, product, lat/lon, size, max_px, format and
quality, so hits are served without any upstream traffic (the scene search
itself is never cached: freshness matters and it is cheap). Files land as
`cache/ab/cd/<hex>.<ext>` via temp+rename. Concurrent identical requests
share one build (single-flight); distinct builds of the same key are safe by
last-rename-wins.

## 4. HTTP API

All endpoints GET. Errors: JSON `{"error":"..."}` with 400 invalid
parameters, 404 no scene found, 502 upstream failure (with `Retry-After:
60`), 504 build timeout.

`/image` and `/ndvi` parameters: `lat` (required, -90..90), `lon` (required,
-180..180), `size_km` (5, 0.5..50), `max_cloud` (20, 0..100), `days` (30,
1..365), `format` (`png` | `jpeg` | `gtiff`; `/ndvi` disallows jpeg),
`scene_id` (pin a scene, skips search), `max_px` (0 = native). `jpeg` and
`max_px` exist for delivery paths with hard byte budgets (the Bison Relay
inline embed cap is about 1 MiB).

Success headers: `X-Scene-ID`, `X-Scene-Datetime` (RFC3339),
`X-Scene-Cloud-Cover`, `X-Cache: HIT|MISS`.

`/scenes` parameters: `lat`, `lon`, `max_cloud` (100), `days` (90), `limit`
(20, max 50). Response: `{"scenes":[{"id","datetime","cloud_cover",
"thumbnail","assets":["visual","red","nir","scl"]}]}`.

`/ortho` parameters: `lat`, `lon` (required), `source` (required, a
registry name from `/sources`), `size_km` (1, 0.1..10), `px` (1024,
64..4096, output width and height; clamped to the source's server cap),
`format` (`jpeg` default | `png`). The WMS server renders the window, so
there is no scene, no cloud filter, no NDVI and no GeoTIFF here; success
headers are `X-Source`, `X-Source-GSD` and `X-Cache`. `/sources` returns
`{"sources":[{"name","gsd","attribution"}]}`.

`/healthz` returns `{"status":"ok"}` checking nothing external. `/metrics`
returns plain-text counters: requests by endpoint/status, cache hits, build
seconds sum/count, STAC errors, upstream bytes fetched.

### Ortho WMS sources

`Options.WMSSources` registers WMS 1.3.0 GetMap endpoints (default:
BuiltinWMSSources() - Poland GUGiK 25 cm and 10 cm, Netherlands PDOK 8 cm
and 25 cm, all keyless and live-verified). Requests always use CRS
EPSG:4326, whose 1.3.0 BBOX axis order is lat,lon. The AOI is square in
meters, so WIDTH = HEIGHT = px. WMS failures often arrive as HTTP 200 with
an XML service exception: any non-image content type is treated as an
upstream error (detail logged, never sent to clients). Fetches stream
straight into the cache and share the build semaphore, timeout and retry
policy with the scene pipeline.

## 5. Constraints and conventions

- Go >= 1.24 directive, standard library only, `CGO_ENABLED=0`.
- STAC client: 30 s timeout, 3 retries with 500 ms exponential backoff on
  5xx/network errors only. Range requests retry the same way.
- Every handler: validate, resolve scene, cache, build, serve. Context
  propagation end to end; server ReadTimeout 10 s, WriteTimeout 120 s;
  graceful shutdown with a 15 s drain.
- Upstream URLs and command detail stay in logs, never in client bodies.

## 6. Testing

Everything hermetic, no network: the tiffw builder writes synthetic tiled
COGs (deflate, predictor 2, geo tags) that the reader is tested against;
the writer is verified by parsing its own output back with the reader;
UTM goldens come from numerically integrated meridian arcs plus exactness
identities; the catalog client runs against httptest fixtures shaped like a
captured live response; the HTTP layer is tested end to end with a fixture
catalog and an httptest asset server (http.ServeContent provides Range
support).

Gates: `gofmt -l .`, `go vet ./...`, `go test ./...`,
`CGO_ENABLED=0 go build ./cmd/satfetch`.

## 7. Known limits

- Earth Search has no SLA; hence retries, Retry-After and the pluggable
  Catalog.
- Windows crossing the antimeridian are rejected. Windows partially outside
  the selected granule clamp to the granule (black edges).
- Older processing baselines could use different COG compression; the reader
  fails with a precise error rather than guessing.
- NDVI uses raw digital numbers (no baseline offset correction):
  visualization-grade.
