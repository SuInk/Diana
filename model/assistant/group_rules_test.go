// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

// 一个群友叫 Diana 别说「草」，Diana 回别人时也要带上；普通 instruction 仍只跟着本人。
func TestGroupRulesApplyToWholeGroup(t *testing.T) {
	requester := MessageEvent{Kind: EventKindGroup, GroupID: "12345", UserID: "10001", MessageID: "m-1", RawMessage: "今天好热"}
	other := requester
	other.UserID, other.MessageID = "10002", "m-2"
	memory := &testStructuredMemoryStore{items: []StructuredMemoryItem{
		{
			ID: "rule-1", ScopeKey: sessionKey(requester), Key: "instruction.group.no.cao", Kind: MemoryKindInstruction,
			Topic: "说话要求", Content: "群友10001要求 Diana 在本群不再说「草」",
			Visibility: MemoryVisibilitySession, Confidence: 0.95, Importance: 0.7,
		},
		{
			ID: "personal-1", ScopeKey: sessionKey(requester), SubjectUserID: "10001", Key: "instruction.reply.style.hamster", Kind: MemoryKindInstruction,
			Topic: "说话要求", Content: "群友10001希望 Diana 跟他说鼠话",
			Visibility: MemoryVisibilitySession, Confidence: 0.95, Importance: 0.7,
		},
	}}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetStructuredMemoryStore(memory)
	cfg := runtime.effectiveConfigForEvent(other)

	got := runtime.groupRulesPrompt(other, cfg)
	if !strings.Contains(got, "【这个群的约定】") || !strings.Contains(got, "不再说「草」") || strings.Contains(got, "鼠话") {
		t.Fatalf("group rules prompt for another member = %q", got)
	}
	if last := memory.queries[len(memory.queries)-1]; !last.CurrentSessionOnly || !last.GroupRulesOnly {
		t.Fatalf("query = %#v", last)
	}
	private := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "p-1", RawMessage: "在吗"}
	if got := runtime.groupRulesPrompt(private, cfg); got != "" {
		t.Fatalf("private chat must not carry group rules: %q", got)
	}

	// 尾部已经有了，长期记忆那层不再重复；本人的鼠话要求照常在。
	memoryText := runtime.memoryContext(context.Background(), requester, requester.RawMessage)
	if strings.Contains(memoryText, "不再说「草」") || !strings.Contains(memoryText, "鼠话") {
		t.Fatalf("memory context = %q", memoryText)
	}

	// 门控看得到本群约定并知道它是群约定，别人说「又可以说了」时才能复用原 key 撤销。
	existing := memoryGateExistingMemories(memory.items, "10002")
	if len(existing) != 1 || existing[0].Key != "instruction.group.no.cao" || existing[0].AppliesTo != MemoryAudienceGroup {
		t.Fatalf("gate existing memories = %#v", existing)
	}
}
