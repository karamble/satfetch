// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Package httpapi exposes a Service over HTTP: /image, /ndvi, /scenes,
// /healthz and /metrics.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/karamble/satfetch"
)

// Server routes API requests to a satfetch.Service.
type Server struct {
	svc *satfetch.Service
	log *slog.Logger
	m   *metrics
	mux *http.ServeMux
}

// New builds the API handler.
func New(svc *satfetch.Service, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{svc: svc, log: log, m: newMetrics(), mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /image", s.endpoint("image", s.handleImage))
	s.mux.HandleFunc("GET /ndvi", s.endpoint("ndvi", s.handleNDVI))
	s.mux.HandleFunc("GET /ortho", s.endpoint("ortho", s.handleOrtho))
	s.mux.HandleFunc("GET /scenes", s.endpoint("scenes", s.handleScenes))
	s.mux.HandleFunc("GET /sources", s.endpoint("sources", s.handleSources))
	s.mux.HandleFunc("GET /healthz", s.endpoint("healthz", s.handleHealthz))
	s.mux.HandleFunc("GET /metrics", s.m.serve)
	return s
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

// ServeHTTP implements http.Handler with recovery and request logging.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sw := &statusWriter{ResponseWriter: w}
	start := time.Now()
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("panic serving request", "path", r.URL.Path, "panic", p)
			if sw.status == 0 {
				writeError(sw, http.StatusInternalServerError, "internal error")
			}
		}
		s.log.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", sw.status, "ms", time.Since(start).Milliseconds())
	}()
	s.mux.ServeHTTP(sw, r)
}

// endpoint wraps a handler with the per-endpoint request counter.
func (s *Server) endpoint(name string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sw, ok := w.(*statusWriter)
		if !ok {
			sw = &statusWriter{ResponseWriter: w}
			w = sw
		}
		h(w, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		s.m.request(name, status)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// params parses query values, collecting the first error.
type params struct {
	q   url.Values
	err error
}

func (p *params) fail(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

func (p *params) float(name string, def, lo, hi float64) float64 {
	s := p.q.Get(name)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		p.fail("%s must be a number", name)
		return def
	}
	if v < lo || v > hi {
		p.fail("%s %v out of range %g..%g", name, v, lo, hi)
	}
	return v
}

func (p *params) requiredFloat(name string, lo, hi float64) float64 {
	if p.q.Get(name) == "" {
		p.fail("%s required", name)
		return 0
	}
	return p.float(name, 0, lo, hi)
}

func (p *params) integer(name string, def, lo, hi int) int {
	s := p.q.Get(name)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		p.fail("%s must be an integer", name)
		return def
	}
	if v < lo || v > hi {
		p.fail("%s %d out of range %d..%d", name, v, lo, hi)
	}
	return v
}

func (s *Server) imageRequest(r *http.Request) (satfetch.ImageRequest, error) {
	p := &params{q: r.URL.Query()}
	req := satfetch.ImageRequest{
		Lat:      p.requiredFloat("lat", -90, 90),
		Lon:      p.requiredFloat("lon", -180, 180),
		SizeKM:   p.float("size_km", 5, 0.5, 50),
		MaxCloud: p.float("max_cloud", 20, 0, 100),
		Days:     p.integer("days", 30, 1, 365),
		MaxPx:    p.integer("max_px", 0, 0, 1<<14),
		SceneID:  p.q.Get("scene_id"),
		Format:   satfetch.Format(p.q.Get("format")),
	}
	return req, p.err
}

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	req, err := s.imageRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.serveProduct(w, r, req, s.svc.Image)
}

func (s *Server) handleNDVI(w http.ResponseWriter, r *http.Request) {
	req, err := s.imageRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.serveProduct(w, r, req, s.svc.NDVI)
}

func (s *Server) serveProduct(w http.ResponseWriter, r *http.Request, req satfetch.ImageRequest,
	build func(context.Context, satfetch.ImageRequest) (*satfetch.Result, error)) {

	start := time.Now()
	res, err := build(r.Context(), req)
	if err != nil {
		s.writeProductError(w, err)
		return
	}
	s.serveResult(w, r, res, start)
}

