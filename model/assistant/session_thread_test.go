// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func threadRuntime(items []StructuredMemoryItem) (*Runtime, *testStructuredMemoryStore) {
	memory := &testStructuredMemoryStore{items: items}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetStructuredMemoryStore(memory)
	return runtime, memory
}

func TestSessionThreadNoteReturnsCurrentState(t *testing.T) {
	runtime, _ := threadRuntime([]StructuredMemoryItem{{
		ScopeKey: "group:123",
		Key:      ThreadMemoryKey("group:123"),
		Kind:     MemoryKindThread, Topic: "会话状态",
		Content: "正在排查上下文变短，已定位到 16K 被写死，下一步做记忆分层。",
	}})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "9", MessageID: "m-1"}

	note := runtime.sessionThreadNote(context.Background(), event)
	if !strings.Contains(note, "记忆分层") {
		t.Fatalf("thread note = %q", note)
	}
}

// 落库的 key 是归一化过的：normalizeMemoryKey 丢掉冒号且不补分隔符，
// "thread.group:123" 变成 "thread.group123"。读取端以前拿未归一化的
// ThreadMemoryKey 做精确比较，于是线上一条便签都注入不进去。
func TestSessionThreadNoteFindsNormalizedKey(t *testing.T) {
	runtime, _ := threadRuntime([]StructuredMemoryItem{{
		ScopeKey: "group:123",
		Key:      "thread.group123",
		Kind:     MemoryKindThread, Topic: "会话状态",
		Content: "正在排查上下文预算，下一步补层内埋点。",
	}})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "9", MessageID: "m-1"}

	note := runtime.sessionThreadNote(context.Background(), event)
	if !strings.Contains(note, "层内埋点") {
		t.Fatalf("归一化后的 key 取不到便签: %q", note)
	}
}

func TestSessionThreadNoteIgnoresOtherSessionKeys(t *testing.T) {
	runtime, memory := threadRuntime([]StructuredMemoryItem{{
		ScopeKey: "group:999",
		Key:      ThreadMemoryKey("group:999"),
		Kind:     MemoryKindThread, Topic: "会话状态", Content: "别的群的状态",
	}})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "9", MessageID: "m-1"}

	if note := runtime.sessionThreadNote(context.Background(), event); note != "" {
		t.Fatalf("thread note leaked across sessions: %q", note)
	}
	// 去掉 key 比较之后，会话隔离全靠查询的取值范围。默认范围会捎上「本人的
	// visibility=user 记忆」，对便签毫无用处却能放别的会话进来，必须收窄。
	if len(memory.queries) == 0 {
		t.Fatal("没有发出便签查询")
	}
	for _, query := range memory.queries {
		if !query.CurrentSessionOnly {
			t.Fatalf("便签查询没有限定本会话: %#v", query)
		}
	}
}

func TestSessionThreadNoteEmptyWithoutStructuredMemory(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "9", MessageID: "m-1"}

	if note := runtime.sessionThreadNote(context.Background(), event); note != "" {
		t.Fatalf("thread note without a store = %q", note)
	}
}

func TestMemoryContextExcludesThreadFromRetrieval(t *testing.T) {
	runtime, memory := threadRuntime([]StructuredMemoryItem{{
		Key:  ThreadMemoryKey("group:123"),
		Kind: MemoryKindThread, Topic: "会话状态", Content: "正在讨论记忆分层方案",
	}})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "9", MessageID: "m-1", RawMessage: "记忆分层"}

	got := runtime.memoryContext(context.Background(), event, event.RawMessage)
	if strings.Contains(got, "正在讨论记忆分层方案") {
		t.Fatalf("thread was double-injected through retrieval: %q", got)
	}
	found := false
	for _, query := range memory.queries {
		for _, kind := range query.ExcludeKinds {
			if kind == MemoryKindThread {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("retrieval query did not exclude the thread kind")
	}
}

func TestFitSessionThreadToBudgetDropsOldestLines(t *testing.T) {
	note := "第一行已完结的旧话题\n第二行仍在推进的事\n第三行还悬着的问题"
	budget := llm.EstimateTextTokens("第一行已完结的旧话题\n第二行仍在推进的事")

	got := fitSessionThreadToBudget(note, budget)
	if got == "" || strings.Contains(got, "第三行还悬着的问题") {
		t.Fatalf("trimmed thread = %q", got)
	}
	if llm.EstimateTextTokens(got) > budget {
		t.Fatalf("trimmed thread still exceeds budget: %d > %d", llm.EstimateTextTokens(got), budget)
	}
}

func TestFitSessionThreadToBudgetKeepsShortNote(t *testing.T) {
	note := "正在讨论记忆分层"
	if got := fitSessionThreadToBudget(note, 4096); got != note {
		t.Fatalf("short thread was altered: %q", got)
	}
	if got := fitSessionThreadToBudget(note, 0); got != "" {
		t.Fatalf("zero budget should drop the note, got %q", got)
	}
}
