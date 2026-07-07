// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Command satfetch serves Sentinel-2 imagery products over HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/karamble/satfetch"
	"github.com/karamble/satfetch/internal/httpapi"
)

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envIntOr(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDurOr(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func main() {
	listen := flag.String("listen", envOr("SATFETCH_LISTEN", ":8080"), "listen address")
	cacheDir := flag.String("cache-dir", envOr("SATFETCH_CACHE_DIR", "./cache"), "product cache directory")
	cacheMaxMB := flag.Int("cache-max-mb", envIntOr("SATFETCH_CACHE_MAX_MB", 2048), "cache size cap in MiB")
	stacURL := flag.String("stac-url", envOr("SATFETCH_STAC_URL", satfetch.DefaultSTACURL), "STAC API root")
	buildTimeout := flag.Duration("build-timeout", envDurOr("SATFETCH_BUILD_TIMEOUT", 60*time.Second), "per-product build timeout")
	maxBuilds := flag.Int("max-concurrent-builds", envIntOr("SATFETCH_MAX_CONCURRENT_BUILDS", 4), "concurrent product builds")
	logFormat := flag.String("log-format", envOr("SATFETCH_LOG_FORMAT", "text"), "log format: text or json")
	flag.Parse()

	var handler slog.Handler
	switch *logFormat {
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, nil)
	case "text":
		handler = slog.NewTextHandler(os.Stderr, nil)
	default:
		fmt.Fprintf(os.Stderr, "unknown log format %q\n", *logFormat)
		os.Exit(1)
	}
	log := slog.New(handler)

	svc, err := satfetch.New(satfetch.Options{
		Catalog:             satfetch.NewEarthSearch(satfetch.EarthSearchOptions{BaseURL: *stacURL}),
		CacheDir:            *cacheDir,
		CacheMaxMB:          *cacheMaxMB,
		BuildTimeout:        *buildTimeout,
		MaxConcurrentBuilds: *maxBuilds,
		Logger:              log,
	})
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	defer svc.Close()

	srv := &http.Server{
		Addr:         *listen,
		Handler:      httpapi.New(svc, log),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Info("satfetch listening", "addr", *listen, "stac", *stacURL, "cache", *cacheDir)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error("shutdown", "err", err)
		}
	}
}
