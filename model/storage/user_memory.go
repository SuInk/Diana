// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

const (
	defaultUserFavorability = 0
	ownerUserFavorability   = 100
	minUserFavorability     = -100
	maxUserFavorability     = 200
	maxUserMemoryItems      = 20
	maxUserMemoryTextRunes  = 180
)

// UpdateUserMemory updates one QQ user's long-term profile without calling the LLM.
func (s *SQLiteStore) UpdateUserMemory(ctx context.Context, event assistant.MessageEvent, update assistant.UserMemoryUpdate) (assistant.UserMemoryProfile, error) {
	var profile assistant.UserMemoryProfile
	if s == nil || s.db == nil {
		return profile, nil
	}
	s.userMemoryMu.Lock()
	defer s.userMemoryMu.Unlock()
	userID := strings.TrimSpace(event.UserID)
	if userID == "" {
		return profile, nil
	}

	profile, ok, err := s.GetUserMemory(ctx, userID)
	if err != nil {
		return assistant.UserMemoryProfile{}, err
	}
	if !ok {
		profile = assistant.UserMemoryProfile{
			UserID:       userID,
			Favorability: defaultUserFavorability,
			Memories:     []assistant.UserMemoryItem{},
		}
	}

	ownerID := strings.TrimSpace(update.OwnerID)
	if ownerID != "" && ownerID == userID && profile.Favorability < ownerUserFavorability {
		profile.Favorability = ownerUserFavorability
	}
	previousFavorability := profile.Favorability
	if name := strings.TrimSpace(event.SenderName); name != "" {
		profile.DisplayName = name
	}
	if update.SetFavorability != nil {
		profile.Favorability = clampUserFavorability(*update.SetFavorability, ownerID, userID)
	} else {
		profile.Favorability = clampUserFavorability(profile.Favorability+clampUserFavorabilityDelta(update.FavorabilityDelta), ownerID, userID)
	}
	if !update.Administrative {
		profile.MessageCount++
		profile.LastSeenAt = userMemoryEventTime(event)
		if item, ok := userMemoryItemFromEvent(event); ok {
			profile.Memories = appendUserMemory(profile.Memories, item)
		}
	}
	profile.UpdatedAt = time.Now().UTC()

	memories, err := json.Marshal(profile.Memories)
	if err != nil {
		return assistant.UserMemoryProfile{}, err
	}
	lastSeen := ""
	if !profile.LastSeenAt.IsZero() {
		lastSeen = profile.LastSeenAt.UTC().Format(time.RFC3339Nano)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return assistant.UserMemoryProfile{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
INSERT INTO user_profiles (user_id, display_name, favorability, message_count, memories, last_seen_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(user_id) DO UPDATE SET
  display_name=excluded.display_name,
  favorability=excluded.favorability,
  message_count=excluded.message_count,
  memories=excluded.memories,
  last_seen_at=excluded.last_seen_at,
  updated_at=excluded.updated_at
`, profile.UserID, profile.DisplayName, profile.Favorability, profile.MessageCount, string(memories), lastSeen, profile.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return assistant.UserMemoryProfile{}, err
	}
	if profile.Favorability != previousFavorability && favorabilityChangeRequested(update) {
		_, err = tx.ExecContext(ctx, `
INSERT INTO user_favorability_changes (
  user_id, delta, before_score, after_score, source, reason, operator_id, group_id, message_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, profile.UserID, profile.Favorability-previousFavorability, previousFavorability, profile.Favorability,
			favorabilityChangeSource(update), strings.TrimSpace(update.FavorabilityChangeReason),
			strings.TrimSpace(update.FavorabilityChangeOperator), strings.TrimSpace(event.GroupID),
			strings.TrimSpace(event.MessageID), profile.UpdatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return assistant.UserMemoryProfile{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return assistant.UserMemoryProfile{}, err
	}
	return profile, nil
}

// ListUserFavorabilityChanges returns the newest real score changes first.
func (s *SQLiteStore) ListUserFavorabilityChanges(ctx context.Context, userID string, limit int) ([]assistant.UserFavorabilityChange, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" || limit <= 0 {
		return []assistant.UserFavorabilityChange{}, nil
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, delta, before_score, after_score, source, reason, operator_id, group_id, message_id, created_at
FROM user_favorability_changes
WHERE user_id = ?
ORDER BY id DESC
LIMIT ?
`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := make([]assistant.UserFavorabilityChange, 0, limit)
	for rows.Next() {
		var change assistant.UserFavorabilityChange
		var reason, operatorID, groupID, messageID sql.NullString
		var createdAt string
		if err := rows.Scan(&change.ID, &change.UserID, &change.Delta, &change.Before, &change.After, &change.Source,
			&reason, &operatorID, &groupID, &messageID, &createdAt); err != nil {
			return nil, err
		}
		change.Reason = reason.String
		change.OperatorID = operatorID.String
		change.GroupID = groupID.String
		change.MessageID = messageID.String
		change.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

func favorabilityChangeSource(update assistant.UserMemoryUpdate) string {
	if source := strings.TrimSpace(update.FavorabilityChangeSource); source != "" {
		return source
	}
	if update.SetFavorability != nil {
		return "manual"
	}
	return "interaction"
}

func favorabilityChangeRequested(update assistant.UserMemoryUpdate) bool {
	return update.SetFavorability != nil || update.FavorabilityDelta != 0
}

// GetUserMemory loads one QQ user's long-term profile.
func (s *SQLiteStore) GetUserMemory(ctx context.Context, userID string) (assistant.UserMemoryProfile, bool, error) {
	var profile assistant.UserMemoryProfile
	if s == nil || s.db == nil {
		return profile, false, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return profile, false, nil
	}
	var displayName sql.NullString
	var memoriesRaw string
	var lastSeenRaw sql.NullString
	var updatedRaw sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT user_id, display_name, favorability, message_count, memories, last_seen_at, updated_at
FROM user_profiles
WHERE user_id = ?
`, userID).Scan(&profile.UserID, &displayName, &profile.Favorability, &profile.MessageCount, &memoriesRaw, &lastSeenRaw, &updatedRaw)
	if err == sql.ErrNoRows {
		return assistant.UserMemoryProfile{}, false, nil
	}
	if err != nil {
		return assistant.UserMemoryProfile{}, false, err
	}
	profile.DisplayName = displayName.String
	if strings.TrimSpace(memoriesRaw) != "" {
		if err := json.Unmarshal([]byte(memoriesRaw), &profile.Memories); err != nil {
			return assistant.UserMemoryProfile{}, false, err
		}
	}
	profile.LastSeenAt = parseUserProfileTime(lastSeenRaw)
	profile.UpdatedAt = parseUserProfileTime(updatedRaw)
	return profile, true, nil
}

func userMemoryEventTime(event assistant.MessageEvent) time.Time {
	if event.Time > 0 {
		return time.Unix(event.Time, 0).UTC()
	}
	return time.Now().UTC()
}

func userMemoryItemFromEvent(event assistant.MessageEvent) (assistant.UserMemoryItem, bool) {
	text := userMemoryEventText(event)
	if !usefulUserMemoryText(text) {
		return assistant.UserMemoryItem{}, false
	}
	return assistant.UserMemoryItem{
		Text:      truncateUserMemoryText(text),
		Source:    string(event.Kind),
		GroupID:   event.GroupID,
		MessageID: event.MessageID,
		At:        userMemoryEventTime(event),
	}, true
}

func userMemoryEventText(event assistant.MessageEvent) string {
	text := strings.TrimSpace(assistant.PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	return strings.Join(strings.Fields(text), " ")
}

func usefulUserMemoryText(text string) bool {
	text = strings.TrimSpace(text)
	if len([]rune(text)) < 2 {
		return false
	}
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return strings.Contains(lower, " ")
	}
	return true
}

func appendUserMemory(memories []assistant.UserMemoryItem, item assistant.UserMemoryItem) []assistant.UserMemoryItem {
	for _, existing := range memories {
		if existing.Text == item.Text && existing.GroupID == item.GroupID {
			return memories
		}
	}
	memories = append(memories, item)
	if len(memories) > maxUserMemoryItems {
		memories = memories[len(memories)-maxUserMemoryItems:]
	}
	return memories
}

func clampUserFavorabilityDelta(delta int) int {
	if delta < -3 {
		return -3
	}
	if delta > 3 {
		return 3
	}
	return delta
}

func clampUserFavorability(value int, ownerID string, userID string) int {
	minValue := minUserFavorability
	if ownerID != "" && ownerID == userID {
		minValue = ownerUserFavorability
	}
	if value < minValue {
		return minValue
	}
	if value > maxUserFavorability {
		return maxUserFavorability
	}
	return value
}

func truncateUserMemoryText(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxUserMemoryTextRunes {
		return string(runes)
	}
	return string(runes[:maxUserMemoryTextRunes]) + "..."
}

func parseUserProfileTime(value sql.NullString) time.Time {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
