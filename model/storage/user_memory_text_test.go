// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// 「最近发言」写进库时就把 @号码 换成昵称、把引用标记换成「回复 某人：原话」：
// 那两种写法是给模型和出站适配器用的中间产物，控制台上只会是一串号码。
func TestUserMemoryBufferResolvesMarkers(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	// 先让被 @ 的人和被引用的人各自建档，昵称才查得到。
	for _, seed := range []assistant.MessageEvent{
		{Kind: assistant.EventKindGroup, GroupID: "20001", UserID: "3129583166", SenderName: "小明", MessageID: "s1", RawMessage: "大家好", Time: 1_700_000_000},
		{Kind: assistant.EventKindGroup, GroupID: "20001", UserID: "10002", SenderName: "阿花", MessageID: "s2", RawMessage: "下周要去上海出差", Time: 1_700_000_010},
	} {
		if _, err := store.UpdateUserMemory(ctx, seed, assistant.UserMemoryUpdate{}); err != nil {
			t.Fatal(err)
		}
	}

	profile, err := store.UpdateUserMemory(ctx, assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "20001", UserID: "10003", SenderName: "阿强",
		MessageID: "m1", Time: 1_700_000_100,
		Segments: []assistant.MessageSegment{
			{Type: "reply", Data: map[string]string{"id": "s2"}},
			{Type: "at", Data: map[string]string{"qq": "3129583166"}},
			{Type: "text", Data: map[string]string{"text": "你也去吗"}},
		},
		Quoted: &assistant.QuotedMessage{
			MessageID: "s2", UserID: "10002",
			Segments: []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": "下周要去上海出差"}}},
		},
	}, assistant.UserMemoryUpdate{})
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Memories) != 1 {
		t.Fatalf("memories = %#v", profile.Memories)
	}
	want := "[回复 阿花：下周要去上海出差] @小明（3129583166） 你也去吗"
	if profile.Memories[0].Text != want {
		t.Fatalf("text = %q, want %q", profile.Memories[0].Text, want)
	}
}

// 被 @ 的人还没建档时照旧显示号码，不该因为查不到就把 @ 整个吞掉。
func TestUserMemoryBufferKeepsUnknownMentionID(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	profile, err := store.UpdateUserMemory(ctx, assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "20001", UserID: "10003", SenderName: "阿强",
		MessageID: "m1", Time: 1_700_000_100,
		Segments: []assistant.MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "99999"}},
			{Type: "text", Data: map[string]string{"text": "在吗"}},
		},
	}, assistant.UserMemoryUpdate{})
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Memories) != 1 || profile.Memories[0].Text != "@99999 在吗" {
		t.Fatalf("memories = %#v", profile.Memories)
	}
}

// 档案里没有昵称时 DisplayName 会退化成账号本身，不能照它渲染成「@10004（10004）」。
func TestUserMemoryBufferSkipsDegradedDisplayName(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if _, err := store.UpdateUserMemory(ctx, assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "20001", UserID: "10004", SenderName: "10004",
		MessageID: "s1", RawMessage: "哦", Time: 1_700_000_000,
	}, assistant.UserMemoryUpdate{}); err != nil {
		t.Fatal(err)
	}
	profile, err := store.UpdateUserMemory(ctx, assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "20001", UserID: "10003", SenderName: "阿强",
		MessageID: "m1", Time: 1_700_000_100,
		Segments: []assistant.MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "10004"}},
			{Type: "text", Data: map[string]string{"text": "看看"}},
		},
	}, assistant.UserMemoryUpdate{})
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Memories) != 1 || profile.Memories[0].Text != "@10004 看看" {
		t.Fatalf("memories = %#v", profile.Memories)
	}
}
