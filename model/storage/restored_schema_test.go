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

func TestMigrateGroupConfigsDeduplicatesLegacyScopeByLatestUpdate(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "group-scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	profile := assistant.DefaultBotConfig()
	profile.ID = "bot-a"
	if err := store.SaveBotProfiles(context.Background(), assistant.ProfileSet{ActiveID: profile.ID, Profiles: []assistant.BotConfig{profile}}); err != nil {
		t.Fatal(err)
	}
	oldAt := time.Now().Add(-time.Hour)
	newAt := time.Now()
	set := assistant.GroupConfigSet{Groups: []assistant.GroupConfig{
		{BotProfileID: profile.ID, GroupID: "123", Enabled: true, ReplyGate: &assistant.ReplyGate{UserAdmission: assistant.UserAdmissionWhitelist, AllowedUsers: []string{"owner"}}, UpdatedAt: oldAt},
		{GroupID: "123", Enabled: true, ReplyGate: nil, UpdatedAt: newAt},
	}}
	if err := store.SaveBotGroupConfigs(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateGroupConfigsToBotScope(); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.LoadBotGroupConfigs(context.Background())
	if err != nil || !ok || len(got.Groups) != 1 {
		t.Fatalf("groups=%#v ok=%v err=%v", got.Groups, ok, err)
	}
	if got.Groups[0].BotProfileID != profile.ID || got.Groups[0].ReplyGate != nil {
		t.Fatalf("latest legacy config did not win: %#v", got.Groups[0])
	}
}

// 建表语句必须一次到位。以前这些列和索引是靠启动时的 ALTER TABLE 补上的，
// 迁移删掉之后如果 DDL 里漏了任何一项，新库就会静默缺列，直到写入那一刻才炸。
func TestInboundEventsSchemaIsCompleteWithoutMigrations(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	rows, err := store.db.Query(`PRAGMA table_info(inbound_events)`)
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		columns[name] = true
	}
	_ = rows.Close()
	for _, want := range []string{
		"priority", "decision", "decision_reason", "reply_text", "processing_error",
		"duration_ms", "delivery_stage", "outbound_message_id", "reply_generated_at",
		"send_attempted_at", "send_acked_at", "self_echo_at", "delivery_error",
		"superseded_by", "delivery_json",
	} {
		if !columns[want] {
			t.Fatalf("inbound_events is missing column %q", want)
		}
	}

	for _, want := range []string{"idx_inbound_events_outbound_message", "idx_inbound_events_priority_claim"} {
		var name string
		if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name = ?`, want).Scan(&name); err != nil {
			t.Fatalf("index %q missing: %v", want, err)
		}
	}
}

// memory_items 的 kind 约束要直接允许 thread，不再靠整表重建放宽。
func TestMemoryItemsSchemaAllowsThreadKind(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory-kind.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if _, err := store.db.Exec(`
INSERT INTO memory_items (
  id, scope_key, subject_user_id, memory_key, kind, topic, content,
  source_type, source_session, source_event_time, confidence, importance,
  visibility, last_verified_at, created_at, updated_at
) VALUES (
  'm1', 'group:1', 'u1', 'note', 'thread', 'topic', 'note',
  'explicit', 'group:1', 1, 0.5, 0.5, 'session', 1, 1, 1
)`); err != nil {
		t.Fatalf("thread kind rejected by schema: %v", err)
	}
}

func TestThreadStatesSchemaIsPrivateAndVersioned(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "thread-state-schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for _, name := range []string{"thread_states", "idx_thread_states_active_scope", "idx_thread_states_active_expiry"} {
		var found string
		if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE name = ?`, name).Scan(&found); err != nil {
			t.Fatalf("schema object %q missing: %v", name, err)
		}
	}
}
