// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// InboundEventDebugTrace returns the opt-in debug records correlated with one
// durable inbound event. The boolean reports whether the event itself exists.
func (s *SQLiteStore) InboundEventDebugTrace(ctx context.Context, eventID string) (string, []AppLogEntry, bool, error) {
	trace, found, err := s.InboundEventDebugTraceDetail(ctx, eventID)
	return trace.MessageID, trace.Steps, found, err
}

// InboundEventTrace 是事件页「调试轨迹」要显示的内容。Steps 为空时 EmptyReason
// 说明真实原因，而不是笼统地让人去检查调试模式。
type InboundEventTrace struct {
	MessageID   string
	Steps       []AppLogEntry
	EmptyReason string
}

func (s *SQLiteStore) InboundEventDebugTraceDetail(ctx context.Context, eventID string) (InboundEventTrace, bool, error) {
	if s == nil || s.db == nil {
		return InboundEventTrace{}, false, nil
	}
	var messageID, kind, groupID, userID, payload string
	var row debugTraceEventRow
	err := s.eventReader().QueryRowContext(ctx, `
SELECT COALESCE(message_id, ''), kind, COALESCE(group_id, ''), COALESCE(user_id, ''), payload,
       status, COALESCE(outcome, ''), COALESCE(decision_reason, ''), created_at
FROM inbound_events
WHERE id = ?
`, strings.TrimSpace(eventID)).Scan(&messageID, &kind, &groupID, &userID, &payload,
		&row.status, &row.outcome, &row.decisionReason, &row.createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return InboundEventTrace{}, false, nil
		}
		return InboundEventTrace{}, false, fmt.Errorf("find inbound event debug trace: %w", err)
	}
	row.messageID = messageID
	if strings.TrimSpace(messageID) == "" {
		return InboundEventTrace{MessageID: messageID, Steps: []AppLogEntry{}, EmptyReason: s.debugTraceEmptyReason(row, false)}, true, nil
	}
	var source assistant.MessageEvent
	_ = json.Unmarshal([]byte(payload), &source)
	// 动作名历史上改过两轮（assistant -> chatbot -> diana），旧库里存的还是旧名字。
	// 这里把用过的名字都列上，否则升级之后历史调试轨迹会整段查不出来。
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT id, kind, level, action, message, detail, actor, target, metadata, created_at
FROM app_logs
WHERE kind = ? AND action IN ('debug_trace', 'cross_group_context') AND target = ?
ORDER BY created_at ASC, id ASC
`, string(LogKindDebug), messageID)
	if err != nil {
		return InboundEventTrace{}, false, fmt.Errorf("list inbound event debug trace: %w", err)
	}
	defer func() { _ = rows.Close() }()
	sameEvent := func(entry AppLogEntry) bool {
		return debugMetadataString(entry.Metadata, "kind") == kind &&
			debugMetadataString(entry.Metadata, "group_id") == groupID &&
			debugMetadataString(entry.Metadata, "user_id") == userID &&
			debugMetadataString(entry.Metadata, "platform") == strings.TrimSpace(source.Platform) &&
			debugMetadataString(entry.Metadata, "profile_id") == strings.TrimSpace(source.ProfileID)
	}
	entries := make([]AppLogEntry, 0, 16)
	for rows.Next() {
		entry, scanErr := scanLogEntry(rows)
		if scanErr != nil {
			return InboundEventTrace{}, false, scanErr
		}
		if sameEvent(entry) {
			entries = append(entries, entry)
		}
	}
	if err := rows.Err(); err != nil {
		return InboundEventTrace{}, false, err
	}
	// 模型请求轨迹存在库旁边的文件里（见 debug_trace_files.go），跨群检索记录和
	// 升级前的旧轨迹还在 app_logs，两边合起来按时间排。
	fileEntries, err := s.readDebugTraceFiles(groupID, userID, messageID)
	if err != nil {
		return InboundEventTrace{}, false, err
	}
	for _, entry := range fileEntries {
		if sameEvent(entry) {
			entries = append(entries, entry)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].CreatedAt.Before(entries[j].CreatedAt)
		}
		return entries[i].ID < entries[j].ID
	})
	// 「收到消息」只用来判断处理时调试模式开没开，不作为一步显示：消息本身事件卡片上已经有了。
	steps := make([]AppLogEntry, 0, len(entries))
	received := false
	for _, entry := range entries {
		if debugMetadataString(entry.Metadata, "phase") == debugTracePhaseEventReceived {
			received = true
			continue
		}
		steps = append(steps, entry)
	}
	trace := InboundEventTrace{MessageID: messageID, Steps: steps}
	if len(steps) == 0 {
		trace.EmptyReason = s.debugTraceEmptyReason(row, received)
	}
	return trace, true, nil
}

// debugTracePhaseEventReceived 和 assistant 包里写入时用的阶段名一致。
const debugTracePhaseEventReceived = "event_received"

type debugTraceEventRow struct {
	messageID      string
	status         string
	outcome        string
	decisionReason string
	createdAt      int64
}

// debugTraceEmptyReason 解释一条事件为什么没有模型调用轨迹。每一条都要说得出
// 依据；「检查一下调试模式」只在确实查得到是它关着时才说。
func (s *SQLiteStore) debugTraceEmptyReason(row debugTraceEventRow, received bool) string {
	if strings.TrimSpace(row.messageID) == "" {
		return "这条事件没有消息 ID（例如部分通知事件）。调试轨迹按消息归档，这类事件不会记录。"
	}
	if row.status == "pending" || row.status == "processing" {
		return "事件还在排队或处理中，处理完才会有模型调用记录。"
	}
	enqueuedAt := time.Unix(0, row.createdAt)
	if prunedBefore := s.debugTracePrunedBefore(); !prunedBefore.IsZero() && enqueuedAt.Before(prunedBefore) {
		return fmt.Sprintf("这条事件早于 %s，调试记录已按调试日志保留期清理。", prunedBefore.Local().Format("2006-01-02 15:04"))
	}
	switch row.outcome {
	case "ignored_stale":
		return "消息积压太久，按过期消息直接跳过，没有进入处理流程，也就没有调用模型。"
	case "backfill_history_only":
		return "这是断线回补的消息，只补进了上下文历史，没有进入处理流程，也就没有调用模型。"
	}
	if received {
		why := strings.TrimSpace(row.decisionReason)
		if why == "" {
			why = "处理结果为 " + firstNonEmptyText(row.outcome, "未知")
		}
		return "处理这条消息时调试模式是开着的，但它在调用模型之前就结束了：" + why + "。"
	}
	if since := s.debugTraceSince(); since.IsZero() || enqueuedAt.Before(since) {
		return "这条事件在新版调试记录上线之前处理。当时要么调试模式关着，要么没有调用模型，旧版本没有记下是哪一种。"
	}
	return "处理这条消息时，这个机器人的调试模式是关着的。"
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
