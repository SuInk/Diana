// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

type forgettableMemoryStore struct {
	testStructuredMemoryStore
	forgotten []string
}

func (s *forgettableMemoryStore) ListStructuredMemories(_ context.Context, q StructuredMemoryQuery) ([]StructuredMemoryItem, error) {
	for _, item := range s.items {
		if len(q.IDs) > 0 && item.ID == q.IDs[0] && item.Status == MemoryStatusActive {
			return []StructuredMemoryItem{item}, nil
		}
	}
	return nil, nil
}

func (s *forgettableMemoryStore) ForgetStructuredMemory(_ context.Context, id string) (bool, error) {
	for index := range s.items {
		if s.items[index].ID == id && s.items[index].Status == MemoryStatusActive {
			s.items[index].Status = MemoryStatusForgotten
			s.forgotten = append(s.forgotten, id)
			return true, nil
		}
	}
	return false, nil
}

// 生产上门控把群友说的「gitea官方群」错绑成当前群号，群主指出记错了也删不掉，下一轮
// 照旧注入。memory 工具的 forget 按群管的规矩限人：本人、主人、本群群主和管理员。
func TestMemoryToolForgetFollowsModerationRoles(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "500", UserID: "speaker"}
	session := sessionKey(event)
	other := MessageEvent{Kind: EventKindGroup, GroupID: "600", UserID: "speaker"}
	memory := func(id, subject, source string) StructuredMemoryItem {
		return StructuredMemoryItem{ID: id, Kind: MemoryKindInstruction, SubjectUserID: subject, SourceSession: source,
			Visibility: MemoryVisibilitySession, Status: MemoryStatusActive, Content: "在 Gitea 官方群 500 中，只有糖能创建 Issue"}
	}
	cases := []struct {
		name   string
		role   string
		owner  bool
		item   StructuredMemoryItem
		allow  bool
		reason string
	}{
		{name: "subject", role: "member", item: memory("m", "speaker", sessionKey(other)), allow: true},
		{name: "owner", role: "member", owner: true, item: memory("m", "tang", sessionKey(other)), allow: true},
		{name: "group owner same group", role: "owner", item: memory("m", "tang", session), allow: true},
		{name: "group admin same group", role: "admin", item: memory("m", "tang", session), allow: true},
		{name: "group rule anyone", role: "member", item: memory("m", "", session), allow: true},
		{name: "member others memory", role: "member", item: memory("m", "tang", session)},
		{name: "group owner other group", role: "owner", item: memory("m", "tang", sessionKey(other))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultBotConfig()
			if tc.owner {
				cfg.OwnerID = "speaker"
			}
			channel := &bodyAccountChannel{members: map[string]OneBotGroupMemberInfo{"speaker": {Role: tc.role}}}
			r := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, nil)
			store := &forgettableMemoryStore{}
			store.items = []StructuredMemoryItem{tc.item}
			r.SetStructuredMemoryStore(store)
			tool := &dianaMemoryTool{runtime: r, event: event}
			_, err := tool.Run(context.Background(), map[string]any{"operation": "forget", "id": "m", "reason": "群主说 500 不是 Gitea 官方群，记错了"})
			if tc.allow != (err == nil) || tc.allow != (len(store.forgotten) == 1) {
				t.Fatalf("allow=%v err=%v forgotten=%v", tc.allow, err, store.forgotten)
			}
		})
	}
}

func TestMemoryToolForgetRequiresReason(t *testing.T) {
	r := NewRuntime(DefaultBotConfig(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &forgettableMemoryStore{}
	store.items = []StructuredMemoryItem{{ID: "m", SubjectUserID: "u", Status: MemoryStatusActive}}
	r.SetStructuredMemoryStore(store)
	tool := &dianaMemoryTool{runtime: r, event: MessageEvent{Kind: EventKindGroup, GroupID: "500", UserID: "u"}}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "forget", "id": "m"}); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("forget without reason should be rejected: %v", err)
	}
	if len(store.forgotten) != 0 {
		t.Fatal("memory forgotten without reason")
	}
}