func (s *Server) serveResult(w http.ResponseWriter, r *http.Request, res *satfetch.Result, start time.Time) {
	if res.CacheHit {
		s.m.cacheHit()
	} else {
		s.m.build(time.Since(start).Seconds(), res.BytesFetched)
	}
	f, err := os.Open(res.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cached product unavailable")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cached product unavailable")
		return
	}
	w.Header().Set("Content-Type", res.ContentType)
	if res.Scene.ID != "" {
		w.Header().Set("X-Scene-ID", res.Scene.ID)
		w.Header().Set("X-Scene-Datetime", res.Scene.Datetime.UTC().Format(time.RFC3339))
		w.Header().Set("X-Scene-Cloud-Cover", fmt.Sprintf("%.1f", res.Scene.CloudCover))
	}
	if res.Source != "" {
		w.Header().Set("X-Source", res.Source)
		if res.SourceGSD > 0 {
			w.Header().Set("X-Source-GSD", fmt.Sprintf("%g", res.SourceGSD))
		}
	}
	if res.CacheHit {
		w.Header().Set("X-Cache", "HIT")
	} else {
		w.Header().Set("X-Cache", "MISS")
	}
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func (s *Server) handleOrtho(w http.ResponseWriter, r *http.Request) {
	p := &params{q: r.URL.Query()}
	req := satfetch.OrthoRequest{
		Lat:    p.requiredFloat("lat", -90, 90),
		Lon:    p.requiredFloat("lon", -180, 180),
		SizeKM: p.float("size_km", 1, 0.1, 10),
		Px:     p.integer("px", 1024, 64, 4096),
		Source: p.q.Get("source"),
		Format: satfetch.Format(p.q.Get("format")),
	}
	if p.err != nil {
		writeError(w, http.StatusBadRequest, p.err.Error())
		return
	}
	start := time.Now()
	res, err := s.svc.Ortho(r.Context(), req)
	if err != nil {
		s.writeProductError(w, err)
		return
	}
	s.serveResult(w, r, res, start)
}

type sourceJSON struct {
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	GSD         float64 `json:"gsd"`
	Attribution string  `json:"attribution,omitempty"`
}

func (s *Server) handleSources(w http.ResponseWriter, _ *http.Request) {
	catalog := s.svc.SourceCatalog()
	out := struct {
		Sources []sourceJSON `json:"sources"`
	}{Sources: make([]sourceJSON, 0, len(catalog))}
	for _, src := range catalog {
		out.Sources = append(out.Sources, sourceJSON{
			Name: src.Name, Type: src.Type, GSD: src.GSD, Attribution: src.Attribution,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// writeProductError maps service errors to API statuses. Upstream detail
// stays in the logs; clients get the category.
func (s *Server) writeProductError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, satfetch.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, satfetch.ErrNoScene), errors.Is(err, satfetch.ErrOutsideScene):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		s.log.Error("product timeout", "err", err)
		writeError(w, http.StatusGatewayTimeout, "product build timed out")
	default:
		if errors.Is(err, satfetch.ErrUpstream) {
			s.m.stacError()
		}
		s.log.Error("product failure", "err", err)
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusBadGateway, "upstream failure")
	}
}

var sceneAssetKeys = []string{"visual", "red", "nir", "scl"}

type sceneJSON struct {
	ID         string   `json:"id"`
	Datetime   string   `json:"datetime"`
	CloudCover float64  `json:"cloud_cover"`
	Thumbnail  string   `json:"thumbnail,omitempty"`
	Assets     []string `json:"assets"`
}

func (s *Server) handleScenes(w http.ResponseWriter, r *http.Request) {
	p := &params{q: r.URL.Query()}
	req := satfetch.ScenesRequest{
		Lat:      p.requiredFloat("lat", -90, 90),
		Lon:      p.requiredFloat("lon", -180, 180),
		MaxCloud: p.float("max_cloud", 100, 0, 100),
		Days:     p.integer("days", 90, 1, 365),
		Limit:    p.integer("limit", 20, 1, 50),
	}
	if p.err != nil {
		writeError(w, http.StatusBadRequest, p.err.Error())
		return
	}
	scenes, err := s.svc.Scenes(r.Context(), req)
	if err != nil {
		s.writeProductError(w, err)
		return
	}
	out := struct {
		Scenes []sceneJSON `json:"scenes"`
	}{Scenes: make([]sceneJSON, 0, len(scenes))}
	for _, sc := range scenes {
		sj := sceneJSON{
			ID:         sc.ID,
			Datetime:   sc.Datetime.UTC().Format(time.RFC3339),
			CloudCover: sc.CloudCover,
			Thumbnail:  sc.Assets["thumbnail"],
			Assets:     make([]string, 0, len(sceneAssetKeys)),
		}
		for _, k := range sceneAssetKeys {
			if sc.Assets[k] != "" {
				sj.Assets = append(sj.Assets, k)
			}
		}
		out.Scenes = append(out.Scenes, sj)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}` + "\n"))
}
