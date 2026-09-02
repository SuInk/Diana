// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// 恋爱状态要跟着档案一起落库：确立后能读回，分手后整列清空，普通互动不碰它。
func TestUserRomanceStatePersistsAcrossReads(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "romance.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	event := assistant.MessageEvent{Kind: assistant.EventKindPrivate, UserID: "10005", ProfileID: "bot"}
	since := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{
		Administrative: true,
		SetRomance:     &assistant.UserRomanceState{Active: true, Since: since, StartedBy: "user"},
	}); err != nil {
		t.Fatal(err)
	}

	profile, ok, err := store.GetUserMemory(ctx, "bot", "10005")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if profile.Romance == nil || !profile.Romance.Active || !profile.Romance.Since.Equal(since) || profile.Romance.StartedBy != "user" {
		t.Fatalf("romance = %#v", profile.Romance)
	}

	// 普通互动不碰恋爱状态。
	if _, err := store.UpdateUserMemory(ctx, assistant.MessageEvent{
		Kind: assistant.EventKindPrivate, UserID: "10005", ProfileID: "bot",
		Segments: []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": "今天也辛苦啦"}}},
	}, assistant.UserMemoryUpdate{FavorabilityDelta: 1}); err != nil {
		t.Fatal(err)
	}
	profile, _, err = store.GetUserMemory(ctx, "bot", "10005")
	if err != nil || profile.Romance == nil || !profile.Romance.Since.Equal(since) {
		t.Fatalf("romance after chat = %#v err=%v", profile.Romance, err)
	}

	// 列表接口也要带出来。
	profiles, _, err := store.ListUserMemories(ctx, "bot", "", 10, 0)
	if err != nil || len(profiles) != 1 || profiles[0].Romance == nil {
		t.Fatalf("list = %#v err=%v", profiles, err)
	}

	// 分手清空整条状态。
	if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{
		Administrative: true,
		SetRomance:     &assistant.UserRomanceState{Active: false},
	}); err != nil {
		t.Fatal(err)
	}
	profile, _, err = store.GetUserMemory(ctx, "bot", "10005")
	if err != nil || profile.Romance != nil {
		t.Fatalf("romance after breakup = %#v err=%v", profile.Romance, err)
	}
	// 分手不动好感度。
	if profile.Favorability != 1 {
		t.Fatalf("favorability = %d", profile.Favorability)
	}
}
