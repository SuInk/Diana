// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func threadWriteRequest(content string, at time.Time, messageID string) assistant.MemoryWriteRequest {
	return assistant.MemoryWriteRequest{
		Session:         "group:123",
		EventKind:       assistant.EventKindGroup,
		GroupID:         "123",
		SourceMessageID: messageID,
		SourceEventTime: at,
		Candidates: []assistant.MemoryCandidate{{
			Action:        assistant.MemoryActionUpsert,
			Key:           assistant.ThreadMemoryKey("group:123"),
			Kind:          assistant.MemoryKindThread,
			Topic:         "会话状态",
			Content:       content,
			SourceType:    assistant.MemorySourceSummary,
			Confidence:    0.95,
			Importance:    0.8,
			Visibility:    assistant.MemoryVisibilityUser,
			RetentionDays: 7,
		}},
	}
}

func TestThreadMemoryRollsForwardAsSingleEntry(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "thread.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	written, err := store.ApplyMemoryCandidates(ctx, threadWriteRequest("正在排查上下文变短，已定位到 16K 被写死", time.Unix(100, 0), "m1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[0].Kind != assistant.MemoryKindThread {
		t.Fatalf("first thread write = %#v", written)
	}
	// 会话状态不跨会话，可见性必须被压回 session。
	if written[0].Visibility != assistant.MemoryVisibilitySession {
		t.Fatalf("thread visibility = %q, want session", written[0].Visibility)
	}

	written, err = store.ApplyMemoryCandidates(ctx, threadWriteRequest("迁移已提交，正在设计记忆分层", time.Unix(200, 0), "m2"))
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[0].Version != 2 || written[0].SupersedesID == "" {
		t.Fatalf("second thread write = %#v", written)
	}

	items, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		Session:       "group:123",
		GroupID:       "123",
		Now:           time.Unix(300, 0),
		MaxCandidates: 20,
		Kinds:         []assistant.MemoryKind{assistant.MemoryKindThread},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 滚动更新只留一条活的状态，否则常驻注入会出现互相矛盾的几份。
	if len(items) != 1 || !strings.Contains(items[0].Content, "记忆分层") {
		t.Fatalf("active thread entries = %#v", items)
	}
}

