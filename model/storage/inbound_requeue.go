// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInboundEventNotRetryable 表示这条事件不存在，或者不是处理失败的状态。
var ErrInboundEventNotRetryable = errors.New("inbound event is not a failed event")

// maxInboundFailedRequeue 限制「重试全部失败」一次放回队列的条数：
// 上游挂了一阵子会攒下一大批失败，全放回去等于同时开出一堆模型调用。
const maxInboundFailedRequeue = 100

// inboundRequeueSet 把已完成的事件放回待处理，并在 payload 上打 manual_retry：
// 手动重试不受「太旧的消息不回复」限制，是主人看过以后明确要它回的。
const inboundRequeueSet = `
UPDATE inbound_events
SET status = 'pending', attempts = 0, available_at = ?, last_error = NULL,
    payload = json_set(payload, '$.manual_retry', json('true')),
    outcome = NULL, decision = NULL, decision_reason = NULL, reply_text = NULL,
    processing_error = NULL, duration_ms = NULL, delivery_stage = NULL,
    outbound_message_id = NULL, reply_generated_at = NULL, send_attempted_at = NULL,
    send_acked_at = NULL, self_echo_at = NULL, delivery_error = NULL,
    lease_owner = NULL, lease_until = NULL, completed_at = NULL, updated_at = ?
`

// RequeueFailedInboundEvent 把一条处理失败的事件重新放回队列。
func (s *SQLiteStore) RequeueFailedInboundEvent(ctx context.Context, id string) error {
	defer s.observeStorage(ctx, "RequeueFailedInboundEvent", "write")()
	if s == nil || s.db == nil {
		return errors.New("requeue inbound event: sqlite store is not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("requeue inbound event: id is required")
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, inboundRequeueSet+`
WHERE id IN (SELECT i.id FROM inbound_events AS i WHERE i.id = ? AND `+inboundEventErrorCondition+`)`, now, now, id)
	if err != nil {
		return fmt.Errorf("requeue inbound event %q: %w", id, err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("requeue inbound event %q: %w", id, err)
	} else if affected == 0 {
		return ErrInboundEventNotRetryable
	}
	return nil
}

// RequeueFailedInboundEvents 把 since 之后处理失败的事件重新放回队列，返回放回的条数。
// 每个会话只放最近 perSession 条：同一个人连发的几条都失败了，全重跑会把旧问题挨个答一遍。
// profileID 为空时不按机器人过滤。
func (s *SQLiteStore) RequeueFailedInboundEvents(ctx context.Context, since time.Time, profileID string, perSession int) (int, error) {
	defer s.observeStorage(ctx, "RequeueFailedInboundEvents", "write")()
	if s == nil || s.db == nil {
		return 0, errors.New("requeue inbound events: sqlite store is not configured")
	}
	if perSession <= 0 {
		perSession = 1
	}
	now := time.Now().UTC().UnixNano()
	profileID = strings.TrimSpace(profileID)
	result, err := s.db.ExecContext(ctx, inboundRequeueSet+`
WHERE id IN (
  SELECT id FROM (
    SELECT i.id, i.created_at,
           ROW_NUMBER() OVER (PARTITION BY i.session ORDER BY i.created_at DESC) AS rank_in_session
    FROM inbound_events AS i
    WHERE i.created_at >= ? AND (? = '' OR COALESCE(i.profile_id, '') = ?) AND `+inboundEventErrorCondition+`
  )
  WHERE rank_in_session <= ?
  ORDER BY created_at DESC
  LIMIT ?
)`, now, now, since.UTC().UnixNano(), profileID, profileID, perSession, maxInboundFailedRequeue)
	if err != nil {
		return 0, fmt.Errorf("requeue failed inbound events: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("requeue failed inbound events: %w", err)
	}
	return int(affected), nil
}
