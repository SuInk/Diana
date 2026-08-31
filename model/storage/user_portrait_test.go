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

func TestUserPortraitRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "portrait.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	event := assistant.MessageEvent{Kind: assistant.EventKindPrivate, ProfileID: "bot", UserID: "u1", SenderName: "Alice"}
	now := time.Now()
	if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{
		Administrative: true,
		PortraitTraits: []assistant.UserPortraitTrait{
			{Field: assistant.PortraitFieldResidence, Value: "住在杭州", Source: assistant.PortraitSourceStated, Confidence: 0.98, UpdatedAt: now},
			{Field: assistant.PortraitFieldOccupation, Value: "做后端开发", Source: assistant.PortraitSourceStated, Confidence: 0.95, UpdatedAt: now},
		},
	}); err != nil {
		t.Fatal(err)
	}

	profile, ok, err := store.GetUserMemory(ctx, "bot", "u1")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(profile.Portrait) != 2 || profile.Portrait[0].Value != "住在杭州" || profile.Portrait[0].Label != "居住地点" {
		t.Fatalf("portrait = %#v", profile.Portrait)
	}

	// 搬家：容量为 1 的栏被新值顶掉，不是并排留着两个城市。
	if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{
		Administrative: true,
		PortraitTraits: []assistant.UserPortraitTrait{
			{Field: assistant.PortraitFieldResidence, Value: "住在上海", Source: assistant.PortraitSourceStated, Confidence: 0.97, UpdatedAt: now.Add(time.Hour)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	profile, _, err = store.GetUserMemory(ctx, "bot", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Portrait) != 2 || profile.Portrait[0].Value != "住在上海" {
		t.Fatalf("portrait after moving = %#v", profile.Portrait)
	}

	// 列表接口也要能读出画像，控制台才数得出条数。
	profiles, _, err := store.ListUserMemories(ctx, "bot", "", 10, 0)
	if err != nil || len(profiles) != 1 || len(profiles[0].Portrait) != 2 {
		t.Fatalf("listed = %#v err=%v", profiles, err)
	}

	if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{
		Administrative:   true,
		PortraitRemovals: []assistant.UserPortraitField{assistant.PortraitFieldResidence},
	}); err != nil {
		t.Fatal(err)
	}
	profile, _, err = store.GetUserMemory(ctx, "bot", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Portrait) != 1 || profile.Portrait[0].Field != assistant.PortraitFieldOccupation {
		t.Fatalf("portrait after forgetting = %#v", profile.Portrait)
	}
}

// 普通互动不该动画像：它只在评估或用户明确要求时才被写。
func TestUserPortraitSurvivesOrdinaryInteractions(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "portrait-keep.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	event := assistant.MessageEvent{
		Kind:      assistant.EventKindPrivate,
		ProfileID: "bot",
		UserID:    "u1",
		Segments:  []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": "今天天气不错"}}},
	}
	if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{
		Administrative: true,
		PortraitTraits: []assistant.UserPortraitTrait{
			{Field: assistant.PortraitFieldHabit, Value: "习惯早睡", Source: assistant.PortraitSourceStated, Confidence: 0.9},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{FavorabilityDelta: 1}); err != nil {
		t.Fatal(err)
	}
	profile, _, err := store.GetUserMemory(ctx, "bot", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Portrait) != 1 || profile.MessageCount != 1 {
		t.Fatalf("profile = %#v", profile)
	}
}

// 老库升级：补上画像列，历史好感度和记忆原样留着，不凭空编一份画像。
func TestUserProfilePortraitColumnMigration(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "portrait-migrate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if _, err := store.db.ExecContext(ctx, `DROP TABLE user_profiles`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
CREATE TABLE user_profiles (
  bot_profile_id TEXT NOT NULL DEFAULT '',
  user_id TEXT NOT NULL,
  display_name TEXT,
  favorability INTEGER NOT NULL,
  message_count INTEGER NOT NULL,
  memories TEXT NOT NULL,
  last_seen_at TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (bot_profile_id, user_id)
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO user_profiles (bot_profile_id, user_id, display_name, favorability, message_count, memories, last_seen_at, updated_at)
VALUES ('bot', 'u1', '老用户', 42, 7, '[]', '', '2026-08-24T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	if err := store.addUserProfilePortraitColumn(); err != nil {
		t.Fatal(err)
	}
	// 幂等：升级过的库再启动一次不该报错。
	if err := store.addUserProfilePortraitColumn(); err != nil {
		t.Fatal(err)
	}

	profile, ok, err := store.GetUserMemory(ctx, "bot", "u1")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if profile.Favorability != 42 || len(profile.Portrait) != 0 {
		t.Fatalf("migrated profile = %#v", profile)
	}
	if _, err := store.UpdateUserMemory(ctx, assistant.MessageEvent{ProfileID: "bot", UserID: "u1"}, assistant.UserMemoryUpdate{
		Administrative: true,
		PortraitTraits: []assistant.UserPortraitTrait{
			{Field: assistant.PortraitFieldResidence, Value: "住在杭州", Source: assistant.PortraitSourceStated, Confidence: 0.95},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if profile, _, err = store.GetUserMemory(ctx, "bot", "u1"); err != nil || len(profile.Portrait) != 1 {
		t.Fatalf("portrait after migration = %#v err=%v", profile.Portrait, err)
	}
}
