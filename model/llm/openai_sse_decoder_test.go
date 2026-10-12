// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/packages/ssestream"
)

func TestResponsesStreamIgnoresSSEKeepAliveAndEmptyFrames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, ": keep-alive\n\n")
		_, _ = io.WriteString(w, "event: ping\n\n")
		_, _ = io.WriteString(w, "data:\n\n")
		_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","delta":"OK"}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp","object":"response","model":"gpt-test","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
	}))
	defer server.Close()

	client := newOpenAICompatibleClient(ProviderConfig{
		Provider:  ProviderOpenAICompatible,
		APIKey:    "test-key",
		BaseURL:   server.URL,
		Model:     "gpt-test",
		APIFormat: APIFormatResponses,
	}, server.Client())
	events, err := client.Stream(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var completed bool
	for event := range events {
		if event.Type == ChatEventError {
			t.Fatalf("healthy stream failed after keep-alive: %s", event.Error)
		}
		text += event.Text
		completed = completed || event.Type == ChatEventDone
	}
	if text != "OK" || !completed {
		t.Fatalf("text=%q completed=%v", text, completed)
	}
}

func TestOpenAISSEDecoderStillRejectsMalformedNonEmptyJSON(t *testing.T) {
	stream := ssestream.NewStream[map[string]any](
		newOpenAISSEDecoder(io.NopCloser(strings.NewReader(": keep-alive\n\ndata: {\"type\":\n\n"))),
		nil,
	)
	if stream.Next() || stream.Err() == nil || !strings.Contains(stream.Err().Error(), "unexpected end of JSON input") {
		t.Fatalf("malformed non-empty JSON was not rejected: %v", stream.Err())
	}
}
