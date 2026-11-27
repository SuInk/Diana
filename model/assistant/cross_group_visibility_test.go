// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// visibilityHistoryStore 是按会话记发言人的历史存储替身：名录和检索都照 SQLite 实现
// 认 Sessions 限定。
type visibilityHistoryStore struct {
	capturingHistorySearchStore
	// speakerSessions 是每个账号说过话的会话。
	speakerSessions map[string][]string
	names           map[string]string
}

func (s *visibilityHistoryStore) ListSpeakerSessions(_ context.Context, userID, prefix string) ([]string, error) {
	var out []string
	for _, session := range s.speakerSessions[userID] {
		if strings.HasPrefix(session, prefix) {
			out = append(out, session)
		}
	}
	return out, nil
}

func (s *visibilityHistoryStore) ListMessageSpeakers(_ context.Context, query MessageSpeakerQuery) ([]MessageSpeaker, error) {
	var out []MessageSpeaker
	for userID, name := range s.names {
		if !strings.Contains(strings.ToLower(name), strings.ToLower(query.Name)) && userID != query.Name {
			continue
		}
		visible := false
		for _, session := range s.speakerSessions[userID] {
			if query.CrossSession && (len(query.Sessions) == 0 || slices.Contains(query.Sessions, session)) {
				visible = true
			}
			if !query.CrossSession && session == query.Session {
				visible = true
			}
		}
		if visible {
			out = append(out, MessageSpeaker{UserID: userID, Names: []string{name}, ExactMatch: name == query.Name})
		}
	}
	return out, nil
}

// 生产 2026-11-27：A 群的人问只在 B 群说过话的 Ray 的 QQ 号。提问人不在 B 群，
// 以前跨群检索只滤掉命中的消息，resolved_senders 照样给出了 Ray 的账号。现在按昵称
// 找人、检索和记忆都只在提问人核实过也在的群里取；主人照旧不受限。
func TestCrossGroupReadsOnlyGroupsTheRequesterIsIn(t *testing.T) {
	channel := &crossGroupMembershipChannel{allowed: map[string]bool{"group-b|asker": true}}
	r := NewRuntime(BotConfig{OwnerID: "owner", CrossGroupMemoryEnabled: boolPointer(true)}, channel, NewPluginManager(), nil, nil, nil, nil)
	asker := MessageEvent{Kind: EventKindGroup, GroupID: "group-a", UserID: "asker", MessageID: "m1", Time: 200}
	prefix := groupHistorySessionPrefix(asker)
	store := &visibilityHistoryStore{
		speakerSessions: map[string][]string{
			// group-c 他说过话但已经退群：成员接口查不到，不算看得见。
			"asker": {prefix + "group-a", prefix + "group-b", prefix + "group-c"},
			"ray":   {prefix + "group-d"},
			"bob":   {prefix + "group-b"},
		},
		names: map[string]string{"ray": "Ray酱厉害", "bob": "Bob"},
	}
	r.SetMessageHistoryStore(store)

	view := r.crossGroupVisibility(context.Background(), asker)
	if !view.restricted || !slices.Equal(view.sessions, []string{prefix + "group-b"}) {
		t.Fatalf("只该看得见 group-b：%+v", view)
	}

	_, err := newDianaChatHistoryTool(r, asker).Run(context.Background(), map[string]any{"scope": "all_groups", "sender_name": "Ray酱厉害", "limit": 1})
	if err == nil || strings.Contains(err.Error(), "ray") {
		t.Fatalf("提问人看不到 Ray 所在的群，不该解析出他的账号：err=%v", err)
	}
	if store.calls != 0 {
		t.Fatal("找不到人时不该再去检索")
	}

	if _, err := newDianaChatHistoryTool(r, asker).Run(context.Background(), map[string]any{"scope": "all_groups", "sender_name": "Bob"}); err != nil {
		t.Fatalf("同在 group-b 的 Bob 能找到：%v", err)
	}
	if !slices.Equal(store.query.Sessions, []string{sessionKey(asker), prefix + "group-b"}) || !slices.Equal(store.query.SenderUserIDs, []string{"bob"}) {
		t.Fatalf("检索要限定在当前群和 group-b：%+v", store.query)
	}

	query := r.memoryQueryForEvent(context.Background(), asker, "蜜桃四季春", 10)
	if !query.CrossGroupRestricted || !slices.Equal(query.CrossGroupSessions, []string{prefix + "group-b"}) {
		t.Fatalf("其他群公共记忆也只取 group-b：%+v", query)
	}

	owner := asker
	owner.UserID = "owner"
	if view := r.crossGroupVisibility(context.Background(), owner); view.restricted {
		t.Fatal("主人不受限")
	}
	if _, err := newDianaChatHistoryTool(r, owner).Run(context.Background(), map[string]any{"scope": "all_groups", "sender_name": "Ray酱厉害"}); err != nil {
		t.Fatalf("主人能找到 Ray：%v", err)
	}
	if store.query.Sessions != nil {
		t.Fatalf("主人的检索不限定会话：%+v", store.query.Sessions)
	}
}
