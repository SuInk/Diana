// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/SuInk/diana/model/agent"
	"strings"
	"sync"
	"testing"
	"time"

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
		{name: "enabled", enabled: true, want: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs := &captureAppLogs{}
			runtime := NewRuntime(BotConfig{DebugModeEnabled: boolPointer(test.enabled)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
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
			if received := entries[0]; received.Metadata["phase"] != debugTracePhaseEventReceived || received.Metadata["sequence"] != int64(0) {
				t.Fatalf("first entry should mark the event as received: %#v", received)
			}
			entry := entries[1]
			if entry.Kind != applog.KindDebug || entry.Action != "debug_trace" || entry.Target != event.MessageID {
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

func TestDebugTraceRedactsPlatformAgentProtocol(t *testing.T) {
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "tool available: " + dianaPlatformToolName},
		{Role: llm.RoleAssistant, Content: `{"action":"tool","tool":"platform","input":{"operation":"kick","user_id":"secret.example"}}`},
		{Role: llm.RoleUser, Content: "工具 platform 执行成功：owner-secret"},
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

// 调用链里最该看清模型的恰恰是失败的那一步，而失败时没有响应可读模型名。调试
// 装饰器在最外层，中间隔着缓存探针、用量记账和流式几层，要能一路剥到握着选择的
// failover 那层。
func TestLLMProviderIdentitySeesThroughDecorators(t *testing.T) {
	failover := &profileFailoverLLMProvider{profiles: []llm.Profile{
		{Name: "主对话", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "chat-main"}},
		{Name: "后备", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "chat-backup"}},
	}, current: 1}
	wrapped := &promptCacheProbeLLMProvider{provider: &usageAccountingLLMProvider{provider: &streamingLLMProvider{provider: withTransientLLMRetry(failover, true)}}}
	identity, err := llmProviderIdentity(wrapped, "")
	if err != nil {
		t.Fatal(err)
	}
	if identity.ModelID != "chat-backup" || identity.Provider != string(llm.ProviderOpenAICompatible) {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestSubtaskDebugTraceIsolation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		logs := &captureAppLogs{}
		rt := NewRuntime(BotConfig{DebugModeEnabled: boolPointer(enabled)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		rt.SetAppLogWriter(logs)
		parent := rt.withDebugTraceContext(context.Background(), MessageEvent{MessageID: "msg", Kind: EventKindGroup})
		var wg sync.WaitGroup
		for _, id := range []string{"first", "second"} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				ctx := withSubtaskDebugTrace(parent, id)
				run := rt.withDebugTraceRun(ctx, func(provider LLMProvider) (string, error) {
					_, err := provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: id}}})
					return "", err
				})
				if _, err := run(&capturingLLMProvider{reply: id}); err != nil {
					t.Error(err)
				}
				rt.recordSubtaskDebugEvent(ctx, agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "web_search", ToolInput: map[string]any{"query": id}, ToolOutput: id})
			}(id)
		}
		wg.Wait()
		rt.recordDebugTrace(debugTraceFromContext(parent), "parent", map[string]any{"phase": "parent"})
		entries := logs.entriesSnapshot()
		if !enabled {
			if len(entries) != 0 {
				t.Fatal("debug disabled but recorded")
			}
			continue
		}
		if len(entries) != 6 {
			t.Fatalf("entries = %#v", entries)
		}
		sequences := map[int64]bool{}
		counts := map[string]int{}
		for _, entry := range entries {
			seq := entry.Metadata["sequence"].(int64)
			if sequences[seq] {
				t.Fatal("duplicate sequence")
			}
			sequences[seq] = true
			id, _ := entry.Metadata["subtask_id"].(string)
			if id != "" {
				counts[id]++
			}
			if entry.Metadata["phase"] == "parent" && id != "" {
				t.Fatal("child identity leaked to parent")
			}
		}
		if counts["first"] != 2 || counts["second"] != 2 {
			t.Fatalf("counts = %#v", counts)
		}
	}
}

func TestImageDebugTraceTaskAndRedaction(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		logs := &captureAppLogs{}
		runtime := NewRuntime(BotConfig{DebugModeEnabled: boolPointer(enabled)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		runtime.SetAppLogWriter(logs)
		ctx := runtime.withDebugTraceContext(context.Background(), MessageEvent{MessageID: "image-message"})
		ctx = withSubtaskDebugTrace(ctx, "image-task")
		request := map[string]any{"prompt": "画一只猫", "image_count": 1}
		response := &llm.ImageGenerateResponse{Model: "image-model", Images: []string{"data:image/png;base64,private-image"}}
		runtime.recordImageDebugTrace(ctx, llm.ProviderConfig{}, "image_edit", request, response, nil, 25*time.Millisecond)
		runtime.recordImageDebugTrace(ctx, llm.ProviderConfig{}, "image_generate", request, nil, errors.New("upstream failed"), time.Millisecond)
		entries := logs.entriesSnapshot()
		if !enabled {
			if len(entries) != 0 {
				t.Fatal(entries)
			}
			continue
		}
		if len(entries) != 3 {
			t.Fatal(entries)
		}
		for _, entry := range entries[1:] {
			if entry.Metadata["subtask_id"] != "image-task" || entry.Target != "image-message" {
				t.Fatalf("wrong scope: %#v", entry)
			}
			if strings.Contains(fmt.Sprint(entry.Metadata), "private-image") {
				t.Fatal("image payload leaked")
			}
		}
		if entries[1].Metadata["duration_ms"] != int64(25) || entries[2].Metadata["error"] != "upstream failed" {
			t.Fatal(entries)
		}
	}
}

func TestImageTaskPreservesDebugTraceAcrossBackgroundContext(t *testing.T) {
	server := newAPIKeyRuntimeServer(t)
	runtime := newAPIKeyRuntime(t, apiKeyRuntimeProfile(server.URL))
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "image-group", MessageID: "image-event"}
	ctx := context.WithValue(context.Background(), debugTraceContextKey{}, &debugTraceState{event: event})
	done := make(chan error, 1)
	task := PluginTask{Kind: "image", Name: "图片生成", Run: func(ctx context.Context, _ PluginTaskServices) (PluginTaskResult, error) {
		_, _, err := runtime.generateImageWithFailover(ctx, llm.ImageGenerateRequest{Prompt: "a cat"})
		if err == nil {
			source := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n0123456789"))
			_, _, err = runtime.editImageWithFailover(ctx, llm.ImageEditRequest{Prompt: "make it blue", Images: []string{source}})
		}
		done <- err
		return PluginTaskResult{Delivered: true}, err
	}}
	reservation := runtime.reservePluginTasksForTurn(ctx, event, []PluginTask{task})
	if len(reservation.reserved) != 1 {
		t.Fatal(reservation)
	}
	runtime.startPluginTaskReservation(reservation)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("image task did not finish")
	}
	count := 0
	for _, entry := range logs.entriesSnapshot() {
		if entry.Action != "debug_trace" {
			continue
		}
		count++
		if entry.Metadata["subtask_id"] != reservation.reserved[0].id || entry.Target != event.MessageID {
			t.Fatalf("wrong trace association: %#v", entry)
		}
	}
	if count != 2 {
		t.Fatalf("got %d image traces, want generation and edit", count)
	}
}
