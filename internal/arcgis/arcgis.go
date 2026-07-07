// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Package arcgis fetches server-rendered orthophoto windows from ArcGIS
// MapServer export endpoints.
package arcgis

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Request describes one export fetch. The bbox is WGS84; ArcGIS expects
// lon,lat axis order with bboxSR 4326.
type Request struct {
	BaseURL                        string // service URL ending in /MapServer
	MinLat, MinLon, MaxLat, MaxLon float64
	Px                             int    // output width and height
	MIME                           string // image/jpeg or image/png
}

func (r Request) exportURL() string {
	format := "png"
	if r.MIME == "image/jpeg" {
		format = "jpg"
	}
	q := url.Values{
		"bbox":        {fmt.Sprintf("%f,%f,%f,%f", r.MinLon, r.MinLat, r.MaxLon, r.MaxLat)},
		"bboxSR":      {"4326"},
		"size":        {strconv.Itoa(r.Px) + "," + strconv.Itoa(r.Px)},
		"format":      {format},
		"transparent": {"false"},
		"f":           {"image"},
	}
	return strings.TrimRight(r.BaseURL, "/") + "/export?" + q.Encode()
}

// Fetch performs the export request, streaming the image into w and
// returning the bytes copied. Transient failures retry with backoff.
func Fetch(ctx context.Context, client *http.Client, userAgent string, r Request, w io.Writer) (int64, error) {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			delay := 500 * time.Millisecond << (attempt - 1)
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(delay):
			}
		}
		n, retryable, err := tryFetch(ctx, client, userAgent, r, w)
		if err == nil {
			return n, nil
		}
		if !retryable {
			return 0, err
		}
		lastErr = err
	}
	return 0, lastErr
}

func tryFetch(ctx context.Context, client *http.Client, userAgent string, r Request, w io.Writer) (n int64, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.exportURL(), nil)
	if err != nil {
		return 0, false, err
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, ctx.Err()
		}
		return 0, true, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode >= 500:
		return 0, true, fmt.Errorf("arcgis export: HTTP %d", resp.StatusCode)
	default:
		return 0, false, fmt.Errorf("arcgis export: HTTP %d", resp.StatusCode)
	}
	// ArcGIS reports failures as HTTP 200 with a JSON or HTML body; only
	// an image content type is a success.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		snippet := make([]byte, 200)
		sn, _ := io.ReadFull(resp.Body, snippet)
		return 0, false, fmt.Errorf("arcgis export: %s response: %.200s", ct, snippet[:sn])
	}
	n, err = io.Copy(w, resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, ctx.Err()
		}
		return 0, false, fmt.Errorf("arcgis export: read body: %w", err)
	}
	return n, false, nil
}
