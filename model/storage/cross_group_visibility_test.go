// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// 跨群检索、按昵称找人限定会话后，别的群的消息、账号和总数都不出现。
func TestCrossSessionSearchAndSpeakersHonorSessions(t *testing.T) {
	ctx := context.Background()
	s := vectorStore(t)
	for _, item := range []struct {
		session, group, user, name, id string
	}{
		{"bot:group:a", "a", "asker", "提问人", "a1"},
		{"bot:group:b", "b", "asker", "提问人", "b1"},
		{"bot:group:b", "b", "ray", "Ray酱厉害", "b2"},
		{"bot:group:c", "c", "ray", "Ray酱厉害", "c1"},
	} {
		event := speakerEvent(10, item.user, item.name, "", item.id)
		event.GroupID, event.RawMessage = item.group, "蜜桃四季春 "+item.id
		event.Segments = []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": event.RawMessage}}}
		if err := s.AppendMessageEvent(ctx, item.session, event); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := s.ListSpeakerSessions(ctx, "asker", "bot:group:")
	if err != nil || len(sessions) != 2 || sessions[0] != "bot:group:a" || sessions[1] != "bot:group:b" {
		t.Fatalf("speaker sessions=%v err=%v", sessions, err)
	}

	query := assistant.MessageHistorySearchQuery{Session: "bot:group:a", SessionPrefix: "bot:group:", CrossSession: true, Text: "蜜桃四季春", ThroughTime: 100, Limit: 10}
	if _, total, err := s.SearchMessageEvents(ctx, query); err != nil || total != 4 {
		t.Fatalf("unrestricted total=%d err=%v", total, err)
	}
	query.Sessions = []string{"bot:group:a", "bot:group:b"}
	got, total, err := s.SearchMessageEvents(ctx, query)
	if err != nil || total != 3 || len(got) != 3 {
		t.Fatalf("restricted search=%v total=%d err=%v", got, total, err)
	}
	for _, event := range got {
		if event.GroupID == "c" {
			t.Fatalf("restricted search returned group c: %+v", event)
		}
	}

	speakers, err := s.ListMessageSpeakers(ctx, assistant.MessageSpeakerQuery{Session: "bot:group:a", SessionPrefix: "bot:group:", CrossSession: true, Name: "Ray", Sessions: []string{"bot:group:a"}})
	if err != nil || len(speakers) != 0 {
		t.Fatalf("Ray 只在提问人看不到的群里说过话，不该解析出账号：%+v err=%v", speakers, err)
	}
	speakers, err = s.ListMessageSpeakers(ctx, assistant.MessageSpeakerQuery{Session: "bot:group:a", SessionPrefix: "bot:group:", CrossSession: true, Name: "Ray", Sessions: []string{"bot:group:a", "bot:group:b"}})
	if err != nil || len(speakers) != 1 || speakers[0].UserID != "ray" {
		t.Fatalf("可见的群里能解析出 Ray：%+v err=%v", speakers, err)
	}
}

// 限定可见群后，其他群的公共记忆只从这些群取；一个可见群都没有就一条不取。
func TestCrossGroupMemoryHonorsVisibleSessions(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, session := range []string{"qq:group:visible", "qq:group:hidden"} {
		if _, err := s.ApplyMemoryCandidates(context.Background(), assistant.MemoryWriteRequest{
			Session: session, EventKind: assistant.EventKindGroup, GroupID: session, SourceMessageID: session,
			Candidates: []assistant.MemoryCandidate{{Action: assistant.MemoryActionUpsert, Key: session, Kind: assistant.MemoryKindSummary, Topic: "Aurora", Content: "Aurora 摘要 " + session, Confidence: 0.99, Importance: 0.95, Visibility: assistant.MemoryVisibilitySession}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	keys := func(query assistant.StructuredMemoryQuery) map[string]bool {
		t.Helper()
		items, err := s.ListStructuredMemories(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, item := range items {
			out[item.SourceSession] = true
		}
		return out
	}
	base := assistant.StructuredMemoryQuery{Session: "qq:group:target", SubjectUserID: "user", CrossGroup: true, GroupSessionPrefix: "qq:group:", MaxCandidates: 40, Text: "Aurora", Now: time.Now()}
	for _, shared := range []bool{false, true} {
		query := base
		query.SharedPublicOnly = shared
		if got := keys(query); !got["qq:group:visible"] || !got["qq:group:hidden"] {
			t.Fatalf("shared=%v 不限定时两个群都在：%v", shared, got)
		}
		query.CrossGroupRestricted, query.CrossGroupSessions = true, []string{"qq:group:visible"}
		if got := keys(query); !got["qq:group:visible"] || got["qq:group:hidden"] {
			t.Fatalf("shared=%v 只该取可见的群：%v", shared, got)
		}
		query.CrossGroupSessions = nil
		if got := keys(query); len(got) != 0 {
			t.Fatalf("shared=%v 没有可见的群就一条不取：%v", shared, got)
		}
	}
}
