// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// OutboundStepLedger 记录一次入站事件在处理过程中已经成功送达的出站步骤。
// 入站队列失败后会整条重跑：重跑时已经发出去的文字分片、图片和视频不应该再发
// 一次，否则用户会收到重复消息（典型表现是同一张图片被反复发送）。
type OutboundStepLedger interface {
	// OutboundStepDelivered 查询某个步骤是否已经送达，并返回当时的消息 ID。
	OutboundStepDelivered(ctx context.Context, turnID, stepKey string) (messageID string, delivered bool, err error)
	// RecordOutboundStep 在步骤真正送达后登记，供后续重跑跳过。
	RecordOutboundStep(ctx context.Context, turnID, stepKey, messageID string) error
	// ClearOutboundSteps 在这条入站事件不再需要重跑时清理账本。
	ClearOutboundSteps(ctx context.Context, turnID string) error
}

type outboundTurnKey struct{}

// outboundTurn 把「同一条入站事件的这一轮处理」串起来。seq 让步骤键带上序号，
// 于是同一轮里两条内容完全相同的消息不会互相误判为重复。
type outboundTurn struct {
	id  string
	seq atomic.Int64
}

func withOutboundTurn(ctx context.Context, turnID string) context.Context {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return ctx
	}
	return context.WithValue(ctx, outboundTurnKey{}, &outboundTurn{id: turnID})
}

func outboundTurnFromContext(ctx context.Context) *outboundTurn {
	if ctx == nil {
		return nil
	}
	turn, _ := ctx.Value(outboundTurnKey{}).(*outboundTurn)
	return turn
}

// nextStepKey 返回本轮下一个步骤的键：序号 + 载荷指纹。两者都相同才算同一步，
// 因此重跑时内容发生变化（例如模型重新生成了不同的文字）不会被误跳过。
func (t *outboundTurn) nextStepKey(fingerprint string) string {
	if t == nil {
		return ""
	}
	return strconv.FormatInt(t.seq.Add(1), 10) + ":" + fingerprint
}

func outgoingMessageFingerprint(msg OutgoingMessage) string {
	parts := []string{
		"platform=" + msg.Platform,
		"profile=" + msg.ProfileID,
		"group=" + msg.GroupID,
		"user=" + msg.UserID,
		"text=" + msg.Text,
		"reply=" + msg.ReplyMessageID,
		"mention=" + msg.MentionUserID,
		"images=" + strings.Join(msg.ImageURLs, "|"),
		"videos=" + strings.Join(msg.VideoURLs, "|"),
	}
	for _, segment := range msg.Segments {
		parts = append(parts, "segment="+segment.Type+":"+segmentFingerprint(segment))
	}
	return fingerprintOf(parts...)
}

func segmentFingerprint(segment MessageSegment) string {
	keys := make([]string, 0, len(segment.Data))
	for key := range segment.Data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+segment.Data[key])
	}
	return strings.Join(parts, ";")
}

func fingerprintOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// outboundStepLedger 返回当前配置的账本；入站存储没有实现时返回 nil，调用方
// 退回「每次都真的发送」的旧行为。
func (r *Runtime) outboundStepLedger() OutboundStepLedger {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ledger, _ := r.inboundStore.(OutboundStepLedger)
	return ledger
}

// claimOutboundStep 在真正调用发送接口前判断这一步是否已经送达过。
// 返回的 stepKey 为空表示本次不参与幂等（没有 turn 或没有账本）。
func (r *Runtime) claimOutboundStep(ctx context.Context, fingerprint string) (stepKey, deliveredMessageID string, delivered bool) {
	turn := outboundTurnFromContext(ctx)
	if turn == nil {
		return "", "", false
	}
	ledger := r.outboundStepLedger()
	if ledger == nil {
		return "", "", false
	}
	stepKey = turn.nextStepKey(fingerprint)
	lookupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	messageID, ok, err := ledger.OutboundStepDelivered(lookupCtx, turn.id, stepKey)
	if err != nil {
		log.Printf("chatbot outbound step lookup failed: %v", err)
		return stepKey, "", false
	}
	return stepKey, messageID, ok
}

func (r *Runtime) recordOutboundStep(ctx context.Context, stepKey, messageID string) {
	if strings.TrimSpace(stepKey) == "" {
		return
	}
	turn := outboundTurnFromContext(ctx)
	if turn == nil {
		return
	}
	ledger := r.outboundStepLedger()
	if ledger == nil {
		return
	}
	recordCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ledger.RecordOutboundStep(recordCtx, turn.id, stepKey, messageID); err != nil {
		log.Printf("chatbot outbound step record failed: %v", err)
	}
}

// clearOutboundSteps 在入站事件走到终态后清理账本。
func (r *Runtime) clearOutboundSteps(turnID string) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	ledger := r.outboundStepLedger()
	if ledger == nil {
		return
	}
	clearCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ledger.ClearOutboundSteps(clearCtx, turnID); err != nil {
		log.Printf("chatbot outbound step cleanup failed: %v", err)
	}
}

// replayedOutboundResult 构造一个「这步之前已经发过」的返回值，让调用方拿到
// 与首次发送一致的消息 ID。
func replayedOutboundResult(messageID string) map[string]any {
	if strings.TrimSpace(messageID) == "" {
		return map[string]any{}
	}
	return map[string]any{"message_id": messageID}
}
