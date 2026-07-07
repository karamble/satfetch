// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package cache

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKey(t *testing.T) {
	a := Key("scene", "image", "1,2")
	if len(a) != 64 {
		t.Fatalf("key length %d, want 64", len(a))
	}
	if a != Key("scene", "image", "1,2") {
		t.Error("key not stable")
	}
	if a == Key("scene", "image", "1,3") {
		t.Error("distinct inputs collide")
	}
}

func TestPutGet(t *testing.T) {
	c, err := New(t.TempDir(), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	key := Key("scene", "image")
	if _, ok := c.Get(key, "png"); ok {
		t.Fatal("unexpected hit on empty cache")
	}
	content := []byte("pixels")
	path, err := c.Put(key, "png", func(w io.Writer) error {
		_, err := w.Write(content)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content %q, want %q", got, content)
	}
	hit, ok := c.Get(key, "png")
	if !ok || hit != path {
		t.Errorf("Get = %q, %v; want %q, true", hit, ok, path)
	}
	if _, ok := c.Get(key, "tif"); ok {
		t.Error("hit with wrong extension")
	}
}

func TestPutFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	key := Key("broken")
	if _, err := c.Put(key, "png", func(io.Writer) error {
		return fmt.Errorf("render failed")
	}); err == nil {
		t.Fatal("expected error")
	}
	if _, ok := c.Get(key, "png"); ok {
		t.Error("failed build produced a cache entry")
	}
	var leftovers []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasPrefix(info.Name(), ".tmp-") {
			leftovers = append(leftovers, path)
		}
		return nil
	})
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestEviction(t *testing.T) {
	dir := t.TempDir()
	// Pre-populate three ~600 KiB files with staggered ages; a 1 MiB cap
	// must evict the two oldest.
	now := time.Now()
	names := []string{"old", "mid", "new"}
	for i, name := range names {
		sub := filepath.Join(dir, "aa", "bb")
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(sub, name+".png")
		if err := os.WriteFile(p, make([]byte, 600<<10), 0o600); err != nil {
			t.Fatal(err)
		}
		age := time.Duration(len(names)-i) * time.Hour
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	c, err := New(dir, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, tc := range []struct {
		name string
		want bool
	}{{"old", false}, {"mid", false}, {"new", true}} {
		_, err := os.Stat(filepath.Join(dir, "aa", "bb", tc.name+".png"))
		if got := err == nil; got != tc.want {
			t.Errorf("%s exists = %v, want %v", tc.name, got, tc.want)
		}
	}
}
