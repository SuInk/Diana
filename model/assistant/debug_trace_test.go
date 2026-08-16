// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

func TestDebugTraceRecordsModelContextOnlyWhenEnabled(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1"}
	request := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "完整系统提示"},
		{Role: llm.RoleUser, Content: "看图", Parts: []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: "data:image/png;base64,secret"}}},
	}}

	for _, test := range []struct {
		name    string
		enabled bool
		want    int
	}{
		{name: "disabled", enabled: false, want: 0},
		{name: "enabled", enabled: true, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs := &captureAppLogs{}
			runtime := NewRuntime(BotConfig{DebugModeEnabled: test.enabled}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			runtime.SetAppLogWriter(logs)
			ctx := runtime.withDebugTraceContext(context.Background(), event)
			run := runtime.withDebugTraceRun(ctx, func(provider LLMProvider) (string, error) {
				response, err := provider.Generate(ctx, request)
				if err != nil {
					return "", err
				}
				return response.Text, nil
			})
			if _, err := run(&capturingLLMProvider{reply: "模型回复"}); err != nil {
				t.Fatal(err)
			}
			entries := logs.entriesSnapshot()
			if len(entries) != test.want {
				t.Fatalf("entries = %#v, want %d", entries, test.want)
			}
			if !test.enabled {
				return
			}
			entry := entries[0]
			if entry.Kind != applog.KindDebug || entry.Action != "qqbot.debug_trace" || entry.Target != event.MessageID {
				t.Fatalf("entry = %#v", entry)
			}
			loggedRequest, ok := entry.Metadata["request"].(llm.GenerateRequest)
			if !ok || loggedRequest.Messages[0].Content != "完整系统提示" {
				t.Fatalf("request metadata = %#v", entry.Metadata["request"])
			}
			imageURL := loggedRequest.Messages[1].Parts[0].ImageURL
			if !strings.HasPrefix(imageURL, "[data URL omitted:") || strings.Contains(imageURL, "secret") {
				t.Fatalf("image URL was not sanitized: %q", imageURL)
			}
		})
	}
}

func TestDebugTraceRedactsOneBotV11AgentProtocol(t *testing.T) {
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "tool available: " + dianaOneBotV11ToolName},
		{Role: llm.RoleAssistant, Content: `{"action":"tool","tool":"diana.onebot_v11","input":{"action":"get_credentials","params":{"domain":"secret.example"}}}`},
		{Role: llm.RoleUser, Content: "工具 diana.onebot_v11 执行成功：owner-secret"},
	}}
	sanitized := sanitizeDebugGenerateRequest(req)
	if sanitized.Messages[0].Content != req.Messages[0].Content {
		t.Fatalf("system prompt was unexpectedly redacted: %q", sanitized.Messages[0].Content)
	}
	for _, message := range sanitized.Messages[1:] {
		if strings.Contains(message.Content, "secret") || !strings.Contains(message.Content, "omitted") {
			t.Fatalf("protocol message was not redacted: %q", message.Content)
		}
	}
	response := sanitizeDebugGenerateResponse(req, &llm.GenerateResponse{Text: "owner-secret"})
	if response == nil || strings.Contains(response.Text, "owner-secret") || !strings.Contains(response.Text, "omitted") {
		t.Fatalf("response was not redacted: %#v", response)
	}
}
