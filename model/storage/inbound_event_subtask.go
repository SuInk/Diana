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

// SaveInboundEventSubtask 落库或更新一条子任务记录。
//
// started_at 只在第一次写入时确定：后续的进度和完成更新不能把开始时间往后推，
// 否则事件页上的耗时会随着最后一次更新不断缩短。
func (s *SQLiteStore) SaveInboundEventSubtask(ctx context.Context, item assistant.InboundEventSubtask) error {
	if s == nil || s.db == nil {
		return nil
	}
	eventID := strings.TrimSpace(item.EventID)
	taskID := strings.TrimSpace(item.TaskID)
	if eventID == "" || taskID == "" {
		return nil
	}
	var finishedAt any
	if item.FinishedAt != nil && !item.FinishedAt.IsZero() {
		finishedAt = item.FinishedAt.Unix()
	}
	updatedAt := item.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	startedAt := item.StartedAt
	if startedAt.IsZero() {
		startedAt = updatedAt
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO inbound_event_subtasks
  (event_id, task_id, kind, name, phase, completed, total, detail, error, started_at, updated_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(event_id, task_id) DO UPDATE SET
  kind = excluded.kind,
  name = excluded.name,
  phase = excluded.phase,
  completed = excluded.completed,
  total = excluded.total,
  detail = CASE WHEN TRIM(COALESCE(excluded.detail, '')) = '' THEN inbound_event_subtasks.detail ELSE excluded.detail END,
  error = CASE WHEN TRIM(COALESCE(excluded.error, '')) = '' THEN inbound_event_subtasks.error ELSE excluded.error END,
  updated_at = excluded.updated_at,
  finished_at = COALESCE(excluded.finished_at, inbound_event_subtasks.finished_at)
`,
		eventID, taskID, strings.TrimSpace(item.Kind), strings.TrimSpace(item.Name), strings.TrimSpace(item.Phase),
		item.Completed, item.Total, strings.TrimSpace(item.Detail), strings.TrimSpace(item.Error),
		startedAt.Unix(), updatedAt.Unix(), finishedAt,
	)
	if err != nil {
		return fmt.Errorf("save inbound event subtask: %w", err)
	}
	return nil
}

// LoadInboundEventSubtasks 按事件 ID 批量取子任务，返回值按事件分组。
// 事件列表一次要展示几十条，逐条查会把列表接口拖成 N+1。
func (s *SQLiteStore) LoadInboundEventSubtasks(ctx context.Context, eventIDs []string) (map[string][]assistant.InboundEventSubtask, error) {
	if s == nil || s.db == nil || len(eventIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, 0, len(eventIDs))
	args := make([]any, 0, len(eventIDs))
	for _, id := range eventIDs {
		if id = strings.TrimSpace(id); id != "" {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
	}
	if len(args) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT event_id, task_id, kind, name, phase, completed, total,
       COALESCE(detail, ''), COALESCE(error, ''), started_at, updated_at, finished_at
FROM inbound_event_subtasks
WHERE event_id IN (`+strings.Join(placeholders, ",")+`)
ORDER BY started_at ASC, task_id ASC
`, args...)
	if err != nil {
		return nil, fmt.Errorf("load inbound event subtasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string][]assistant.InboundEventSubtask)
	for rows.Next() {
		var item assistant.InboundEventSubtask
		var startedAt, updatedAt int64
		var finishedAt sql.NullInt64
		if err := rows.Scan(
			&item.EventID, &item.TaskID, &item.Kind, &item.Name, &item.Phase,
			&item.Completed, &item.Total, &item.Detail, &item.Error,
			&startedAt, &updatedAt, &finishedAt,
		); err != nil {
			return nil, fmt.Errorf("scan inbound event subtask: %w", err)
		}
		item.StartedAt = time.Unix(startedAt, 0)
		item.UpdatedAt = time.Unix(updatedAt, 0)
		if finishedAt.Valid {
			finished := time.Unix(finishedAt.Int64, 0)
			item.FinishedAt = &finished
		}
		out[item.EventID] = append(out[item.EventID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbound event subtasks: %w", err)
	}
	return out, nil
}
