// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

// InboundEventDebugTrace returns the opt-in debug records correlated with one
// durable inbound event. The boolean reports whether the event itself exists.
func (s *SQLiteStore) InboundEventDebugTrace(ctx context.Context, eventID string) (string, []AppLogEntry, bool, error) {
	if s == nil || s.db == nil {
		return "", nil, false, nil
	}
	var messageID, kind, groupID, userID, payload string
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(message_id, ''), kind, COALESCE(group_id, ''), COALESCE(user_id, ''), payload
FROM inbound_events
WHERE id = ?
`, strings.TrimSpace(eventID)).Scan(&messageID, &kind, &groupID, &userID, &payload)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, false, nil
		}
		return "", nil, false, fmt.Errorf("find inbound event debug trace: %w", err)
	}
	if strings.TrimSpace(messageID) == "" {
		return messageID, []AppLogEntry{}, true, nil
	}
	var source assistant.MessageEvent
	_ = json.Unmarshal([]byte(payload), &source)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, kind, level, action, message, detail, actor, target, metadata, created_at
FROM app_logs
WHERE kind = ? AND action IN ('chatbot.debug_trace') AND target = ?
ORDER BY created_at ASC, id ASC
`, string(LogKindDebug), messageID)
	if err != nil {
		return "", nil, false, fmt.Errorf("list inbound event debug trace: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make([]AppLogEntry, 0, 16)
	for rows.Next() {
		entry, scanErr := scanLogEntry(rows)
		if scanErr != nil {
			return "", nil, false, scanErr
		}
		if debugMetadataString(entry.Metadata, "kind") != kind ||
			debugMetadataString(entry.Metadata, "group_id") != groupID ||
			debugMetadataString(entry.Metadata, "user_id") != userID ||
			debugMetadataString(entry.Metadata, "platform") != strings.TrimSpace(source.Platform) ||
			debugMetadataString(entry.Metadata, "profile_id") != strings.TrimSpace(source.ProfileID) {
			continue
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return "", nil, false, err
	}
	return messageID, entries, true, nil
}

func debugMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
