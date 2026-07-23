// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Package pcsign signs Microsoft Planetary Computer asset URLs. The blob
// containers behind the catalog expect a shared access signature, handed out
// by a keyless token endpoint and valid for about an hour.
package pcsign

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// blobHost suffixes the storage hosts whose URLs carry a signature. Catalog
// items also reference the Planetary Computer API itself (tile JSON, preview
// renderers), which must be left alone.
const blobHost = ".blob.core.windows.net"

// refreshMargin renews a token this long before it expires, so a signature
// handed out now survives the fetch it is handed to.
const refreshMargin = 5 * time.Minute

// fetchTimeout bounds a token request. Sign has no context of its own: it
// sits behind an href hook whose signature carries none.
const fetchTimeout = 15 * time.Second

// Signer hands out signed asset URLs, caching the token until it nears
// expiry. Safe for concurrent use.
type Signer struct {
	tokenURL string
	ua       string
	c        *http.Client

	mu    sync.Mutex
	token string
	exp   time.Time
}

// New creates a Signer against a token endpoint such as
// https://planetarycomputer.microsoft.com/api/sas/v1/token/naip.
func New(tokenURL, userAgent string, c *http.Client) *Signer {
	if c == nil {
		c = &http.Client{Timeout: fetchTimeout}
	}
	return &Signer{tokenURL: tokenURL, ua: userAgent, c: c}
}

// Sign appends a valid signature to a storage href. Hrefs pointing anywhere
// else come back untouched.
func (s *Signer) Sign(href string) (string, error) {
	u, err := url.Parse(href)
	if err != nil || !strings.HasSuffix(strings.ToLower(u.Host), blobHost) {
		return href, nil
	}
	tok, err := s.get()
	if err != nil {
		return "", err
	}
	sep := "?"
	if u.RawQuery != "" {
		sep = "&"
	}
	return href + sep + tok, nil
}

// get returns a cached token, fetching a fresh one when the cached one is
// missing or close to expiring.
func (s *Signer) get() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Until(s.exp) > refreshMargin {
		return s.token, nil
	}
	token, exp, err := s.fetch()
	if err != nil {
		return "", err
	}
	s.token, s.exp = token, exp
	return token, nil
}

type tokenResponse struct {
	Token  string `json:"token"`
	Expiry string `json:"msft:expiry"`
}

func (s *Signer) fetch() (string, time.Time, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.tokenURL, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	if s.ua != "" {
		req.Header.Set("User-Agent", s.ua)
	}
	resp, err := s.c.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("pcsign: token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("pcsign: token request: HTTP %d", resp.StatusCode)
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", time.Time{}, fmt.Errorf("pcsign: token response: %w", err)
	}
	if tr.Token == "" {
		return "", time.Time{}, fmt.Errorf("pcsign: token response carries no token")
	}
	// A token that parses to no expiry is still usable. Give it a short
	// life rather than caching it indefinitely, but keep it past the
	// refresh margin so it does not force a fetch on every single call.
	exp := time.Now().Add(2 * refreshMargin)
	if tr.Expiry != "" {
		if t, err := time.Parse(time.RFC3339, tr.Expiry); err == nil {
			exp = t
		}
	}
	return tr.Token, exp, nil
}
