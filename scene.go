// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package satfetch

import (
	"context"
	"time"
)

// Scene is one catalog item: a satellite acquisition with its assets.
type Scene struct {
	ID         string
	Datetime   time.Time
	CloudCover float64
	EPSG       int
	Assets     map[string]string // asset key -> https href
}

// Query selects scenes covering a point.
type Query struct {
	Lon, Lat float64
	MaxCloud float64
	Days     int
	Limit    int
}

// Catalog finds scenes. Implementations must return scenes newest first.
type Catalog interface {
	Search(ctx context.Context, q Query) ([]Scene, error)
	Get(ctx context.Context, id string) (Scene, error)
}
