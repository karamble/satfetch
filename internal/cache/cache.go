// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Package cache is a disk cache of finished products, content-addressed by
// request key, evicted least-recently-used by file mtime to a size cap.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const evictInterval = 10 * time.Minute

// Key derives the cache key for a set of request fields.
func Key(fields ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(fields, "|")))
	return hex.EncodeToString(sum[:])
}

type Cache struct {
	dir      string
	maxBytes int64
	log      *slog.Logger

	mu     sync.Mutex
	closed bool
	done   chan struct{}
}

// New opens (creating if needed) a cache directory and starts the eviction
// loop. maxMB <= 0 disables the size cap.
func New(dir string, maxMB int, log *slog.Logger) (*Cache, error) {
	if dir == "" {
		return nil, fmt.Errorf("cache: directory required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	c := &Cache{dir: dir, maxBytes: int64(maxMB) << 20, log: log, done: make(chan struct{})}
	c.evict()
	go c.loop()
	return c, nil
}

// Close stops the eviction loop.
func (c *Cache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.done)
	}
}

func (c *Cache) loop() {
	t := time.NewTicker(evictInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			c.evict()
		}
	}
}

func (c *Cache) path(key, ext string) string {
	return filepath.Join(c.dir, key[:2], key[2:4], key+"."+ext)
}

// Get returns the path of a cached product and whether it exists. A hit
// refreshes the file's mtime so eviction is least-recently-used.
func (c *Cache) Get(key, ext string) (string, bool) {
	p := c.path(key, ext)
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return p, true
}

// Put builds a product into the cache atomically: write writes the content
// to a temp file which is renamed into place on success.
func (c *Cache) Put(key, ext string, write func(io.Writer) error) (string, error) {
	p := c.path(key, ext)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := write(tmp); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return "", err
	}
	return p, nil
}

type entry struct {
	path  string
	size  int64
	mtime time.Time
}

// evict deletes the oldest files until the cache fits the size cap.
func (c *Cache) evict() {
	if c.maxBytes <= 0 {
		return
	}
	var entries []entry
	var total int64
	filepath.WalkDir(c.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		entries = append(entries, entry{path, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if total <= c.maxBytes {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mtime.Before(entries[j].mtime) })
	var evicted int
	for _, e := range entries {
		if total <= c.maxBytes {
			break
		}
		if err := os.Remove(e.path); err == nil {
			total -= e.size
			evicted++
		}
	}
	if evicted > 0 {
		c.log.Info("cache evicted", "files", evicted, "bytes", total)
	}
}
