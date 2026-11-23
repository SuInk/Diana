// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

var (
	_ assistant.AgentToolCallStore  = (*SQLiteStore)(nil)
	_ assistant.SessionSubtaskStore = (*SQLiteStore)(nil)
)

// agent_tool_calls 记每一次实际执行过的工具调用，给 tasks 工具按会话查（见
// model/assistant/tool_call_log.go）。每条回复可能有好几条，和日志一样按天清理。
const agentToolCallsSchema = `
CREATE TABLE IF NOT EXISTS agent_tool_calls (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session TEXT NOT NULL,
  profile_id TEXT NOT NULL DEFAULT '',
  event_id TEXT NOT NULL DEFAULT '',
  message_id TEXT NOT NULL DEFAULT '',
  user_id TEXT NOT NULL DEFAULT '',
  tool TEXT NOT NULL,
  input TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_tool_calls_session ON agent_tool_calls(session, profile_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_agent_tool_calls_created ON agent_tool_calls(created_at);
`

func (s *SQLiteStore) migrateAgentToolCalls() error {
	if _, err := s.db.Exec(agentToolCallsSchema); err != nil {
		return fmt.Errorf("create agent tool calls schema: %w", err)
	}
	// 2026-11 加：后台任务要做什么（生图提示词、调研目标），tasks 工具要给模型看。
	has, err := s.hasColumn("inbound_event_subtasks", "request")
	if err != nil {
		return err
	}
	if !has {
		if _, err := s.db.Exec(`ALTER TABLE inbound_event_subtasks ADD COLUMN request TEXT`); err != nil {
			return fmt.Errorf("add inbound event subtask request: %w", err)
		}
	}
	return nil
}

// RecordAgentToolCall 记下一次工具调用。
func (s *SQLiteStore) RecordAgentToolCall(ctx context.Context, record assistant.AgentToolCallRecord) error {
	defer s.observeStorage(ctx, "RecordAgentToolCall", "write")()
	if s == nil || s.db == nil {
		return nil
	}
	session, tool := strings.TrimSpace(record.Session), strings.TrimSpace(record.Tool)
	if session == "" || tool == "" {
		return nil
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO agent_tool_calls (session, profile_id, event_id, message_id, user_id, tool, input, error, duration_ms, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session, strings.TrimSpace(record.ProfileID), strings.TrimSpace(record.EventID), strings.TrimSpace(record.MessageID),
		strings.TrimSpace(record.UserID), tool, record.Input, strings.TrimSpace(record.Error), record.DurationMS, createdAt.UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("record agent tool call: %w", err)
	}
	return nil
}

// ListAgentToolCalls 按 ID 倒序列出会话里的工具调用。
func (s *SQLiteStore) ListAgentToolCalls(ctx context.Context, query assistant.AgentToolCallQuery) ([]assistant.AgentToolCallRecord, error) {
	defer s.observeStorage(ctx, "ListAgentToolCalls", "read")()
	if s == nil || s.db == nil || strings.TrimSpace(query.Session) == "" {
		return nil, nil
	}
	where := []string{"session = ?", "profile_id = ?"}
	args := []any{strings.TrimSpace(query.Session), strings.TrimSpace(query.ProfileID)}
	if tool := strings.TrimSpace(query.Tool); tool != "" {
		where = append(where, "tool = ?")
		args = append(args, tool)
	}
	if query.BeforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, query.BeforeID)
	}
	args = append(args, max(query.Limit, 1))
	rows, err := s.db.QueryContext(ctx, `
SELECT id, session, profile_id, event_id, message_id, user_id, tool, input, error, duration_ms, created_at
FROM agent_tool_calls
WHERE `+strings.Join(where, " AND ")+`
ORDER BY id DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list agent tool calls: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []assistant.AgentToolCallRecord
	for rows.Next() {
		var record assistant.AgentToolCallRecord
		var createdAt int64
		if err := rows.Scan(&record.ID, &record.Session, &record.ProfileID, &record.EventID, &record.MessageID, &record.UserID,
			&record.Tool, &record.Input, &record.Error, &record.DurationMS, &createdAt); err != nil {
			return nil, fmt.Errorf("scan agent tool call: %w", err)
		}
		record.CreatedAt = time.Unix(0, createdAt).UTC()
		records = append(records, record)
	}
	return records, rows.Err()
}

// PruneAgentToolCalls 删掉早于 createdBefore 的工具调用，分批删免得长时间占写锁。
func (s *SQLiteStore) PruneAgentToolCalls(ctx context.Context, createdBefore time.Time) (int64, error) {
	if s == nil || s.db == nil || createdBefore.IsZero() {
		return 0, nil
	}
	cutoff := createdBefore.UTC().UnixNano()
	var deleted int64
	for {
		result, err := s.db.ExecContext(ctx, `DELETE FROM agent_tool_calls WHERE id IN (
SELECT id FROM agent_tool_calls WHERE created_at < ? ORDER BY created_at LIMIT 500)`, cutoff)
		if err != nil {
			return deleted, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return deleted, err
		}
		deleted += count
		if count < 500 {
			return deleted, nil
		}
	}
}

// ListSessionSubtasks 按开始时间倒序列出会话里的后台任务。子任务表只挂着入站事件 ID，
// 会话和机器人从 inbound_events 取。
func (s *SQLiteStore) ListSessionSubtasks(ctx context.Context, query assistant.SessionSubtaskQuery) ([]assistant.SessionSubtask, error) {
	defer s.observeStorage(ctx, "ListSessionSubtasks", "read")()
	if s == nil || s.db == nil || strings.TrimSpace(query.Session) == "" {
		return nil, nil
	}
	where := []string{"e.session = ?", "COALESCE(e.profile_id, '') = ?"}
	args := []any{strings.TrimSpace(query.Session), strings.TrimSpace(query.ProfileID)}
	if query.BeforeSeq > 0 {
		where = append(where, "t.rowid < ?")
		args = append(args, query.BeforeSeq)
	}
	args = append(args, max(query.Limit, 1))
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT t.rowid, t.event_id, COALESCE(e.message_id, ''), t.task_id, t.kind, t.name, COALESCE(t.request, ''), t.phase,
       t.completed, t.total, COALESCE(t.detail, ''), COALESCE(t.error, ''), t.started_at, t.updated_at, t.finished_at
FROM inbound_event_subtasks t
JOIN inbound_events e ON e.id = t.event_id
WHERE `+strings.Join(where, " AND ")+`
ORDER BY t.rowid DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list session subtasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []assistant.SessionSubtask
	for rows.Next() {
		var item assistant.SessionSubtask
		var startedAt, updatedAt int64
		var finishedAt sql.NullInt64
		if err := rows.Scan(&item.Seq, &item.EventID, &item.MessageID, &item.TaskID, &item.Kind, &item.Name, &item.Request, &item.Phase,
			&item.Completed, &item.Total, &item.Detail, &item.Error, &startedAt, &updatedAt, &finishedAt); err != nil {
			return nil, fmt.Errorf("scan session subtask: %w", err)
		}
		item.StartedAt = time.Unix(startedAt, 0)
		item.UpdatedAt = time.Unix(updatedAt, 0)
		if finishedAt.Valid {
			finished := time.Unix(finishedAt.Int64, 0)
			item.FinishedAt = &finished
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
