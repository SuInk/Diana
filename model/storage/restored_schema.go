// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"log"
	"strings"

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
  bot_profile_id TEXT NOT NULL DEFAULT '',
  user_id TEXT NOT NULL,
  display_name TEXT,
  favorability INTEGER NOT NULL,
  message_count INTEGER NOT NULL,
  memories TEXT NOT NULL,
  last_seen_at TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (bot_profile_id, user_id)
);

CREATE TABLE IF NOT EXISTS user_favorability_changes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  bot_profile_id TEXT NOT NULL DEFAULT '',
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
  profile_id TEXT,
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
	if err := s.addInboundEventProfileColumn(); err != nil {
		return err
	}
	if err := s.migrateUserProfilesToBotScope(); err != nil {
		return err
	}
	if err := s.backfillRecallNoticeAudits(); err != nil {
		return err
	}
	return s.migrateGlossary()
}

// addInboundEventProfileColumn 给事件表补上机器人维度。
//
// 事件本来就带 profile_id，但只躺在 payload 的 JSON 里，SQL 过滤不到：控制台想
// 「只看这台机器人的事件」就得把整段历史读出来在内存里筛。这里补一列，并从已有
// payload 里回填一次，老库升级后历史事件同样可以按机器人过滤。
func (s *SQLiteStore) addInboundEventProfileColumn() error {
	has, err := s.hasColumn("inbound_events", "profile_id")
	if err != nil || has {
		return err
	}
	if _, err := s.db.Exec(`ALTER TABLE inbound_events ADD COLUMN profile_id TEXT`); err != nil {
		return err
	}
	// json_extract 在 SQLite 3.38 之后随 JSON1 默认可用；万一这个构建里没有，
	// 回填失败不该挡住启动——新事件仍会写入正确的列。
	if _, err := s.db.Exec(`
UPDATE inbound_events
SET profile_id = TRIM(COALESCE(json_extract(payload, '$.profile_id'), ''))
WHERE COALESCE(profile_id, '') = ''
`); err != nil {
		log.Printf("storage: backfill inbound event profile_id skipped: %v", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_inbound_events_profile_time ON inbound_events(profile_id, event_time DESC)`); err != nil {
		return err
	}
	return nil
}

// hasColumn 判断表里有没有这一列，用于幂等地补列。
func (s *SQLiteStore) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}

// migrateUserProfilesToBotScope 把人员画像和好感度改成按机器人各记一份。
//
// 一个人在 QQ 上和在 Telegram 上面对的是两台机器人、两种关系，共用一份好感度和
// 长期记忆会让「Telegram 上的它记得你在 QQ 说过什么」，那不是同一个角色。
//
// 已有数据归给迁移时的当前配置档：历史上通常只有一台在跑，这些记忆本来就是它攒
// 下来的。其余机器人从空白开始，而不是复制一份——复制等于凭空给每台都编造一段
// 它没经历过的关系。
func (s *SQLiteStore) migrateUserProfilesToBotScope() error {
	has, err := s.hasColumn("user_profiles", "bot_profile_id")
	if err != nil {
		return err
	}
	owner := s.currentBotProfileID()
	if !has {
		if err := s.rebuildUserProfilesWithBotScope(owner); err != nil {
			return err
		}
	}
	changesHas, err := s.hasColumn("user_favorability_changes", "bot_profile_id")
	if err != nil {
		return err
	}
	if !changesHas {
		if _, err := s.db.Exec(`ALTER TABLE user_favorability_changes ADD COLUMN bot_profile_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE user_favorability_changes SET bot_profile_id = ? WHERE COALESCE(bot_profile_id, '') = ''`, owner); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_user_favorability_changes_scope ON user_favorability_changes(bot_profile_id, user_id, id DESC)`); err != nil {
		return err
	}
	return nil
}

// rebuildUserProfilesWithBotScope 重建表来换主键：SQLite 改不了已有表的主键，
// 只能新建、搬数据、换名。
func (s *SQLiteStore) rebuildUserProfilesWithBotScope(owner string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS user_profiles_scoped (
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
		return err
	}
	if _, err := tx.Exec(`
INSERT OR REPLACE INTO user_profiles_scoped (bot_profile_id, user_id, display_name, favorability, message_count, memories, last_seen_at, updated_at)
SELECT ?, user_id, display_name, favorability, message_count, memories, last_seen_at, updated_at FROM user_profiles
`, owner); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE user_profiles`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE user_profiles_scoped RENAME TO user_profiles`); err != nil {
		return err
	}
	return tx.Commit()
}

// currentBotProfileID 读当前生效的机器人配置档 ID，供迁移决定历史数据的归属。
// 读不到（全新库、或还没配过机器人）就归到空作用域，后续第一次写入会带上真实 ID。
func (s *SQLiteStore) currentBotProfileID() string {
	set, ok, err := s.LoadBotProfiles(context.Background())
	if err != nil || !ok {
		return ""
	}
	if current, found := set.Current(); found {
		return strings.TrimSpace(current.ID)
	}
	return ""
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
