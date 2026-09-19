// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

func continuationScope(cfg ProviderConfig, model string) string {
	if strings.TrimSpace(model) == "" {
		model = cfg.Model
	}
	// Hash the endpoint as it may contain credentials. Never serialize this scope.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s", cfg.Provider, cfg.BaseURL, cfg.APIFormat, model))))
}

func scopedContinuationMessages(messages []Message, scope string) []Message {
	out := append([]Message(nil), messages...)
	for i := range out {
		msg := &out[i]
		// Untagged messages are supported for callers constructing native history.
		if msg.ContinuationScope == "" || msg.ContinuationScope == scope {
			continue
		}
		msg.AnthropicThinking = nil
		msg.ReasoningContent = nil
		msg.ResponsesOutput = nil
		msg.ToolCalls = append([]ToolCall(nil), msg.ToolCalls...)
		for j := range msg.ToolCalls {
			msg.ToolCalls[j].ThoughtSignature = nil
		}
	}
	return out
}

func scopeContinuationEvents(ctx context.Context, events <-chan ChatEvent, scope string) <-chan ChatEvent {
	out := make(chan ChatEvent, 8)
	go func() {
		defer close(out)
		defer recoverChatStreamPanic(ctx, out, "continuation scope")
		for event := range events {
			if event.Response != nil {
				response := *event.Response
				response.ContinuationScope = scope
				event.Response = &response
			}
			// Drain the producer on cancellation, just like the visible-text filter.
			sendChatEvent(ctx, out, event)
		}
	}()
	return out
}
