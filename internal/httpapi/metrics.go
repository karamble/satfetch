// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

type metrics struct {
	mu         sync.Mutex
	requests   map[string]uint64 // "endpoint|status"
	cacheHits  uint64
	buildSum   float64
	buildCount uint64
	stacErrors uint64
	cogBytes   int64
}

func newMetrics() *metrics {
	return &metrics{requests: make(map[string]uint64)}
}

func (m *metrics) request(endpoint string, status int) {
	m.mu.Lock()
	m.requests[fmt.Sprintf("%s|%d", endpoint, status)]++
	m.mu.Unlock()
}

func (m *metrics) cacheHit() {
	m.mu.Lock()
	m.cacheHits++
	m.mu.Unlock()
}

func (m *metrics) build(seconds float64, fetchedBytes int64) {
	m.mu.Lock()
	m.buildSum += seconds
	m.buildCount++
	m.cogBytes += fetchedBytes
	m.mu.Unlock()
}

func (m *metrics) stacError() {
	m.mu.Lock()
	m.stacErrors++
	m.mu.Unlock()
}

func (m *metrics) serve(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	keys := make([]string, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		endpoint, status, _ := strings.Cut(k, "|")
		fmt.Fprintf(w, "satfetch_requests_total{endpoint=%q,status=%q} %d\n",
			endpoint, status, m.requests[k])
	}
	fmt.Fprintf(w, "satfetch_cache_hits_total %d\n", m.cacheHits)
	fmt.Fprintf(w, "satfetch_build_seconds_sum %g\n", m.buildSum)
	fmt.Fprintf(w, "satfetch_build_seconds_count %d\n", m.buildCount)
	fmt.Fprintf(w, "satfetch_stac_errors_total %d\n", m.stacErrors)
	fmt.Fprintf(w, "satfetch_cog_fetch_bytes_total %d\n", m.cogBytes)
}
