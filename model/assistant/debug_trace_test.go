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
