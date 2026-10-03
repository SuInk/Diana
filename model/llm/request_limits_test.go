package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestProviderRequestLimitsShareConcurrencyUntilBodyClosed(t *testing.T) {
	entered := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	cfg := ProviderConfig{RequestLimitID: t.Name(), MaxConcurrency: 1}
	first := httpClientWithConfigCredentials(server.Client(), nil, cfg)
	second := httpClientWithConfigCredentials(server.Client(), nil, cfg)
	resp, err := first.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	done := make(chan error, 1)
	go func() {
		res, err := second.Do(req)
		if res != nil {
			res.Body.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
		t.Fatal("second client bypassed held streaming body")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued request did not cancel")
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := second.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	next.Body.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("closing stream did not release slot")
	}
}

func TestProviderRequestLimitsPaceIndependentClientsAndReleaseEOF(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		io.WriteString(w, "ok")
	}))
	defer server.Close()
	cfg := ProviderConfig{RequestLimitID: t.Name(), MaxConcurrency: 1, MaxRPS: 25}
	for i := 0; i < 3; i++ {
		client := httpClientWithConfigCredentials(server.Client(), nil, cfg)
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
		// EOF already frees the slot; closing twice must not corrupt the count.
		resp.Body.Close()
		resp.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 30*time.Millisecond {
			t.Fatalf("RPS gap=%v", gap)
		}
	}
}

func TestProviderRequestLimitsReconfigureWakesWaiters(t *testing.T) {
	limit := &providerRequestLimit{}
	limit.configure(1, 0)
	release, err := limit.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	acquired := make(chan func(), 1)
	go func() {
		next, err := limit.acquire(context.Background())
		if err == nil {
			acquired <- next
		}
	}()
	limit.configure(2, 0)
	select {
	case next := <-acquired:
		next()
	case <-time.After(time.Second):
		t.Fatal("updated limit did not wake waiter")
	}
}

func TestProviderRequestLimitValidation(t *testing.T) {
	for _, cfg := range []ProviderConfig{{MaxConcurrency: -1}, {MaxConcurrency: 10001}, {MaxRPS: -1}, {MaxRPS: 0.0001}} {
		cfg.Provider = ProviderOpenAICompatible
		cfg.APIKey = "test"
		cfg.Model = "test"
		if cfg.Validate() == nil {
			t.Fatalf("accepted invalid limit: %+v", cfg)
		}
	}
}

func TestProviderRequestLimitsReleaseCanceledUnreadStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := httpClientWithRequestLimits(server.Client(), ProviderConfig{RequestLimitID: t.Name(), MaxConcurrency: 1})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	response, err := client.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer response.Body.Close()
	cancel()
	// The caller has not read or closed the first response yet. Cancellation
	// must still release its slot rather than starving the next request.
	nextCtx, nextCancel := context.WithTimeout(context.Background(), time.Second)
	defer nextCancel()
	nextReq, _ := http.NewRequestWithContext(nextCtx, http.MethodGet, server.URL, nil)
	next, err := client.Do(nextReq)
	if err != nil {
		t.Fatalf("canceled stream held slot: %v", err)
	}
	next.Body.Close()
}
