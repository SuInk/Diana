// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestRecordTemporaryMemoryContextOnlyRecordsInjectedState(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Platform: PlatformTelegram, ProfileID: "bot-a", Kind: EventKindPrivate, UserID: "user-a", MessageID: "message-a"}
	expiresAt := time.Date(2026, 9, 4, 13, 0, 0, 0, time.Local)
	preload := &promptContextPreload{
		threadStates:        []ThreadState{{ID: "state-a", TaskKind: "guess.character", State: json.RawMessage(`{"target":"DIO"}`), Version: 2, ExpiresAt: expiresAt}},
		sessionThreadMemory: &StructuredMemoryItem{ID: "thread-a", Topic: "当前任务", SourceMessageID: "source-a", ExpiresAt: expiresAt},
	}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system", Priority: llm.MessagePrioritySystem},
		{Role: llm.RoleUser, Content: privateThreadStateMarker + `\n[{"id":"state-a"}]`, Priority: llm.MessagePriorityPlugin, AtomicText: true},
		{Role: llm.RoleUser, Content: sessionThreadPromptPrefix + "正在排查 Telegram", Priority: llm.MessagePrioritySummary, AtomicText: true},
		{Role: llm.RoleUser, Content: "继续", Priority: llm.MessagePriorityCurrent},
	}
	runtime.recordTemporaryMemoryContext(context.Background(), event, BotConfig{}.WithDefaults(), messages, preload)
	entries := logs.entriesSnapshot()
	if len(entries) != 1 || entries[0].Action != "diana.memory.temporary" {
		t.Fatalf("entries = %#v", entries)
	}
	memories, ok := entries[0].Metadata["memories"].([]map[string]any)
	if !ok || len(memories) != 2 {
		t.Fatalf("memories = %#v", entries[0].Metadata["memories"])
	}
	if memories[0]["task_kind"] != "guess.character" || memories[1]["content"] != "正在排查 Telegram" {
		t.Fatalf("memories = %#v", memories)
	}
}
