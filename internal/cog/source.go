// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package cog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// Source supplies byte ranges of a TIFF. ReadRange returns the number of
// bytes read into p; n < len(p) only when the range extends past the end
// of the source.
type Source interface {
	ReadRange(ctx context.Context, off int64, p []byte) (int, error)
}

// HTTPSource reads ranges of a remote COG with HTTP Range requests.
type HTTPSource struct {
	url       string
	client    *http.Client
	userAgent string
	fetched   atomic.Int64
}

func NewHTTPSource(url string, client *http.Client, userAgent string) *HTTPSource {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	return &HTTPSource{url: url, client: client, userAgent: userAgent}
}

// BytesFetched reports the total body bytes read so far.
func (s *HTTPSource) BytesFetched() int64 { return s.fetched.Load() }

func (s *HTTPSource) ReadRange(ctx context.Context, off int64, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			delay := 500 * time.Millisecond << (attempt - 1)
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(delay):
			}
		}
		n, retryable, err := s.tryRange(ctx, off, p)
		if err == nil {
			s.fetched.Add(int64(n))
			return n, nil
		}
		if !retryable {
			return 0, err
		}
		lastErr = err
	}
	return 0, lastErr
}

func (s *HTTPSource) tryRange(ctx context.Context, off int64, p []byte) (n int, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(len(p))-1))
	if s.userAgent != "" {
		req.Header.Set("User-Agent", s.userAgent)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, ctx.Err()
		}
		return 0, true, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPartialContent:
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return 0, false, io.EOF
	case resp.StatusCode == http.StatusOK && off == 0:
		// The server ignored the Range header; reading only len(p)
		// bytes of the body still yields the requested prefix.
	case resp.StatusCode >= 500:
		return 0, true, fmt.Errorf("range request: HTTP %d", resp.StatusCode)
	default:
		return 0, false, fmt.Errorf("range request: HTTP %d", resp.StatusCode)
	}
	n, err = io.ReadFull(resp.Body, p)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return n, false, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, ctx.Err()
		}
		return 0, true, err
	}
	return n, false, nil
}

// BytesSource serves ranges from an in-memory buffer.
type BytesSource struct{ Data []byte }

func (s *BytesSource) ReadRange(_ context.Context, off int64, p []byte) (int, error) {
	if off >= int64(len(s.Data)) {
		return 0, io.EOF
	}
	return copy(p, s.Data[off:]), nil
}
