// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Package wms fetches server-rendered orthophoto windows from WMS GetMap
// endpoints.
package wms

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

// Request describes one GetMap fetch. Version and CRS default to 1.3.0 and
// EPSG:4326; with that pair the BBOX axis order is lat,lon.
type Request struct {
	BaseURL                        string
	Layers                         string
	Version                        string
	CRS                            string
	MinLat, MinLon, MaxLat, MaxLon float64
	Px                             int // output width and height
	MIME                           string
}

func (r Request) mapURL() string {
	version := r.Version
	if version == "" {
		version = "1.3.0"
	}
	crs := r.CRS
	if crs == "" {
		crs = "EPSG:4326"
	}
	q := url.Values{
		"SERVICE": {"WMS"},
		"VERSION": {version},
		"REQUEST": {"GetMap"},
		"LAYERS":  {r.Layers},
		"STYLES":  {""},
		"CRS":     {crs},
		"BBOX":    {fmt.Sprintf("%f,%f,%f,%f", r.MinLat, r.MinLon, r.MaxLat, r.MaxLon)},
		"WIDTH":   {strconv.Itoa(r.Px)},
		"HEIGHT":  {strconv.Itoa(r.Px)},
		"FORMAT":  {r.MIME},
	}
	return r.BaseURL + "?" + q.Encode()
}

// Fetch performs the GetMap request, streaming the image into w and
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.mapURL(), nil)
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
		return 0, true, fmt.Errorf("wms getmap: HTTP %d", resp.StatusCode)
	default:
		return 0, false, fmt.Errorf("wms getmap: HTTP %d", resp.StatusCode)
	}
	// WMS servers report failures as HTTP 200 with an XML service
	// exception; only an image content type is a success.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		snippet := make([]byte, 200)
		sn, _ := io.ReadFull(resp.Body, snippet)
		return 0, false, fmt.Errorf("wms getmap: %s response: %.200s", ct, snippet[:sn])
	}
	n, err = io.Copy(w, resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, ctx.Err()
		}
		return 0, false, fmt.Errorf("wms getmap: read body: %w", err)
	}
	return n, false, nil
}
