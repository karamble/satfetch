// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Package tiles assembles orthophoto windows from WebMercator tile pyramids
// (WMTS/XYZ/TMS): it computes the tiles covering a geographic box at a zoom
// level, fetches them concurrently, and mosaics and crops them into one
// image.
package tiles

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MaxLat is the WebMercator latitude limit.
const MaxLat = 85.05112877980659

// maxTileBytes bounds a single tile download.
const maxTileBytes = 8 << 20

// Request describes one mosaic build. The URL template must contain {z},
// {x} and a row placeholder: {y} (XYZ/WMTS) or {-y} (TMS, flipped rows).
type Request struct {
	URLTemplate                    string
	MinLat, MinLon, MaxLat, MaxLon float64
	Zoom                           int
	TileSize                       int // default 256
	Concurrency                    int // default 4
}

// MetersPerPixel returns the WebMercator ground resolution at a latitude
// and zoom level for 256 px tiles.
func MetersPerPixel(lat float64, zoom int) float64 {
	return 156543.03392 * math.Cos(lat*math.Pi/180) / float64(int64(1)<<zoom)
}

// globalPx converts a coordinate to global pixel space at a zoom level.
func globalPx(lat, lon float64, zoom, tileSize int) (x, y float64) {
	n := float64(int64(1)<<zoom) * float64(tileSize)
	x = (lon + 180) / 360 * n
	rad := lat * math.Pi / 180
	y = (1 - math.Log(math.Tan(rad)+1/math.Cos(rad))/math.Pi) / 2 * n
	return x, y
}

// tileURL expands the template for one tile.
func tileURL(tmpl string, zoom, x, y int) string {
	return strings.NewReplacer(
		"{z}", strconv.Itoa(zoom),
		"{x}", strconv.Itoa(x),
		"{y}", strconv.Itoa(y),
		"{-y}", strconv.Itoa(int(int64(1)<<zoom)-1-y),
	).Replace(tmpl)
}

func validate(r Request) error {
	if !strings.Contains(r.URLTemplate, "{z}") || !strings.Contains(r.URLTemplate, "{x}") ||
		(!strings.Contains(r.URLTemplate, "{y}") && !strings.Contains(r.URLTemplate, "{-y}")) {
		return fmt.Errorf("tiles: template needs {z}, {x} and {y} or {-y}")
	}
	if math.Abs(r.MinLat) >= MaxLat || math.Abs(r.MaxLat) >= MaxLat {
		return fmt.Errorf("tiles: latitude outside the WebMercator range")
	}
	if r.MinLat >= r.MaxLat || r.MinLon >= r.MaxLon {
		return fmt.Errorf("tiles: empty box")
	}
	if r.Zoom < 0 || r.Zoom > 24 {
		return fmt.Errorf("tiles: zoom %d out of range", r.Zoom)
	}
	return nil
}

// Fetch builds the mosaic and returns it with the total bytes downloaded.
// Tiles answering 404 or 204 are outside the source's coverage and stay
// black.
func Fetch(ctx context.Context, client *http.Client, userAgent string, r Request) (*image.NRGBA, int64, error) {
	if err := validate(r); err != nil {
		return nil, 0, err
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	ts := r.TileSize
	if ts <= 0 {
		ts = 256
	}
	conc := r.Concurrency
	if conc <= 0 {
		conc = 4
	}

	x0f, y0f := globalPx(r.MaxLat, r.MinLon, r.Zoom, ts)
	x1f, y1f := globalPx(r.MinLat, r.MaxLon, r.Zoom, ts)
	px0, py0 := int(math.Floor(x0f)), int(math.Floor(y0f))
	px1, py1 := int(math.Ceil(x1f)), int(math.Ceil(y1f))
	if px1 <= px0 || py1 <= py0 {
		return nil, 0, fmt.Errorf("tiles: window is empty at zoom %d", r.Zoom)
	}
	w, h := px1-px0, py1-py0
	mosaic := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(mosaic, mosaic.Bounds(), image.NewUniform(color.NRGBA{A: 0xff}), image.Point{}, draw.Src)

	type job struct{ tx, ty int }
	var jobs []job
	for ty := py0 / ts; ty <= (py1-1)/ts; ty++ {
		for tx := px0 / ts; tx <= (px1-1)/ts; tx++ {
			jobs = append(jobs, job{tx, ty})
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	var fetched atomic.Int64
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			data, skip, err := fetchTile(ctx, client, userAgent, tileURL(r.URLTemplate, r.Zoom, j.tx, j.ty))
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				mu.Unlock()
				return
			}
			if skip {
				return
			}
			fetched.Add(int64(len(data)))
			img, _, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("tiles: decode tile %d,%d: %w", j.tx, j.ty, err)
					cancel()
				}
				mu.Unlock()
				return
			}
			target := image.Rect(j.tx*ts-px0, j.ty*ts-py0, j.tx*ts-px0+ts, j.ty*ts-py0+ts)
			mu.Lock()
			draw.Draw(mosaic, target, img, image.Point{}, draw.Src)
			mu.Unlock()
		}(j)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, fetched.Load(), firstErr
	}
	return mosaic, fetched.Load(), nil
}

func fetchTile(ctx context.Context, client *http.Client, userAgent, url string) (data []byte, skip bool, err error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			delay := 500 * time.Millisecond << (attempt - 1)
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case <-time.After(delay):
			}
		}
		data, skip, retryable, err := tryTile(ctx, client, userAgent, url)
		if err == nil {
			return data, skip, nil
		}
		if !retryable {
			return nil, false, err
		}
		lastErr = err
	}
	return nil, false, lastErr
}

func tryTile(ctx context.Context, client *http.Client, userAgent, url string) (data []byte, skip, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, false, err
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, false, ctx.Err()
		}
		return nil, false, true, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusNoContent:
		return nil, true, false, nil
	case resp.StatusCode >= 500:
		return nil, false, true, fmt.Errorf("tiles: HTTP %d", resp.StatusCode)
	default:
		return nil, false, false, fmt.Errorf("tiles: HTTP %d", resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxTileBytes))
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, false, ctx.Err()
		}
		return nil, false, true, err
	}
	return data, false, false, nil
}
