package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

// AdminGroupHistoryQuery scopes durable group messages by their recorded bot
// identity. An empty ProfileID includes old records whose identity is unknown;
// those records must never be attributed to a selected bot.
type AdminGroupHistoryQuery struct {
	ProfileID, GroupID, Search string
	FromTime, ThroughTime      int64
	Limit, Offset              int
	OldestFirst                bool
}

type AdminGroupHistoryMessage struct {
	ProfileID     string `json:"profile_id"`
	Platform      string `json:"platform,omitempty"`
	GroupID       string `json:"group_id"`
	GroupName     string `json:"group_name,omitempty"`
	MessageID     string `json:"message_id"`
	Time          int64  `json:"event_time"`
	LocalTime     string `json:"local_time,omitempty"`
	UserID        string `json:"user_id"`
	SenderName    string `json:"sender_name,omitempty"`
	Outbound      bool   `json:"outbound,omitempty"`
	Text          string `json:"text"`
	SearchExtra   string `json:"search_extra,omitempty"`
	TextTruncated bool   `json:"text_truncated,omitempty"`
}

// SearchAdminGroupHistory reads bounded, paginated chat records, including
// persisted bot replies. It never returns private messages, notice events or
// the raw event payload, which may include unrelated platform metadata.
func (s *SQLiteStore) SearchAdminGroupHistory(ctx context.Context, q AdminGroupHistoryQuery) ([]AdminGroupHistoryMessage, int, error) {
	defer s.observeStorage(ctx, "SearchAdminGroupHistory", "read")()
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("群聊记录数据库不可用")
	}
	if q.FromTime < 0 || q.ThroughTime < q.FromTime {
		return nil, 0, fmt.Errorf("群聊记录时间范围无效")
	}
	limit := q.Limit
	if limit < 1 {
		limit = 20
	}
	limit = min(limit, 50)
	offset := max(0, q.Offset)
	where := `kind = ? AND group_id != '' AND event_time BETWEEN ? AND ?`
	args := []any{string(assistant.EventKindGroup), q.FromTime, q.ThroughTime}
	if profile := strings.TrimSpace(q.ProfileID); profile != "" {
		where += ` AND profile_id = ?`
		args = append(args, profile)
	}
	if group := strings.TrimSpace(q.GroupID); group != "" {
		where += ` AND group_id = ?`
		args = append(args, group)
	}
	if search := strings.TrimSpace(q.Search); search != "" {
		where += ` AND LOWER(COALESCE(text, '') || CHAR(10) || COALESCE(search_extra, '') || CHAR(10) || COALESCE(sender_name, '') || CHAR(10) || COALESCE(user_id, '') || CHAR(10) || COALESCE(message_id, '')) LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeMessageHistoryLike(strings.ToLower(search))+"%")
	}
	var total int
	if err := s.eventReader().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_events WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	sort := "newest"
	if q.OldestFirst {
		sort = "oldest"
	}
	rowArgs := append(append([]any(nil), args...), limit, offset)
	rows, err := s.eventReader().QueryContext(ctx, `SELECT COALESCE(profile_id, ''), group_id, COALESCE(message_id, ''), event_time, COALESCE(user_id, ''), COALESCE(sender_name, ''), COALESCE(text, ''), COALESCE(search_extra, ''), payload FROM message_events WHERE `+where+` ORDER BY `+historyChronologicalOrder(sort, "")+` LIMIT ? OFFSET ?`, rowArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]AdminGroupHistoryMessage, 0, min(limit, total))
	for rows.Next() {
		var item AdminGroupHistoryMessage
		var payload string
		if err := rows.Scan(&item.ProfileID, &item.GroupID, &item.MessageID, &item.Time, &item.UserID, &item.SenderName, &item.Text, &item.SearchExtra, &payload); err != nil {
			return nil, 0, err
		}
		// Read only the display fields; do not expose the full platform event.
		var display struct {
			Platform  string `json:"platform"`
			GroupName string `json:"group_name"`
			Outbound  bool   `json:"outbound"`
		}
		if err := json.Unmarshal([]byte(payload), &display); err != nil {
			return nil, 0, fmt.Errorf("decode group history: %w", err)
		}
		item.Platform, item.GroupName, item.Outbound = display.Platform, display.GroupName, display.Outbound
		items = append(items, item)
	}
	return items, total, rows.Err()
}
