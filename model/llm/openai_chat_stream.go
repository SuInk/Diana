// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

func sendChatEvent(ctx context.Context, out chan<- ChatEvent, event ChatEvent) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *openAICompatibleClient) streamChatCompletion(ctx context.Context, req GenerateRequest) (<-chan ChatEvent, error) {
	req = applyContextBudget(req.withDefaults(c.cfg), c.cfg)
	if err := validateGenerateRequest(req); err != nil {
		return nil, fmt.Errorf("llm: local request validation failed: %w", err)
	}
	if c.strictUnsupported.Load() {
		req = withoutStrictTools(req)
	}
	// Strict-schema rejection happens before any SSE output. Retry the same
	// streaming request without strict schemas, never switch tool calls to JSON.
	for attempt := 0; attempt < 2; attempt++ {
		params := openAIChatCompletionRequest{Model: req.Model, Messages: openAIChatCompletionMessages(req.Messages, req.Tools), Temperature: req.Temperature, ReasoningEffort: req.ReasoningEffort, MaxTokens: req.MaxOutputTokens, Stream: true, Tools: openAIChatTools(req.Tools), ToolChoice: openAIChatToolChoice(req)}
		params.StreamOptions = map[string]bool{"include_usage": true}
		if len(req.Tools) > 0 {
			parallel := false
			params.ParallelToolCalls = &parallel
		}
		body, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		httpReq, err := c.newOpenAIRequest(ctx, "chat/completions", body)
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		resp, cancel, err := c.doChatCompletionRequest(ctx, httpReq)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			var data []byte
			readErr := readOpenAIResponseBodyWithIdleTimeout(ctx, resp.Body, c.cfg.Timeout, "response body", func(r io.Reader) error { var e error; data, e = io.ReadAll(io.LimitReader(r, 1<<20)); return e })
			resp.Body.Close()
			cancel()
			if readErr != nil {
				return nil, readErr
			}
			err = openAICompatibleError(fmt.Errorf("stream request failed"), &openAIErrorCapture{statusCode: resp.StatusCode, body: string(data)})
			if attempt == 0 && requestHasStrictTools(req) && strictToolsRejected(err) {
				c.strictUnsupported.Store(true)
				req = withoutStrictTools(req)
				continue
			}
			return nil, err
		}
		if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
			resp.Body.Close()
			cancel()
			return nil, fmt.Errorf("llm: streaming endpoint did not return text/event-stream")
		}
		out := make(chan ChatEvent, 8)
		go func() {
			defer close(out)
			defer recoverChatStreamPanic(ctx, out, "openai-compatible chat completions")
			defer cancel()
			defer resp.Body.Close()
			parseCtx, cancelParse := context.WithCancel(ctx)
			defer cancelParse()
			readDone := make(chan struct{})
			err := readOpenAIResponseBodyWithIdleTimeout(ctx, resp.Body, c.cfg.Timeout, "stream", func(r io.Reader) error {
				defer close(readDone)
				return decodeChatCompletionEvents(parseCtx, r, req, func(event ChatEvent) bool { return sendChatEvent(parseCtx, out, event) })
			})
			// The idle reader can return while its scanner is unwinding. Stop event
			// writes and join it before closing out (including timeout/cancel paths).
			cancelParse()
			<-readDone
			if err != nil {
				sendChatEvent(ctx, out, ChatEvent{Type: ChatEventError, Error: err.Error(), ErrorCause: err})
			}
		}()
		return out, nil
	}
	return nil, fmt.Errorf("llm: streaming request failed")
}

type chatStreamTool struct {
	id, name  string
	arguments strings.Builder
}

