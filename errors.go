// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package satfetch

import "errors"

var (
	// ErrInvalid marks request validation failures.
	ErrInvalid = errors.New("invalid request")
	// ErrNoScene means no scene matched the search constraints.
	ErrNoScene = errors.New("no matching scene")
	// ErrUpstream marks catalog or pixel-data fetch failures.
	ErrUpstream = errors.New("upstream failure")
	// ErrOutsideScene means the requested window misses the scene's raster.
	ErrOutsideScene = errors.New("window outside scene coverage")
)
