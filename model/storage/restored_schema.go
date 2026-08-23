// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"

	"github.com/SuInk/diana/model/assistant"
)

func (s *SQLiteStore) migrateRestoredFeatures() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS message_events (
  id TEXT PRIMARY KEY,
  session TEXT NOT NULL,
  kind TEXT NOT NULL,
  group_id TEXT,
  user_id TEXT,
  message_id TEXT,
  sender_name TEXT,
  event_time INTEGER NOT NULL,
  text TEXT,
  payload TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS image_descriptions (
  content_sha256 TEXT PRIMARY KEY,
  description TEXT NOT NULL,
  source_session TEXT,
  source_message_id TEXT,
  source TEXT,
  version TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS image_recognitions (
  cache_key TEXT PRIMARY KEY,
  content_sha256 TEXT NOT NULL,
  kind TEXT NOT NULL,
  backend TEXT NOT NULL,
  model TEXT,
  text TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS voice_blobs (
  audio_sha256 TEXT PRIMARY KEY,
  body BLOB NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS voice_transcripts (
  cache_key TEXT PRIMARY KEY,
  audio_sha256 TEXT NOT NULL,
  backend TEXT NOT NULL,
  model TEXT NOT NULL,
  language TEXT,
  transcript TEXT NOT NULL,
  duration_ms INTEGER,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS semantic_reference_cache (
  cache_key TEXT PRIMARY KEY,
  message_ids TEXT NOT NULL,
  confidence REAL NOT NULL,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_semantic_reference_cache_expiry
  ON semantic_reference_cache(expires_at);

CREATE TABLE IF NOT EXISTS user_profiles (
  user_id TEXT PRIMARY KEY,
  display_name TEXT,
  favorability INTEGER NOT NULL,
  message_count INTEGER NOT NULL,
  memories TEXT NOT NULL,
  last_seen_at TEXT,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS user_favorability_changes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id TEXT NOT NULL,
  delta INTEGER NOT NULL,
  before_score INTEGER NOT NULL,
  after_score INTEGER NOT NULL,
  source TEXT NOT NULL,
  reason TEXT,
  operator_id TEXT,
  group_id TEXT,
  message_id TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS memory_items (
  id TEXT PRIMARY KEY,
  scope_key TEXT NOT NULL,
  subject_user_id TEXT NOT NULL,
  subject_name TEXT,
  memory_key TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('fact', 'preference', 'episode', 'instruction', 'summary', 'thread')),
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

CREATE TABLE IF NOT EXISTS memory_sources (
  memory_id TEXT NOT NULL,
  source_session TEXT NOT NULL,
  source_group_id TEXT,
  source_message_id TEXT NOT NULL,
  source_event_time INTEGER NOT NULL,
  source_type TEXT NOT NULL,
  evidence TEXT,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(memory_id, source_session, source_message_id),
  FOREIGN KEY(memory_id) REFERENCES memory_items(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS memory_jobs (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('event', 'summary')),
  session TEXT NOT NULL,
  payload TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'done')),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  available_at INTEGER NOT NULL,
  lease_owner TEXT,
  lease_until INTEGER,
  last_error TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  completed_at INTEGER
);

CREATE TABLE IF NOT EXISTS outbound_delivery_steps (
  turn_id TEXT NOT NULL,
  step_key TEXT NOT NULL,
  message_id TEXT,
  delivered_at INTEGER NOT NULL,
  PRIMARY KEY (turn_id, step_key)
);

CREATE TABLE IF NOT EXISTS inbound_event_subtasks (
  event_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  phase TEXT NOT NULL,
  completed INTEGER NOT NULL DEFAULT 0,
  total INTEGER NOT NULL DEFAULT 0,
  detail TEXT,
  error TEXT,
  started_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  finished_at INTEGER,
  PRIMARY KEY (event_id, task_id)
);

CREATE INDEX IF NOT EXISTS idx_inbound_event_subtasks_event
  ON inbound_event_subtasks (event_id, started_at);

CREATE TABLE IF NOT EXISTS inbound_events (
  id TEXT PRIMARY KEY,
  session TEXT NOT NULL,
  kind TEXT NOT NULL,
  group_id TEXT,
  user_id TEXT,
  message_id TEXT,
  event_time INTEGER NOT NULL,
  payload TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'done')),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  available_at INTEGER NOT NULL,
  lease_owner TEXT,
  lease_until INTEGER,
  superseded_by TEXT,
  outcome TEXT,
  decision TEXT,
  decision_reason TEXT,
  reply_text TEXT,
  processing_error TEXT,
  duration_ms INTEGER,
  delivery_stage TEXT,
  outbound_message_id TEXT,
  reply_generated_at INTEGER,
  send_attempted_at INTEGER,
  send_acked_at INTEGER,
  self_echo_at INTEGER,
  delivery_error TEXT,
  -- 这一轮实际发出去的内容概览（几条消息、几张图、几个视频、有没有转发卡片）。
  -- reply_text 只是文本，发媒体不发文字时它是空的。
  delivery_json TEXT,
  last_error TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  completed_at INTEGER
);

CREATE TABLE IF NOT EXISTS repository_issue_drafts (
  id TEXT PRIMARY KEY,
  group_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending', 'created', 'cancelled')),
  payload TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_message_events_session_time ON message_events(session, event_time DESC, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_message_events_kind_group_time ON message_events(kind, group_id, event_time DESC, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_message_events_text ON message_events(text);
CREATE INDEX IF NOT EXISTS idx_message_events_user_time ON message_events(user_id, event_time DESC);
CREATE INDEX IF NOT EXISTS idx_image_descriptions_source_message ON image_descriptions(source_session, source_message_id);
CREATE INDEX IF NOT EXISTS idx_voice_transcripts_audio ON voice_transcripts(audio_sha256, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_image_recognitions_content ON image_recognitions(content_sha256, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_image_recognitions_created_at ON image_recognitions(created_at DESC);

-- Raw inline audio is only a durable queue payload. Once a transcript exists,
-- history and long-term memory use the text and source reference instead.
DELETE FROM voice_blobs
WHERE audio_sha256 IN (SELECT DISTINCT audio_sha256 FROM voice_transcripts);
CREATE INDEX IF NOT EXISTS idx_user_profiles_updated_at ON user_profiles(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_favorability_changes_user_id ON user_favorability_changes(user_id, id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_items_active_key ON memory_items(scope_key, subject_user_id, memory_key) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_memory_items_subject_active ON memory_items(subject_user_id, status, importance DESC, confidence DESC, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_memory_items_scope_active ON memory_items(scope_key, status, importance DESC, confidence DESC, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_memory_sources_message ON memory_sources(source_session, source_message_id);
CREATE INDEX IF NOT EXISTS idx_memory_jobs_claim ON memory_jobs(status, available_at, created_at, id);
CREATE INDEX IF NOT EXISTS idx_memory_jobs_lease ON memory_jobs(status, lease_until);
CREATE INDEX IF NOT EXISTS idx_inbound_events_claim_time ON inbound_events(status, available_at, event_time, created_at, id);
CREATE INDEX IF NOT EXISTS idx_inbound_events_lease ON inbound_events(status, lease_until);
CREATE INDEX IF NOT EXISTS idx_inbound_events_session_lease ON inbound_events(status, session, lease_until);
CREATE INDEX IF NOT EXISTS idx_inbound_events_session_time ON inbound_events(session, event_time, created_at, id);
CREATE INDEX IF NOT EXISTS idx_inbound_events_group_time ON inbound_events(group_id, event_time DESC);
CREATE INDEX IF NOT EXISTS idx_inbound_events_outbound_message ON inbound_events(outbound_message_id) WHERE outbound_message_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_inbound_events_priority_claim ON inbound_events(status, available_at, priority DESC, event_time, created_at, id);
CREATE INDEX IF NOT EXISTS idx_repository_issue_drafts_group_status_time ON repository_issue_drafts(group_id, status, created_at DESC);
`)
	if err != nil {
		return err
	}
	if err := s.backfillRecallNoticeAudits(); err != nil {
		return err
	}
	return s.migrateGlossary()
}

func (s *SQLiteStore) backfillRecallNoticeAudits() error {
	_, err := s.db.Exec(`
INSERT OR IGNORE INTO inbound_events (
  id, session, kind, group_id, user_id, message_id, event_time, payload,
  priority, status, attempts, available_at, outcome, decision, decision_reason,
  created_at, updated_at, completed_at
)
SELECT
  id, session, kind, group_id, user_id, message_id, event_time, payload,
  0, 'done', 0, event_time * 1000000000,
  'notice_' || json_extract(payload, '$.sub_type'), 'notice', '已记录平台通知',
  event_time * 1000000000, event_time * 1000000000, event_time * 1000000000
FROM message_events
WHERE kind = 'notice'
  AND json_extract(payload, '$.sub_type') IN ('group_recall', 'friend_recall')
`)
	if err != nil {
		return fmt.Errorf("backfill recall notice audits: %w", err)
	}
	return nil
}

func (s *SQLiteStore) LoadReplySuppressions(ctx context.Context) ([]assistant.ReplySuppression, bool, error) {
	var items []assistant.ReplySuppression
	ok, err := s.loadJSON(ctx, replySuppressionsKey, &items)
	return items, ok, err
}

func (s *SQLiteStore) SaveReplySuppressions(ctx context.Context, items []assistant.ReplySuppression) error {
	return s.saveJSON(ctx, replySuppressionsKey, items)
}

// memoryItemsKindCheck 是 memory_items.kind 当前允许的取值。加类型必须同时改这里、
// 建表语句和 normalizeMemoryCandidate 的白名单，否则新类型会在写入时被 CHECK 拒掉。
const memoryItemsKindCheck = `CHECK (kind IN ('fact', 'preference', 'episode', 'instruction', 'summary', 'thread'))`
