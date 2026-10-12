// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLiveOpenAIResponsesKeepAlive is opt-in because it calls a real provider.
// Use high reasoning with a small visible answer: gateways normally emit
// keep-alives only when a model leaves the stream idle long enough.
func TestLiveOpenAIResponsesKeepAlive(t *testing.T) {
	if os.Getenv("DIANA_LIVE_SSE_KEEPALIVE") != "1" {
		t.Skip("set DIANA_LIVE_SSE_KEEPALIVE=1 with DIANA_TEST_LLM_CONFIG_JSON")
	}
	var cfg ProviderConfig
	if err := json.Unmarshal([]byte(os.Getenv("DIANA_TEST_LLM_CONFIG_JSON")), &cfg); err != nil {
		t.Fatal("invalid live provider config JSON")
	}
	cfg.Timeout = 90 * time.Second
	recorder := &liveSSERecorder{}
	client, err := NewClient(cfg, WithHTTPClient(&http.Client{Transport: liveSSERecordingTransport{
		base:     http.DefaultTransport,
		recorder: recorder,
	}}))
	if err != nil {
		t.Fatal(err)
	}
	streamer, ok := client.(interface {
		Stream(context.Context, GenerateRequest) (<-chan ChatEvent, error)
	})
	if !ok {
		t.Fatal("live client does not support streaming")
	}
	for attempt := 1; attempt <= 12; attempt++ {
		events, err := streamer.Stream(context.Background(), GenerateRequest{
			Messages:        []Message{{Role: RoleUser, Content: "Think carefully, then reply with only the integer: 1234567 * 7654321."}},
			ReasoningEffort: "high",
			MaxOutputTokens: 256,
		})
		if err != nil {
			t.Fatalf("attempt %d: open stream: %v", attempt, err)
		}
		var text string
		completed := false
		for event := range events {
			if event.Type == ChatEventError {
				t.Fatalf("attempt %d: %s", attempt, event.Error)
			}
			text += event.Text
			completed = completed || event.Type == ChatEventDone
		}
		if !completed || strings.TrimSpace(text) == "" {
			t.Fatalf("attempt %d: text=%q completed=%v", attempt, text, completed)
		}
		if recorder.hasKeepAlive() {
			t.Logf("handled a real SSE keep-alive on attempt %d", attempt)
			return
		}
	}
	t.Log("all live streams completed; none was slow enough to emit a keep-alive")
}

type liveSSERecorder struct {
	mu   sync.Mutex
	data strings.Builder
}

func (r *liveSSERecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data.Write(data)
}

func (r *liveSSERecorder) hasKeepAlive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Contains(r.data.String(), ": keep-alive")
}

type liveSSERecordingTransport struct {
	base     http.RoundTripper
	recorder io.Writer
}

func (t liveSSERecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	response.Body = struct {
		io.Reader
		io.Closer
	}{Reader: io.TeeReader(response.Body, t.recorder), Closer: response.Body}
	return response, nil
}
