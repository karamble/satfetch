// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package pcsign_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/karamble/satfetch/internal/pcsign"
)

// tokenServer hands out a signature valid until expiry, counting requests.
func tokenServer(t *testing.T, token string, expiry time.Time, calls *int64) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(calls, 1)
		fmt.Fprintf(w, `{"token":%q,"msft:expiry":%q}`, token, expiry.Format(time.RFC3339))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestSignAppendsToken(t *testing.T) {
	var calls int64
	ts := tokenServer(t, "sv=2024&sig=abc", time.Now().Add(time.Hour), &calls)
	s := pcsign.New(ts.URL, "test", nil)

	got, err := s.Sign("https://naipeuwest.blob.core.windows.net/naip/a.tif")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://naipeuwest.blob.core.windows.net/naip/a.tif?sv=2024&sig=abc"
	if got != want {
		t.Errorf("Sign() = %q, want %q", got, want)
	}
}

func TestSignJoinsExistingQuery(t *testing.T) {
	var calls int64
	ts := tokenServer(t, "sig=abc", time.Now().Add(time.Hour), &calls)
	s := pcsign.New(ts.URL, "test", nil)

	got, err := s.Sign("https://x.blob.core.windows.net/a.tif?v=2")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://x.blob.core.windows.net/a.tif?v=2&sig=abc"; got != want {
		t.Errorf("Sign() = %q, want %q", got, want)
	}
}

// Catalog items also reference the API itself; only storage hrefs are signed.
func TestSignLeavesNonStorageHrefsAlone(t *testing.T) {
	var calls int64
	ts := tokenServer(t, "sig=abc", time.Now().Add(time.Hour), &calls)
	s := pcsign.New(ts.URL, "test", nil)

	for _, href := range []string{
		"https://planetarycomputer.microsoft.com/api/data/v1/item/preview.png?x=1",
		"s3://naip-analytic/ca/a.tif",
		"://not a url",
	} {
		got, err := s.Sign(href)
		if err != nil {
			t.Fatalf("Sign(%q): %v", href, err)
		}
		if got != href {
			t.Errorf("Sign(%q) = %q, want it unchanged", href, got)
		}
	}
	if calls != 0 {
		t.Errorf("fetched %d tokens for unsigned hrefs, want 0", calls)
	}
}

func TestSignCachesToken(t *testing.T) {
	var calls int64
	ts := tokenServer(t, "sig=abc", time.Now().Add(time.Hour), &calls)
	s := pcsign.New(ts.URL, "test", nil)

	for i := 0; i < 5; i++ {
		if _, err := s.Sign("https://x.blob.core.windows.net/a.tif"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("fetched %d tokens, want 1", calls)
	}
}

// A token inside the refresh margin is renewed rather than handed out to a
// fetch that may outlive it.
func TestSignRefreshesNearExpiry(t *testing.T) {
	var calls int64
	ts := tokenServer(t, "sig=abc", time.Now().Add(time.Minute), &calls)
	s := pcsign.New(ts.URL, "test", nil)

	for i := 0; i < 3; i++ {
		if _, err := s.Sign("https://x.blob.core.windows.net/a.tif"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Errorf("fetched %d tokens, want 3", calls)
	}
}

func TestSignTokenEndpointFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	s := pcsign.New(ts.URL, "test", nil)

	if _, err := s.Sign("https://x.blob.core.windows.net/a.tif"); err == nil {
		t.Fatal("expected an error when the token endpoint fails")
	} else if !strings.Contains(err.Error(), "503") {
		t.Errorf("error %v does not mention the upstream status", err)
	}
}

func TestSignEmptyToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"token":""}`))
	}))
	defer ts.Close()
	s := pcsign.New(ts.URL, "test", nil)

	if _, err := s.Sign("https://x.blob.core.windows.net/a.tif"); err == nil {
		t.Fatal("expected an error for a token-less response")
	}
}
