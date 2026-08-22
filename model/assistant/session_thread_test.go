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
		Key:  ThreadMemoryKey("group:123"),
		Kind: MemoryKindThread, Topic: "会话状态",
		Content: "正在排查上下文变短，已定位到 16K 被写死，下一步做记忆分层。",
	}})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "9", MessageID: "m-1"}

	note := runtime.sessionThreadNote(context.Background(), event)
	if !strings.Contains(note, "记忆分层") {
		t.Fatalf("thread note = %q", note)
	}
}

func TestSessionThreadNoteIgnoresOtherSessionKeys(t *testing.T) {
	runtime, _ := threadRuntime([]StructuredMemoryItem{{
		Key:  ThreadMemoryKey("group:999"),
		Kind: MemoryKindThread, Topic: "会话状态", Content: "别的群的状态",
	}})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "9", MessageID: "m-1"}

	if note := runtime.sessionThreadNote(context.Background(), event); note != "" {
		t.Fatalf("thread note leaked across sessions: %q", note)
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
