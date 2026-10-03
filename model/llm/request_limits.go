// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// A provider's clients share this state across robots, models and SDK retries.
// Rate pacing permits no initial burst. Concurrency covers the response body,
// including streaming, rather than ending when the HTTP headers arrive.
var providerRequestLimits sync.Map

type providerRequestLimit struct {
	mu             sync.Mutex
	maxConcurrency int
	maxRPS         float64
	active         int
	lastStart      time.Time
	changed        chan struct{}
}

func (l *providerRequestLimit) signal() { close(l.changed); l.changed = make(chan struct{}) }

func (l *providerRequestLimit) configure(concurrency int, rps float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.changed == nil {
		l.changed = make(chan struct{})
	}
	if l.maxConcurrency != concurrency || l.maxRPS != rps {
		l.maxConcurrency, l.maxRPS = concurrency, rps
		l.signal()
	}
}

func (l *providerRequestLimit) acquire(ctx context.Context) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		l.mu.Lock()
		available := l.maxConcurrency <= 0 || l.active < l.maxConcurrency
		delay := time.Duration(0)
		if l.maxRPS > 0 {
			delay = time.Until(l.lastStart.Add(time.Duration(float64(time.Second) / l.maxRPS)))
		}
		if available && delay <= 0 {
			l.active++
			l.lastStart = time.Now()
			l.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { l.mu.Lock(); l.active--; l.signal(); l.mu.Unlock() }) }, nil
		}
		changed := l.changed
		l.mu.Unlock()
		var timer *time.Timer
		var tick <-chan time.Time
		if available && delay > 0 {
			timer = time.NewTimer(delay)
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return nil, ctx.Err()
		case <-changed:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

type requestLimitTransport struct {
	base  http.RoundTripper
	limit *providerRequestLimit
}

func (t *requestLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	release, err := t.limit.acquire(req.Context())
	if err != nil {
		return nil, err
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		release()
		return resp, err
	}
	stop := context.AfterFunc(req.Context(), release)
	resp.Body = &requestLimitBody{ReadCloser: resp.Body, release: func() { stop(); release() }}
	return resp, nil
}

type requestLimitBody struct {
	io.ReadCloser
	release func()
}

func (b *requestLimitBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.release()
	}
	return n, err
}
func (b *requestLimitBody) Close() error { defer b.release(); return b.ReadCloser.Close() }

func httpClientWithRequestLimits(client *http.Client, cfg ProviderConfig) *http.Client {
	key := cfg.RequestLimitID
	if key == "" {
		// Direct API users have no profile ID. Share clients for the same credentials
		// and endpoint without retaining the secret in the scheduler's map key.
		key = fmt.Sprintf("direct:%x", sha256.Sum256([]byte(string(cfg.Provider)+"\x00"+cfg.BaseURL+"\x00"+cfg.APIKey+"\x00"+cfg.OAuthProvider)))
	}
	stored, found := providerRequestLimits.Load(key)
	if cfg.MaxConcurrency == 0 && cfg.MaxRPS == 0 && !found {
		return client
	}
	if !found {
		stored, _ = providerRequestLimits.LoadOrStore(key, &providerRequestLimit{})
	}
	limit := stored.(*providerRequestLimit)
	limit.configure(cfg.MaxConcurrency, cfg.MaxRPS)
	if client == nil {
		client = http.DefaultClient
	}
	wrapped := *client
	wrapped.Transport = &requestLimitTransport{base: client.Transport, limit: limit}
	return &wrapped
}
