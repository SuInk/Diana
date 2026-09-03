// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

type debugTraceContextKey struct{}

type debugTraceState struct {
	event    MessageEvent
	sequence atomic.Int64
}

func (r *Runtime) withDebugTraceContext(ctx context.Context, event MessageEvent) context.Context {
	if ctx == nil || strings.TrimSpace(event.MessageID) == "" || !r.effectiveConfigForEvent(event).DebugModeEnabled {
		return ctx
	}
	return context.WithValue(ctx, debugTraceContextKey{}, &debugTraceState{event: event})
}

func debugTraceFromContext(ctx context.Context) *debugTraceState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(debugTraceContextKey{}).(*debugTraceState)
	return state
}

func (r *Runtime) withDebugTraceRun(ctx context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	state := debugTraceFromContext(ctx)
	if state == nil {
		return run
	}
	return func(provider LLMProvider) (string, error) {
		return run(&debugTraceLLMProvider{runtime: r, state: state, provider: provider})
	}
}

type debugTraceLLMProvider struct {
	runtime  *Runtime
	state    *debugTraceState
	provider LLMProvider
}

func (p *debugTraceLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	startedAt := time.Now()
	response, err := p.provider.Generate(ctx, req)
	metadata := map[string]any{
		"phase":       "model_request",
		"purpose":     debugModelPurpose(req),
		"request":     sanitizeDebugGenerateRequest(req),
		"duration_ms": time.Since(startedAt).Milliseconds(),
	}
	message := "模型请求完成"
	if response != nil {
		metadata["response"] = sanitizeDebugGenerateResponse(req, response)
		metadata["provider"] = response.Provider
		metadata["model"] = response.Model
		metadata["usage"] = response.Usage
	}
	if err != nil {
		message = "模型请求失败"
		metadata["error"] = err.Error()
	}
	p.runtime.recordDebugTrace(p.state, message, metadata)
	return response, err
}

func (r *Runtime) recordDebugTrace(state *debugTraceState, message string, metadata map[string]any) {
	writer := r.appLogWriter()
	if state == nil || writer == nil {
		return
	}
	metadata["sequence"] = state.sequence.Add(1)
	metadata["platform"] = state.event.Platform
	metadata["profile_id"] = state.event.ProfileID
	metadata["kind"] = state.event.Kind
	metadata["group_id"] = state.event.GroupID
	metadata["user_id"] = state.event.UserID
	metadata["message_id"] = state.event.MessageID
	logCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:     applog.KindDebug,
		Level:    applog.LevelInfo,
		Action:   "diana.debug_trace",
		Message:  message,
		Actor:    oneBotEventActor(state.event),
		Target:   strings.TrimSpace(state.event.MessageID),
		Metadata: metadata,
	})
}

func sanitizeDebugGenerateRequest(req llm.GenerateRequest) llm.GenerateRequest {
	cloned := req
	cloned.Messages = make([]llm.Message, len(req.Messages))
	for index, message := range req.Messages {
		cloned.Messages[index] = message
		if oneBotV11DebugProtocolMessage(message) {
			cloned.Messages[index].Content = "[OneBot v11 Agent protocol payload omitted]"
		} else if privateThreadStateDebugMessage(message) {
			cloned.Messages[index].Content = "[private thread state payload omitted]"
		}
		cloned.Messages[index].ToolCalls = sanitizeDebugThreadStateToolCalls(message.ToolCalls)
		cloned.Messages[index].Parts = append([]llm.ContentPart(nil), message.Parts...)
		for partIndex := range cloned.Messages[index].Parts {
			part := &cloned.Messages[index].Parts[partIndex]
			if strings.HasPrefix(strings.TrimSpace(part.ImageURL), "data:") {
				part.ImageURL = fmt.Sprintf("[data URL omitted: %d chars]", len(part.ImageURL))
			}
		}
	}
	return cloned
}

func sanitizeDebugGenerateResponse(req llm.GenerateRequest, response *llm.GenerateResponse) *llm.GenerateResponse {
	if response == nil {
		return nil
	}
	cloned := *response
	cloned.ToolCalls = sanitizeDebugThreadStateToolCalls(response.ToolCalls)
	if responseContainsThreadStateToolCall(response) || strings.Contains(cloned.Text, dianaThreadStateToolName) {
		cloned.Text = "[private thread state model payload omitted]"
	}
	if strings.Contains(cloned.Text, dianaOneBotV11ToolName) || requestContainsOneBotV11DebugProtocol(req) {
		cloned.Text = "[OneBot v11 model payload omitted]"
	}
	return &cloned
}

func privateThreadStateDebugMessage(message llm.Message) bool {
	if message.ToolName == dianaThreadStateToolName || strings.Contains(message.Content, privateThreadStateMarker) {
		return true
	}
	if strings.Contains(message.Content, dianaThreadStateToolName) && (message.Role == llm.RoleAssistant || message.Role == llm.RoleTool) {
		return true
	}
	for _, call := range message.ToolCalls {
		if call.Name == dianaThreadStateToolName {
			return true
		}
	}
	return false
}

func responseContainsThreadStateToolCall(response *llm.GenerateResponse) bool {
	if response == nil {
		return false
	}
	for _, call := range response.ToolCalls {
		if call.Name == dianaThreadStateToolName {
			return true
		}
	}
	return false
}

func sanitizeDebugThreadStateToolCalls(calls []llm.ToolCall) []llm.ToolCall {
	cloned := make([]llm.ToolCall, len(calls))
	for index, call := range calls {
		cloned[index] = call
		if call.Name != dianaThreadStateToolName {
			cloned[index].Arguments = cloneStringAnyMap(call.Arguments)
			continue
		}
		cloned[index].Arguments = map[string]any{
			"operation": strings.TrimSpace(configToolString(call.Arguments, "operation")),
			"task_kind": strings.TrimSpace(configToolString(call.Arguments, "task_kind")),
		}
	}
	return cloned
}

func cloneStringAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func requestContainsOneBotV11DebugProtocol(req llm.GenerateRequest) bool {
	for _, message := range req.Messages {
		if oneBotV11DebugProtocolMessage(message) {
			return true
		}
	}
	return false
}

func oneBotV11DebugProtocolMessage(message llm.Message) bool {
	if !strings.Contains(message.Content, dianaOneBotV11ToolName) {
		return false
	}
	if message.Role == llm.RoleAssistant {
		return true
	}
	return message.Role == llm.RoleUser && strings.Contains(message.Content, "工具 "+dianaOneBotV11ToolName+" 执行")
}

func debugModelPurpose(req llm.GenerateRequest) string {
	var system strings.Builder
	for _, message := range req.Messages {
		if message.Role == llm.RoleSystem {
			system.WriteString(message.Content)
			system.WriteByte('\n')
		}
	}
	prompt := system.String()
	switch {
	case strings.Contains(prompt, "planner"):
		return "planner"
	case strings.Contains(prompt, "主动回复路由器"):
		return "proactive_reply_router"
	case strings.Contains(prompt, "选择本轮上下文和工具"):
		return "agent_scope_router"
	case strings.Contains(prompt, "action") && strings.Contains(prompt, "tool"):
		return "agent_model"
	case strings.Contains(prompt, "机器人自动回复"):
		return "bot_reply_detection"
	default:
		return "chat_completion"
	}
}
