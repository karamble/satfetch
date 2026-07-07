// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package satfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultSTACURL is the Element84 Earth Search API root: free, keyless,
// best-effort.
const DefaultSTACURL = "https://earth-search.aws.element84.com/v1"

// collection is the complete Sentinel-2 L2A COG collection (the newer
// sentinel-2-c1-l2a collection has historical gaps).
const collection = "sentinel-2-l2a"

const defaultUserAgent = "satfetch/0.1 (+https://github.com/karamble/satfetch)"

// EarthSearchOptions configures the catalog client. Zero values take
// defaults.
type EarthSearchOptions struct {
	BaseURL    string
	UserAgent  string
	HTTPClient *http.Client
	// HrefHook post-processes asset hrefs, the seam for catalogs that
	// require URL signing. Identity when nil.
	HrefHook func(string) (string, error)
}

// EarthSearch implements Catalog against a STAC Item Search endpoint.
type EarthSearch struct {
	base string
	ua   string
	c    *http.Client
	hook func(string) (string, error)
}

func NewEarthSearch(o EarthSearchOptions) *EarthSearch {
	if o.BaseURL == "" {
		o.BaseURL = DefaultSTACURL
	}
	if o.UserAgent == "" {
		o.UserAgent = defaultUserAgent
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &EarthSearch{
		base: strings.TrimRight(o.BaseURL, "/"),
		ua:   o.UserAgent,
		c:    o.HTTPClient,
		hook: o.HrefHook,
	}
}

type stacSearch struct {
	Collections []string       `json:"collections"`
	Intersects  *geoPoint      `json:"intersects,omitempty"`
	Datetime    string         `json:"datetime,omitempty"`
	Query       map[string]any `json:"query,omitempty"`
	IDs         []string       `json:"ids,omitempty"`
	Limit       int            `json:"limit"`
}

type geoPoint struct {
	Type        string     `json:"type"`
	Coordinates [2]float64 `json:"coordinates"` // lon, lat
}

type stacItem struct {
	ID         string         `json:"id"`
	Properties map[string]any `json:"properties"`
	Assets     map[string]struct {
		Href string `json:"href"`
	} `json:"assets"`
}

type stacResponse struct {
	Features []stacItem `json:"features"`
}

// Search implements Catalog.
func (e *EarthSearch) Search(ctx context.Context, q Query) ([]Scene, error) {
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -q.Days)
	limit := q.Limit
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	body := stacSearch{
		Collections: []string{collection},
		Intersects:  &geoPoint{Type: "Point", Coordinates: [2]float64{q.Lon, q.Lat}},
		Datetime:    start.Format(time.RFC3339) + "/" + end.Format(time.RFC3339),
		Query:       map[string]any{"eo:cloud_cover": map[string]float64{"lt": q.MaxCloud}},
		Limit:       limit,
	}
	scenes, err := e.search(ctx, body)
	if err != nil {
		return nil, err
	}
	sort.Slice(scenes, func(i, j int) bool { return scenes[i].Datetime.After(scenes[j].Datetime) })
	return scenes, nil
}

// Get implements Catalog.
func (e *EarthSearch) Get(ctx context.Context, id string) (Scene, error) {
	scenes, err := e.search(ctx, stacSearch{
		Collections: []string{collection},
		IDs:         []string{id},
		Limit:       1,
	})
	if err != nil {
		return Scene{}, err
	}
	if len(scenes) == 0 {
		return Scene{}, fmt.Errorf("%w: scene %s not found", ErrNoScene, id)
	}
	return scenes[0], nil
}

func (e *EarthSearch) search(ctx context.Context, body stacSearch) ([]Scene, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			delay := 500 * time.Millisecond << (attempt - 1)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		scenes, retryable, err := e.trySearch(ctx, payload)
		if err == nil {
			return scenes, nil
		}
		if !retryable {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (e *EarthSearch) trySearch(ctx context.Context, payload []byte) (scenes []Scene, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+"/search", bytes.NewReader(payload))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", e.ua)
	resp, err := e.c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, true, fmt.Errorf("%w: stac search: %v", ErrUpstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("%w: stac search: HTTP %d", ErrUpstream, resp.StatusCode)
		return nil, resp.StatusCode >= 500, err
	}
	var sr stacResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, false, fmt.Errorf("%w: stac search: %v", ErrUpstream, err)
	}
	scenes = make([]Scene, 0, len(sr.Features))
	for _, it := range sr.Features {
		sc, err := e.toScene(it)
		if err != nil {
			return nil, false, err
		}
		scenes = append(scenes, sc)
	}
	return scenes, false, nil
}

func (e *EarthSearch) toScene(it stacItem) (Scene, error) {
	sc := Scene{ID: it.ID, Assets: make(map[string]string, len(it.Assets))}
	if s, ok := it.Properties["datetime"].(string); ok {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t, err = time.Parse(time.RFC3339, s)
		}
		if err == nil {
			sc.Datetime = t
		}
	}
	if v, ok := it.Properties["eo:cloud_cover"].(float64); ok {
		sc.CloudCover = v
	}
	if v, ok := it.Properties["proj:epsg"].(float64); ok {
		sc.EPSG = int(v)
	} else if s, ok := it.Properties["proj:code"].(string); ok {
		if n, err := strconv.Atoi(strings.TrimPrefix(s, "EPSG:")); err == nil {
			sc.EPSG = n
		}
	}
	for key, a := range it.Assets {
		href := a.Href
		if e.hook != nil {
			var err error
			href, err = e.hook(href)
			if err != nil {
				return Scene{}, fmt.Errorf("%w: href hook: %v", ErrUpstream, err)
			}
		}
		sc.Assets[key] = href
	}
	return sc, nil
}