func TestThreadMemoryExcludedFromRetrieval(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "thread-exclude.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if _, err := store.ApplyMemoryCandidates(ctx, threadWriteRequest("正在讨论记忆分层方案", time.Unix(100, 0), "m1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
		SubjectUserID:   "10001",
		Session:         "group:123",
		EventKind:       assistant.EventKindGroup,
		GroupID:         "123",
		SourceMessageID: "m2",
		SourceEventTime: time.Unix(120, 0),
		Candidates: []assistant.MemoryCandidate{{
			Action: assistant.MemoryActionUpsert, Key: "preference.topic.memory",
			Kind: assistant.MemoryKindPreference, Topic: "话题偏好",
			Content: "用户关心记忆分层方案", SourceType: assistant.MemorySourceExplicit,
			Confidence: 0.95, Importance: 0.7,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	items, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		SubjectUserID: "10001",
		Session:       "group:123",
		GroupID:       "123",
		Text:          "记忆分层",
		Now:           time.Unix(300, 0),
		MaxCandidates: 20,
		ExcludeKinds:  []assistant.MemoryKind{assistant.MemoryKindThread},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Kind == assistant.MemoryKindThread {
			t.Fatalf("thread leaked into retrieval: %#v", item)
		}
	}
	if len(items) == 0 {
		t.Fatal("excluding thread also dropped ordinary memories")
	}
}

func TestMemoryKindMigrationWidensLegacyCheckConstraint(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-kind.db")

	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// 先写一条普通记忆，确认重建会保留既有数据。
	if _, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
		SubjectUserID:   "10001",
		Session:         "group:123",
		EventKind:       assistant.EventKindGroup,
		GroupID:         "123",
		SourceMessageID: "m1",
		SourceEventTime: time.Unix(100, 0),
		Candidates: []assistant.MemoryCandidate{{
			Action: assistant.MemoryActionUpsert, Key: "preference.food.spicy",
			Kind: assistant.MemoryKindPreference, Topic: "饮食偏好",
			Content: "用户偏好辣味食物", SourceType: assistant.MemorySourceExplicit,
			Confidence: 0.98, Importance: 0.72,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	// 把 CHECK 约束改回升级前的样子，模拟老库。
	if _, err := store.db.Exec(`
PRAGMA foreign_keys = OFF;
CREATE TABLE memory_items_legacy AS SELECT * FROM memory_items;
DROP TABLE memory_items;
CREATE TABLE memory_items (
  id TEXT PRIMARY KEY,
  scope_key TEXT NOT NULL,
  subject_user_id TEXT NOT NULL,
  subject_name TEXT,
  memory_key TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('fact', 'preference', 'episode', 'instruction', 'summary')),
  topic TEXT NOT NULL,
  entity TEXT,
  content TEXT NOT NULL,
  evidence TEXT,
  source_type TEXT NOT NULL CHECK (source_type IN ('explicit', 'inferred', 'summary')),
  source_session TEXT NOT NULL,
  source_group_id TEXT,
  source_message_id TEXT,
  source_event_time INTEGER NOT NULL,
  confidence REAL NOT NULL,
  importance REAL NOT NULL,
  visibility TEXT NOT NULL CHECK (visibility IN ('session', 'user')),
  sensitive INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER,
  last_verified_at INTEGER NOT NULL,
  version INTEGER NOT NULL DEFAULT 1,
  supersedes_id TEXT,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'superseded', 'forgotten')),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  FOREIGN KEY(supersedes_id) REFERENCES memory_items(id)
);
INSERT INTO memory_items SELECT * FROM memory_items_legacy;
DROP TABLE memory_items_legacy;
PRAGMA foreign_keys = ON;
`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// 重新打开触发迁移。
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen legacy store: %v", err)
	}
	defer func() { _ = store.Close() }()

	if _, err := store.ApplyMemoryCandidates(ctx, threadWriteRequest("正在验证迁移", time.Unix(200, 0), "m2")); err != nil {
		t.Fatalf("thread write after migration: %v", err)
	}
	items, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		SubjectUserID: "10001",
		Session:       "group:123",
		GroupID:       "123",
		Now:           time.Unix(300, 0),
		MaxCandidates: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[assistant.MemoryKind]bool{}
	for _, item := range items {
		kinds[item.Kind] = true
	}
	// 迁移既要放开新类型，也不能丢掉重建前的数据。
	if !kinds[assistant.MemoryKindThread] || !kinds[assistant.MemoryKindPreference] {
		t.Fatalf("kinds after migration = %#v", kinds)
	}
}

func TestTouchStructuredMemoriesRecordsRetrievalHit(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "touch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	written, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
		SubjectUserID:   "10001",
		Session:         "group:123",
		EventKind:       assistant.EventKindGroup,
		GroupID:         "123",
		SourceMessageID: "m1",
		SourceEventTime: time.Unix(1_000_000, 0),
		Candidates: []assistant.MemoryCandidate{{
			Action: assistant.MemoryActionUpsert, Key: "episode.2026-08.budget",
			Kind: assistant.MemoryKindEpisode, Topic: "上下文预算",
			Content: "讨论了上下文预算分层", SourceType: assistant.MemorySourceExplicit,
			Confidence: 0.95, Importance: 0.7,
		}},
	})
	if err != nil || len(written) != 1 {
		t.Fatalf("seed write: %#v err=%v", written, err)
	}
	before := written[0].LastVerifiedAt

	hitAt := time.Unix(2_000_000, 0)
	if err := store.TouchStructuredMemories(ctx, []string{written[0].ID}, hitAt); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListStructuredMemories(ctx, assistant.StructuredMemoryQuery{
		SubjectUserID: "10001", Session: "group:123", GroupID: "123",
		Now: hitAt, MaxCandidates: 10,
	})
	if err != nil || len(items) != 1 {
		t.Fatalf("reload: %#v err=%v", items, err)
	}
	if !items[0].LastVerifiedAt.After(before) {
		t.Fatalf("last_verified_at not advanced: before=%v after=%v", before, items[0].LastVerifiedAt)
	}
	// 被读一次不是内容变化，updated_at 不该跟着动。
	if items[0].UpdatedAt.Unix() == hitAt.Unix() {
		t.Fatalf("touch rewrote updated_at: %v", items[0].UpdatedAt)
	}
	// 空列表与空 ID 都不该报错，调用方是 fire-and-forget。
	if err := store.TouchStructuredMemories(ctx, nil, hitAt); err != nil {
		t.Fatalf("empty touch: %v", err)
	}
	if err := store.TouchStructuredMemories(ctx, []string{"  "}, hitAt); err != nil {
		t.Fatalf("blank touch: %v", err)
	}
}
