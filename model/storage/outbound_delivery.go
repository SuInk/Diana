// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// inboundDeliverySetSQL 是投递阶段的推进规则，两种定位方式共用。
const inboundDeliverySetSQL = `
UPDATE inbound_events
SET delivery_stage = CASE
      WHEN ? = 'failed' AND COALESCE(delivery_stage, '') IN ('acknowledged', 'echo_persisted') THEN delivery_stage
      WHEN ? = 'send_attempted' AND COALESCE(delivery_stage, '') IN ('acknowledged', 'echo_persisted') THEN delivery_stage
      ELSE ?
    END,
    outbound_message_id = CASE
      WHEN ? = '' THEN outbound_message_id
      WHEN COALESCE(outbound_message_id, '') = '' THEN ?
      WHEN instr(',' || outbound_message_id || ',', ',' || ? || ',') > 0 THEN outbound_message_id
      ELSE outbound_message_id || ',' || ?
    END,
    reply_generated_at = CASE WHEN ? = 'generated' AND reply_generated_at IS NULL THEN ? ELSE reply_generated_at END,
    send_attempted_at = CASE WHEN ? = 'send_attempted' AND send_attempted_at IS NULL THEN ? ELSE send_attempted_at END,
    send_acked_at = CASE WHEN ? = 'acknowledged' THEN ? ELSE send_acked_at END,
    delivery_error = CASE WHEN ? = 'failed' THEN ? WHEN ? IN ('acknowledged', 'echo_persisted') THEN NULL ELSE delivery_error END,
    updated_at = ?
`

// inboundDeliveryByIDSQL 按主键定位这一轮的入站事件，再核对它确实是这条消息，
// 不扫表。发送回执是每条分片都要写一次的热路径，走这条。
const inboundDeliveryByIDSQL = inboundDeliverySetSQL + `
WHERE id = ?
  AND message_id = ?
  AND kind = ?
  AND COALESCE(group_id, '') = ?
  AND COALESCE(user_id, '') = ?
RETURNING id
`

// inboundDeliveryByMessageSQL 是不知道入站事件 id 时的老办法：按消息找最新那条。
const inboundDeliveryByMessageSQL = inboundDeliverySetSQL + `
WHERE id = (
  SELECT id
  FROM inbound_events
  WHERE message_id = ?
    AND kind = ?
    AND COALESCE(group_id, '') = ?
    AND COALESCE(user_id, '') = ?
  ORDER BY created_at DESC, id DESC
  LIMIT 1
)
RETURNING id
`

func inboundDeliverySetArgs(stage assistant.OutboundDeliveryStage, outboundMessageID, detail string, now int64) []any {
	return []any{
		string(stage), string(stage), string(stage),
		outboundMessageID, outboundMessageID, outboundMessageID, outboundMessageID,
		string(stage), now,
		string(stage), now,
		string(stage), now,
		string(stage), detail, string(stage), now,
	}
}

func inboundDeliveryEventArgs(event assistant.MessageEvent) []any {
	return []any{
		strings.TrimSpace(event.MessageID), strings.TrimSpace(string(event.Kind)),
		strings.TrimSpace(event.GroupID), strings.TrimSpace(event.UserID),
	}
}

// RecordInboundEventDelivery advances the durable delivery state for the
// source inbound event. Stages are monotonic, so late retry callbacks cannot
// downgrade an acknowledged or echoed message.
func (s *SQLiteStore) RecordInboundEventDelivery(ctx context.Context, event assistant.MessageEvent, stage assistant.OutboundDeliveryStage, outboundMessageID, detail string) error {
	return s.RecordInboundEventDeliveryByID(ctx, "", event, stage, outboundMessageID, detail)
}