func decodeChatCompletionEvents(ctx context.Context, reader io.Reader, req GenerateRequest, emit func(ChatEvent) bool) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	pending := map[int]*chatStreamTool{}
	var reasoning strings.Builder
	reasoningPresent := false
	model := req.Model
	var usage Usage
	finished, done, visible := false, false, false
	var dataLines []string
	process := func(data string) error {
		if data == "[DONE]" {
			done = true
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return fmt.Errorf("llm: malformed chat stream JSON: %w", err)
		}
		if message := streamErrorMessage(payload); message != "" {
			return fmt.Errorf("llm: %s", message)
		}
		if v := stringField(payload, "model"); v != "" {
			model = v
		}
		// Usage-only chunks have choices:[] and normally follow finish_reason.
		if v, ok := payload["usage"].(map[string]any); ok {
			usage = usageFromPayload(v)
		}
		choices, _ := payload["choices"].([]any)
		for _, raw := range choices {
			choice, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("llm: invalid chat stream choice")
			}
			if index, ok := choice["index"].(float64); ok && index != 0 {
				continue
			}
			delta, _ := choice["delta"].(map[string]any)
			if text := openAIContentText(delta["content"]); text != "" {
				if finished {
					return fmt.Errorf("llm: chat content after finish_reason")
				}
				visible = visible || strings.TrimSpace(text) != ""
				if !emit(ChatEvent{Type: ChatEventTextDelta, Text: text}) {
					return ctx.Err()
				}
			}
			for _, key := range []string{"reasoning_content", "reasoning", "thinking"} {
				if value, ok := delta[key].(string); ok {
					reasoningPresent = true
					reasoning.WriteString(value)
					if !emit(ChatEvent{Type: ChatEventReasoning, Reasoning: value}) {
						return ctx.Err()
					}
					break // Gateways sometimes duplicate the same reasoning in two fields.
				}
			}
			calls, _ := delta["tool_calls"].([]any)
			for _, rawCall := range calls {
				if finished {
					return fmt.Errorf("llm: tool arguments after finish_reason")
				}
				call, ok := rawCall.(map[string]any)
				if !ok {
					return fmt.Errorf("llm: invalid streaming tool call")
				}
				value, ok := call["index"].(float64)
				if !ok || value < 0 || value != float64(int(value)) {
					return fmt.Errorf("llm: streaming tool call has invalid index")
				}
				index := int(value)
				tool := pending[index]
				if tool == nil {
					tool = &chatStreamTool{}
					pending[index] = tool
				}
				if id := stringField(call, "id"); id != "" {
					if tool.id != "" && tool.id != id {
						return fmt.Errorf("llm: streaming tool call id changed")
					}
					tool.id = id
				}
				function, _ := call["function"].(map[string]any)
				if value := function["arguments"]; value != nil {
					if _, ok := value.(string); !ok {
						return fmt.Errorf("llm: streamed tool arguments must be a JSON string")
					}
				}
				if name := stringField(function, "name"); name != "" {
					tool.name += name
				}
				if args, ok := function["arguments"].(string); ok {
					if tool.arguments.Len()+len(args) > 4<<20 {
						return fmt.Errorf("llm: streaming tool arguments exceed limit")
					}
					tool.arguments.WriteString(args)
				}
			}
			if finish := stringField(choice, "finish_reason"); finish != "" {
				switch finish {
				case "stop", "tool_calls", "function_call":
					finished = true
				default:
					return fmt.Errorf("llm: incomplete chat stream (finish_reason=%s)", finish)
				}
			}
		}
		return nil
	}
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		return process(data)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			if done {
				break
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !done && !finished {
		return fmt.Errorf("llm: chat stream ended before completion")
	}
	// Never publish a tool call while its JSON is still arriving, or after a
	// truncated/error stream. Consumers receive all calls in stable index order.
	indices := make([]int, 0, len(pending))
	for index := range pending {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	calls := make([]ToolCall, 0, len(indices))
	ids := map[string]bool{}
	for _, index := range indices {
		tool := pending[index]
		if tool.id == "" || tool.name == "" || ids[tool.id] {
			return fmt.Errorf("llm: malformed streaming tool identity")
		}
		ids[tool.id] = true
		args := map[string]any{}
		if raw := strings.TrimSpace(tool.arguments.String()); raw != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil || args == nil {
				return fmt.Errorf("llm: invalid streamed tool arguments for %s", tool.name)
			}
		}
		calls = append(calls, ToolCall{ID: tool.id, Name: nativeToolName(tool.name, req.Tools), Arguments: args})
	}
	if !visible && len(calls) == 0 {
		return fmt.Errorf("llm: chat stream output is empty (no text or tool calls)")
	}
	for _, call := range calls {
		if !emit(ChatEvent{Type: ChatEventToolCall, ToolCall: &call}) {
			return ctx.Err()
		}
	}
	if !emit(ChatEvent{Type: ChatEventUsage, Usage: &usage}) {
		return ctx.Err()
	}
	response := &GenerateResponse{Provider: ProviderOpenAICompatible, Model: model}
	if reasoningPresent {
		value := reasoning.String()
		response.ReasoningContent = &value
	}
	if !emit(ChatEvent{Type: ChatEventDone, Response: response}) {
		return ctx.Err()
	}
	return nil
}

func chatReasoningContent(payload map[string]any) *string {
	choices, _ := payload["choices"].([]any)
	if len(choices) == 0 {
		return nil
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	for _, key := range []string{"reasoning_content", "reasoning", "thinking"} {
		if value, ok := message[key].(string); ok {
			return &value
		}
	}
	return nil
}
