package storage

import (
	"context"
	"fmt"
	"time"

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

CREATE TABLE IF NOT EXISTS user_profiles (
  user_id TEXT PRIMARY KEY,
  display_name TEXT,
  favorability INTEGER NOT NULL,
  message_count INTEGER NOT NULL,
  memories TEXT NOT NULL,
  last_seen_at TEXT,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS memory_items (
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
  outcome TEXT,
  decision TEXT,
  decision_reason TEXT,
  reply_text TEXT,
  processing_error TEXT,
  duration_ms INTEGER,
  last_error TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  completed_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_message_events_session_time ON message_events(session, event_time DESC, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_message_events_kind_group_time ON message_events(kind, group_id, event_time DESC, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_message_events_text ON message_events(text);
CREATE INDEX IF NOT EXISTS idx_message_events_user_time ON message_events(user_id, event_time DESC);
CREATE INDEX IF NOT EXISTS idx_image_descriptions_source_message ON image_descriptions(source_session, source_message_id);
CREATE INDEX IF NOT EXISTS idx_user_profiles_updated_at ON user_profiles(updated_at DESC);
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
`)
	if err != nil {
		return err
	}
	if err := s.ensureInboundPriorityColumn(); err != nil {
		return err
	}
	if err := s.ensureInboundAuditColumns(); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_inbound_events_priority_claim ON inbound_events(status, available_at, priority DESC, event_time, created_at, id)`); err != nil {
		return err
	}
	if err := s.backfillLegacyUserMemories(); err != nil {
		return err
	}

	now := time.Now().UTC().UnixNano()
	cutoff := time.Now().Add(-assistant.InboundReplayWindow).Unix()
	_, err = s.db.Exec(`
	UPDATE inbound_events
	SET status = 'pending', available_at = ?, lease_owner = NULL, lease_until = NULL,
	    outcome = NULL, decision = NULL, decision_reason = NULL, reply_text = NULL,
	    processing_error = NULL, duration_ms = NULL, last_error = NULL,
	    completed_at = NULL, updated_at = ?
WHERE status = 'done' AND outcome = 'ignored_stale' AND event_time >= ?
`, now, now, cutoff)
	return err
}

func (s *SQLiteStore) ensureInboundPriorityColumn() error {
	rows, err := s.db.Query(`PRAGMA table_info(inbound_events)`)
	if err != nil {
		return fmt.Errorf("inspect inbound queue schema: %w", err)
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("inspect inbound queue column: %w", err)
		}
		if name == "priority" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate inbound queue schema: %w", err)
	}
	if found {
		return nil
	}
	if _, err := s.db.Exec(`ALTER TABLE inbound_events ADD COLUMN priority INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("add inbound queue priority: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ensureInboundAuditColumns() error {
	rows, err := s.db.Query(`PRAGMA table_info(inbound_events)`)
	if err != nil {
		return fmt.Errorf("inspect inbound audit schema: %w", err)
	}
	defer func() { _ = rows.Close() }()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("inspect inbound audit column: %w", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate inbound audit schema: %w", err)
	}
	columns := []struct {
		name       string
		definition string
	}{
		{name: "decision", definition: "TEXT"},
		{name: "decision_reason", definition: "TEXT"},
		{name: "reply_text", definition: "TEXT"},
		{name: "processing_error", definition: "TEXT"},
		{name: "duration_ms", definition: "INTEGER"},
	}
	for _, column := range columns {
		if found[column.name] {
			continue
		}
		query := fmt.Sprintf("ALTER TABLE inbound_events ADD COLUMN %s %s", column.name, column.definition)
		if _, err := s.db.Exec(query); err != nil {
			return fmt.Errorf("add inbound audit column %s: %w", column.name, err)
		}
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
