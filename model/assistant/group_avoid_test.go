// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

// 一个群友叫 Diana 别说「草」，Diana 回别人时也要带上这条；普通 instruction 仍只跟着本人。
func TestGroupAvoidRequestAppliesToWholeGroup(t *testing.T) {
	requester := MessageEvent{Kind: EventKindGroup, GroupID: "12345", UserID: "10001", MessageID: "m-1", RawMessage: "今天好热"}
	other := requester
	other.UserID, other.MessageID = "10002", "m-2"
	memory := &testStructuredMemoryStore{items: []StructuredMemoryItem{
		{
			ScopeKey: sessionKey(requester), SubjectUserID: "10001", Key: "instruction.group.avoid.草", Kind: MemoryKindInstruction,
			Topic: "说话要求", Content: "群友10001要求 Diana 在本群不再说「草」",
			Visibility: MemoryVisibilitySession, Confidence: 0.95, Importance: 0.7,
		},
		{
			ScopeKey: sessionKey(requester), SubjectUserID: "10001", Key: "instruction.reply.style.hamster", Kind: MemoryKindInstruction,
			Topic: "说话要求", Content: "群友10001希望 Diana 跟他说鼠话",
			Visibility: MemoryVisibilitySession, Confidence: 0.95, Importance: 0.7,
		},
	}}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetStructuredMemoryStore(memory)
	cfg := runtime.effectiveConfigForEvent(other)

	got := runtime.groupAvoidPrompt(other, cfg)
	if !strings.Contains(got, "【这个群不想听你说的】") || !strings.Contains(got, "不再说「草」") || strings.Contains(got, "鼠话") {
		t.Fatalf("avoid prompt for another member = %q", got)
	}
	last := memory.queries[len(memory.queries)-1]
	if !last.CurrentSessionOnly || last.KeyPrefix != GroupAvoidMemoryKeyPrefix {
		t.Fatalf("query = %#v", last)
	}

	private := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "p-1", RawMessage: "在吗"}
	if got := runtime.groupAvoidPrompt(private, cfg); got != "" {
		t.Fatalf("private chat must not carry group avoid requests: %q", got)
	}

	// 提要求的人自己发言时，这条已经在尾部了，长期记忆那层不再重复；他自己的鼠话要求照常在。
	memoryText := runtime.memoryContext(context.Background(), requester, requester.RawMessage)
	if strings.Contains(memoryText, "不再说「草」") || !strings.Contains(memoryText, "鼠话") {
		t.Fatalf("memory context = %q", memoryText)
	}
}
