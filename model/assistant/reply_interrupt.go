// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"time"
)

// 回复打断：两种「这条回复不该再发出去」的情形，都在首条消息发出之前拦截。
//
//  1. 撤回打断：用户撤回了触发消息，说明想收回或修正这句话。还没送出的回复
//     直接放弃；用户修正后重发的消息按普通新消息重新回答，不会新旧各回一次。
//  2. 追发合并：回复还没开口，同一用户又发来一条明确叫机器人的消息，多半是
//     上一条的补充或修正。旧回复放弃，由新消息那一轮结合上下文一并回答。
//     私聊队列按会话串行，新一轮生成时必然能看到前一条还没被回应的消息。
//
// 两个登记表都只存内存：进程重启后失去打断能力是安全的退化（顶多多回一条），
// 而误拦一条该发的回复才是真正的事故。已经开始分条投递的回复不在中途打断，
// 避免留下半截话（后续分条带 continuousOutboundDelivery 标记，直接放行）。
const (
	// replyInterruptRetention 是登记项的有效期。撤回到回复送出的竞争窗口只有
	// 秒级，10 分钟足够覆盖排队和重试，也避免登记表无限增长。
	replyInterruptRetention = 10 * time.Minute
	// replyInterruptPruneThreshold 超过该数量时顺手清理过期项。
	replyInterruptPruneThreshold = 256
)

var (
	errReplyTriggerRecalled   = errors.New("chatbot: reply trigger message was recalled")
	errReplyTriggerSuperseded = errors.New("chatbot: reply trigger superseded by a newer directed message")
)

// inboundOutcomeRecalled 是触发消息被撤回后的终态，不重试、不补发。
const inboundOutcomeRecalled = "dropped_recalled"

// directedInboundMark 记录一个会话里某用户最近一条明确叫机器人的消息。
type directedInboundMark struct {
	messageID string
	at        time.Time
}

// replyTriggerGateContextKey 标记「这次发送是对某条入站消息的直接回复」。
// 只有这类发送才检查撤回和追发；通知、欢迎语、后台任务的发送不受影响。
type replyTriggerGateContextKey struct{}

func withReplyTriggerGate(ctx context.Context) context.Context {
	return context.WithValue(ctx, replyTriggerGateContextKey{}, true)
}

func replyTriggerGateEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(replyTriggerGateContextKey{}).(bool)
	return enabled
}

// recalledInboundKey 以会话加消息 ID 定位一条入站消息。撤回通知的 Kind 是
// notice，但 sessionKey 只看群号/用户号，能和原消息落到同一个键上。
func recalledInboundKey(event MessageEvent) string {
	id := strings.TrimSpace(event.MessageID)
	if id == "" {
		return ""
	}
	return sessionKey(event) + "|" + id
}

func directedInboundKey(event MessageEvent) string {
	userID := strings.TrimSpace(event.UserID)
	if userID == "" {
		return ""
	}
	return sessionKey(event) + "|" + userID
}

// noteRecalledInbound 登记一条撤回通知。被撤回的消息如果恰好是该用户最近的
// 直呼消息，一并清掉追发登记：否则前一条消息会因为「存在更新的直呼」被误判
// 为已被取代，而那条更新的消息自己已经撤回，结果谁都不回答。
func (r *Runtime) noteRecalledInbound(event MessageEvent) {
	key := recalledInboundKey(event)
	if key == "" {
		return
	}
	now := time.Now()
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	if r.recalledInbound == nil {
		r.recalledInbound = map[string]time.Time{}
	}
	pruneRecalledInbound(r.recalledInbound, now)
	r.recalledInbound[key] = now
	if directedKey := directedInboundKey(event); directedKey != "" {
		if mark, ok := r.latestDirectedInbound[directedKey]; ok && mark.messageID == strings.TrimSpace(event.MessageID) {
			delete(r.latestDirectedInbound, directedKey)
		}
	}
}

// inboundTriggerRecalled 报告事件对应的入站消息是否已被撤回。
func (r *Runtime) inboundTriggerRecalled(event MessageEvent) bool {
	key := recalledInboundKey(event)
	if key == "" {
		return false
	}
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	recalledAt, ok := r.recalledInbound[key]
	return ok && time.Since(recalledAt) <= replyInterruptRetention
}

// noteDirectedInbound 在入站时登记明确叫机器人的消息（私聊全部算，群聊要求
// @、直呼或称呼触发）。登记发生在事件入队之前，因此处理旧消息时登记表里
// 一定能看到更新的直呼消息。
func (r *Runtime) noteDirectedInbound(event MessageEvent) {
	if event.Kind != EventKindGroup && event.Kind != EventKindPrivate {
		return
	}
	key := directedInboundKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return
	}
	if !r.shouldHandleChatTrigger(event, directedInboundText(event)) {
		return
	}
	now := time.Now()
	r.replyInterruptMu.Lock()
	defer r.replyInterruptMu.Unlock()
	if r.latestDirectedInbound == nil {
		r.latestDirectedInbound = map[string]directedInboundMark{}
	}
	pruneDirectedInbound(r.latestDirectedInbound, now)
	r.latestDirectedInbound[key] = directedInboundMark{messageID: messageID, at: now}
}

// inboundTriggerSuperseded 报告触发消息是否已被同一用户更新的直呼消息取代。
// 只有触发消息自己是直呼时比较才有意义：它入站时必然登记过，登记表里出现
// 别的消息 ID 就说明更新的直呼在它之后到达。主动插话的触发不是直呼，走的
// 是主动回复自己的取代机制，这里不管。
func (r *Runtime) inboundTriggerSuperseded(event MessageEvent) bool {
	key := directedInboundKey(event)
	messageID := strings.TrimSpace(event.MessageID)
	if key == "" || messageID == "" {
		return false
	}
	r.replyInterruptMu.Lock()
	mark, ok := r.latestDirectedInbound[key]
	r.replyInterruptMu.Unlock()
	if !ok || mark.messageID == messageID || time.Since(mark.at) > replyInterruptRetention {
		return false
	}
	return r.shouldHandleChatTrigger(event, directedInboundText(event))
}

// interruptedReplyError 返回发送前的打断检查结果；nil 表示放行。
func (r *Runtime) interruptedReplyError(ctx context.Context, event MessageEvent) error {
	if !replyTriggerGateEnabled(ctx) || continuousOutboundDelivery(ctx) {
		return nil
	}
	if r.inboundTriggerRecalled(event) {
		return errReplyTriggerRecalled
	}
	if r.inboundTriggerSuperseded(event) {
		return errReplyTriggerSuperseded
	}
	return nil
}

func directedInboundText(event MessageEvent) string {
	if text := PlainText(event.Segments); text != "" {
		return text
	}
	return event.RawMessage
}

func pruneRecalledInbound(entries map[string]time.Time, now time.Time) {
	if len(entries) < replyInterruptPruneThreshold {
		return
	}
	for key, at := range entries {
		if now.Sub(at) > replyInterruptRetention {
			delete(entries, key)
		}
	}
}

func pruneDirectedInbound(entries map[string]directedInboundMark, now time.Time) {
	if len(entries) < replyInterruptPruneThreshold {
		return
	}
	for key, mark := range entries {
		if now.Sub(mark.at) > replyInterruptRetention {
			delete(entries, key)
		}
	}
}
