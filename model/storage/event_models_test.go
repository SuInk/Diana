// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// 事件列表要说得出这条消息用了哪些模型，主回复用的是哪个。带图的一轮先走视觉
// 模型再回到对话模型，所以主回复模型按首次调用排序，可以不止一个。
func TestInboundEventTokenUsageCollectsModelsPerMessage(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "event-models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	base := time.Now().Add(-time.Hour)
	calls := []struct{ model, purpose string }{
		{"intent-mini", "reply_intent_router"},
		{"vision-pro", "reply"},
		{"chat-main", "reply"},
		{"chat-main", "reply"},
		{"intent-mini", "memory_extract"},
		{"", "reply"},
	}
	for index, call := range calls {
		if err := store.AppendLog(ctx, applog.Entry{
			Action:    "llm_usage",
			Target:    "m1",
			CreatedAt: base.Add(time.Duration(index) * time.Second),
			Metadata:  map[string]any{"model": call.model, "provider": "openai_compatible", "purpose": call.purpose, "input_tokens": 10, "output_tokens": 5},
		}); err != nil {
			t.Fatal(err)
		}
	}

	byMessage, _, err := store.inboundEventTokenUsage(ctx, base.Add(-time.Minute), "")
	if err != nil {
		t.Fatal(err)
	}
	usage := byMessage["m1"]
	if !slices.Equal(usage.replyModels, []string{"vision-pro", "chat-main"}) {
		t.Fatalf("reply models = %v", usage.replyModels)
	}
	want := []InboundEventModelUsage{
		{Model: "intent-mini", Provider: "openai_compatible", Calls: 2},
		{Model: "vision-pro", Provider: "openai_compatible", Calls: 1},
		{Model: "chat-main", Provider: "openai_compatible", Calls: 2},
	}
	if !slices.Equal(usage.models, want) {
		t.Fatalf("models = %#v", usage.models)
	}
	// 没记模型名的老日志照样计入调用数，只是不出现在模型列表里。
	if usage.LLMCalls != 6 {
		t.Fatalf("calls = %d", usage.LLMCalls)
	}
}

// 上游没报用量的调用要单独数出来：token 合计因此偏少，界面得能说出少了几次。
func TestInboundEventTokenUsageCountsCallsWithoutReportedUsage(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "usage-missing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	base := time.Now().Add(-time.Hour)
	for index, meta := range []map[string]any{
		{"model": "chat-main", "purpose": "reply", "input_tokens": 10, "output_tokens": 5},
		{"model": "gpt-image-2", "purpose": "image_generate", "usage_missing": true},
	} {
		if err := store.AppendLog(ctx, applog.Entry{Action: "llm_usage", Target: "m2", CreatedAt: base.Add(time.Duration(index) * time.Second), Metadata: meta}); err != nil {
			t.Fatal(err)
		}
	}
	byMessage, total, err := store.inboundEventTokenUsage(ctx, base.Add(-time.Minute), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := byMessage["m2"]; got.LLMCalls != 2 || got.UsageMissingCalls != 1 || got.TotalTokens != 15 {
		t.Fatalf("message usage = %#v", got)
	}
	if total.UsageMissingCalls != 1 {
		t.Fatalf("total usage = %#v", total)
	}
}
