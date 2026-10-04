// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestUserMemoryIgnoresLegacyRomanceColumn(t *testing.T) {
	for _, tc := range []struct{ name, state string }{
		{"active", `{"active":true,"since":"2026-08-01T12:00:00Z","started_by":"user"}`},
		{"invalid", "invalid legacy JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "users.db")
			store, err := NewSQLiteStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			if has, err := store.hasColumn("user_profiles", "romance"); err != nil || has {
				t.Fatalf("new database has removed column: has=%v err=%v", has, err)
			}
			if _, err := store.db.ExecContext(ctx, `ALTER TABLE user_profiles ADD COLUMN romance TEXT NOT NULL DEFAULT ''`); err != nil {
				t.Fatal(err)
			}
			event := assistant.MessageEvent{
				Kind: assistant.EventKindPrivate, ProfileID: "bot", UserID: "user", SenderName: "老用户",
				Segments: []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": "我住在杭州"}}},
			}
			score := 80
			before, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{
				SetFavorability: &score,
				PortraitTraits: []assistant.UserPortraitTrait{
					{Field: assistant.PortraitFieldResidence, Value: "住在杭州", Source: assistant.PortraitSourceStated, Confidence: 0.95},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE user_profiles SET romance = ? WHERE bot_profile_id = 'bot' AND user_id = 'user'`, tc.state); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = NewSQLiteStore(path)
			if err != nil {
				t.Fatal(err)
			}
			profile, ok, err := store.GetUserMemory(ctx, "bot", "user")
			if err != nil || !ok || !reflect.DeepEqual(profile, before) {
				t.Fatalf("legacy user changed on reopen: profile=%#v ok=%v err=%v", profile, ok, err)
			}
			profiles, total, err := store.ListUserMemories(ctx, "bot", "", 10, 0)
			if err != nil || total != 1 || len(profiles) != 1 || !reflect.DeepEqual(profiles[0], before) {
				t.Fatalf("legacy user list failed: profiles=%#v total=%d err=%v", profiles, total, err)
			}
			body, err := json.Marshal(profiles)
			if err != nil || strings.Contains(string(body), "romance") {
				t.Fatalf("legacy state leaked into user response: %s err=%v", body, err)
			}
			event.Segments[0].Data["text"] = "今天也辛苦啦"
			updated, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{FavorabilityDelta: 1})
			if err != nil || updated.Favorability != 81 || updated.MessageCount != 2 || len(updated.Memories) != 2 || !reflect.DeepEqual(updated.Portrait, before.Portrait) {
				t.Fatalf("legacy user update failed: profile=%#v err=%v", updated, err)
			}
			changes, err := store.ListUserFavorabilityChanges(ctx, "bot", "user", 10)
			if err != nil || len(changes) != 2 || changes[0].Delta != 1 || changes[0].Before != 80 || changes[0].After != 81 {
				t.Fatalf("favorability history changed: changes=%#v err=%v", changes, err)
			}
			if _, err := store.UpdateUserMemory(ctx, assistant.MessageEvent{ProfileID: "bot", UserID: "new-user"}, assistant.UserMemoryUpdate{}); err != nil {
				t.Fatalf("new user on legacy database: %v", err)
			}
		})
	}
}