// RecordInboundEventDeliveryByID 和 RecordInboundEventDelivery 一样推进投递阶段，
// 但调用方知道这一轮对应的入站事件 id 时按主键定位。id 对不上这条消息（比如
// 这一轮顺带发给别的会话的消息）才退回按消息查找。回执带 message_id 时顺手写
// 回推映射，用的就是刚更新的那一行的 id，不再为映射另查一遍表。
func (s *SQLiteStore) RecordInboundEventDeliveryByID(ctx context.Context, inboundEventID string, event assistant.MessageEvent, stage assistant.OutboundDeliveryStage, outboundMessageID, detail string) error {
	if s == nil || s.db == nil || strings.TrimSpace(event.MessageID) == "" {
		return nil
	}
	now := time.Now().UTC().UnixNano()
	outboundMessageID = strings.TrimSpace(outboundMessageID)
	detail = strings.TrimSpace(detail)
	setArgs := inboundDeliverySetArgs(stage, outboundMessageID, detail, now)
	var updatedID string
	err := sql.ErrNoRows
	if inboundEventID = strings.TrimSpace(inboundEventID); inboundEventID != "" {
		args := append(append(append([]any{}, setArgs...), inboundEventID), inboundDeliveryEventArgs(event)...)
		err = s.db.QueryRowContext(ctx, inboundDeliveryByIDSQL, args...).Scan(&updatedID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		args := append(append([]any{}, setArgs...), inboundDeliveryEventArgs(event)...)
		err = s.db.QueryRowContext(ctx, inboundDeliveryByMessageSQL, args...).Scan(&updatedID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("record inbound delivery stage: %w", err)
	}
	if stage == assistant.OutboundDeliveryAcknowledged && outboundMessageID != "" {
		return s.recordOutboundMessageMap(ctx, updatedID, event, outboundMessageID)
	}
	return nil
}

// outboundMessageMapSchema 把「机器人发出的一条消息」精确映射回触发它的入站事件。
//
// 以前回推用 ',' || outbound_message_id || ',' LIKE '%,id,%' 去 inbound_events
// 里找：前导通配符用不上索引，每条回推都全表扫一遍、还占着写锁；也不分账号和
// 会话，message_id 撞上的陈年旧行会被改掉。现在发送回执记下时写一行映射，回推按
// (账号, 会话, message_id) 主键精确命中。
const outboundMessageMapSchema = `
CREATE TABLE IF NOT EXISTS outbound_message_map (
  profile_id TEXT NOT NULL,
  conversation TEXT NOT NULL,
  message_id TEXT NOT NULL,
  inbound_event_id TEXT NOT NULL,
  sent_at INTEGER NOT NULL,
  PRIMARY KEY (profile_id, conversation, message_id)
);
CREATE INDEX IF NOT EXISTS idx_outbound_message_map_sent_at ON outbound_message_map(sent_at);
`

const (
	// outboundEchoLinkWindow 是回推能关联回去的时间窗：回推最多晚到几分钟，
	// 超过这个窗口的同号消息只可能是 message_id 撞号。
	outboundEchoLinkWindow = time.Hour
	// outboundMessageMapRetention 之后的映射不再有用，写新映射时顺手清掉。
	outboundMessageMapRetention = 7 * 24 * time.Hour
)

func (s *SQLiteStore) migrateOutboundMessageMap() error {
	if _, err := s.db.Exec(outboundMessageMapSchema); err != nil {
		return fmt.Errorf("create outbound message map schema: %w", err)
	}
	return nil
}

// outboundConversationKey 是一条消息所在的会话：群用群号，私聊用对方的号。
func outboundConversationKey(kind assistant.EventKind, groupID, peerID string) string {
	switch kind {
	case assistant.EventKindGroup:
		if groupID = strings.TrimSpace(groupID); groupID != "" {
			return "group:" + groupID
		}
	case assistant.EventKindPrivate:
		if peerID = strings.TrimSpace(peerID); peerID != "" {
			return "private:" + peerID
		}
	}
	return ""
}

// recordOutboundMessageMap 在发送回执记下时写映射，指向刚刚更新的那条入站事件。
func (s *SQLiteStore) recordOutboundMessageMap(ctx context.Context, inboundEventID string, event assistant.MessageEvent, outboundMessageID string) error {
	conversation := outboundConversationKey(event.Kind, event.GroupID, event.UserID)
	outboundMessageID = strings.TrimSpace(outboundMessageID)
	if conversation == "" || outboundMessageID == "" || strings.TrimSpace(inboundEventID) == "" {
		return nil
	}
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, recordOutboundMessageMapSQL,
		strings.TrimSpace(event.ProfileID), conversation, outboundMessageID, strings.TrimSpace(inboundEventID), now.UnixNano()); err != nil {
		return fmt.Errorf("record outbound message map: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM outbound_message_map WHERE sent_at < ?`,
		now.Add(-outboundMessageMapRetention).UnixNano()); err != nil {
		return fmt.Errorf("prune outbound message map: %w", err)
	}
	return nil
}

const recordOutboundMessageMapSQL = `
INSERT INTO outbound_message_map (profile_id, conversation, message_id, inbound_event_id, sent_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(profile_id, conversation, message_id) DO UPDATE SET
  inbound_event_id = excluded.inbound_event_id,
  sent_at = excluded.sent_at
`

// recordSelfEchoSQL 先按主键命中映射，再按主键改入站事件，两步都不扫表。
const recordSelfEchoSQL = `
UPDATE inbound_events
SET delivery_stage = CASE WHEN COALESCE(delivery_error, '') = '' THEN 'echo_persisted' ELSE delivery_stage END,
    self_echo_at = ?,
    updated_at = ?
WHERE id = (
  SELECT inbound_event_id
  FROM outbound_message_map
  WHERE profile_id = ? AND conversation = ? AND message_id = ? AND sent_at >= ?
)
`

// RecordInboundEventSelfEcho links a real OneBot self-message echo to the
// inbound event whose reply it is, by exact (account, conversation, message_id)
// within outboundEchoLinkWindow.
//
// 回推只补 self_echo_at，不清 delivery_error：同一轮里前一条分片的回推晚到时，
// 后一条分片的发送失败还得留着给人看。有失败记录时阶段也不改成已回推。
func (s *SQLiteStore) RecordInboundEventSelfEcho(ctx context.Context, echo assistant.MessageEvent, observedAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	messageID := strings.TrimSpace(echo.MessageID)
	conversation := outboundConversationKey(echo.Kind, echo.GroupID, echo.TargetID)
	if messageID == "" || conversation == "" {
		return nil
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, recordSelfEchoSQL, observedAt.UTC().UnixNano(), now.UnixNano(),
		strings.TrimSpace(echo.ProfileID), conversation, messageID, now.Add(-outboundEchoLinkWindow).UnixNano())
	if err != nil {
		return fmt.Errorf("record inbound self echo: %w", err)
	}
	return nil
}

func inboundTransportKey(event assistant.MessageEvent) string {
	messageID := strings.TrimSpace(event.MessageID)
	if messageID == "" {
		return ""
	}
	return strings.Join([]string{
		assistant.NormalizePlatformID(event.Platform), strings.TrimSpace(event.SelfID),
		strings.TrimSpace(string(event.Kind)), strings.TrimSpace(event.GroupID),
		strings.TrimSpace(event.UserID), messageID,
	}, "\x00")
}

// OutboundStepDelivered 查询这条入站事件的某个出站步骤是否已经成功送达。
// 入站队列失败重跑时据此跳过已经发出去的分片和媒体。
func (s *SQLiteStore) OutboundStepDelivered(ctx context.Context, turnID, stepKey string) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, nil
	}
	turnID, stepKey = strings.TrimSpace(turnID), strings.TrimSpace(stepKey)
	if turnID == "" || stepKey == "" {
		return "", false, nil
	}
	var messageID sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT message_id FROM outbound_delivery_steps WHERE turn_id = ? AND step_key = ?
`, turnID, stepKey).Scan(&messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("load outbound step %q: %w", stepKey, err)
	}
	return strings.TrimSpace(messageID.String), true, nil
}

// RecordOutboundStep 登记一个已经送达的出站步骤。
func (s *SQLiteStore) RecordOutboundStep(ctx context.Context, turnID, stepKey, messageID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	turnID, stepKey = strings.TrimSpace(turnID), strings.TrimSpace(stepKey)
	if turnID == "" || stepKey == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO outbound_delivery_steps (turn_id, step_key, message_id, delivered_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(turn_id, step_key) DO UPDATE SET
  message_id = CASE WHEN excluded.message_id = '' THEN outbound_delivery_steps.message_id ELSE excluded.message_id END
`, turnID, stepKey, strings.TrimSpace(messageID), time.Now().UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("record outbound step %q: %w", stepKey, err)
	}
	return nil
}

// ClearOutboundSteps 在入站事件走到终态后清理账本。
func (s *SQLiteStore) ClearOutboundSteps(ctx context.Context, turnID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM outbound_delivery_steps WHERE turn_id = ?`, turnID); err != nil {
		return fmt.Errorf("clear outbound steps for %q: %w", turnID, err)
	}
	return nil
}
