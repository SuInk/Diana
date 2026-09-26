// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"strings"
	"time"
)

// 提醒和事件触发任务是「到点了/条件满足了，机器人主动找某个人」。它们和主动接话
// 是两条互不相识的路：2026-09-26 12:27，事件触发任务刚 @ 了一个人提醒他，主动接话
// 又对他同一条消息接了一句，两条说的是同一件事。
//
// 这里不比较两条的意思，只记「刚才有提醒或触发任务找过这个会话里的这个人」：
// 窗口内对同一个人的随口接话直接放掉。被 @、被引用、被叫名字的直接回复不受影响，
// 接话评分判定「他就是在跟机器人说话」（relevance.directed）的也照常回——
// 人家在跟机器人说话，就该回。
const triggeredDeliveryWindow = 60 * time.Second

var errProactiveReplyCoveredByTrigger = errors.New("diana: proactive reply covered by a recent reminder or event trigger")

func triggeredDeliveryKey(event MessageEvent) string {
	userID := strings.TrimSpace(event.UserID)
	if userID == "" || (event.Kind != EventKindGroup && event.Kind != EventKindPrivate) {
		return ""
	}
	return sessionKey(event) + "|" + userID
}

// noteTriggeredDelivery 记下提醒或事件触发任务在 event 所在会话里找了 event.UserID。
// 事件触发任务在认领时就记：它在后台跑模型，送达可能比同一条消息的主动接话还晚。
func (r *Runtime) noteTriggeredDelivery(event MessageEvent) {
	key := triggeredDeliveryKey(event)
	if key == "" {
		return
	}
	now := time.Now()
	r.triggeredDeliveryMu.Lock()
	defer r.triggeredDeliveryMu.Unlock()
	if r.recentTriggeredDeliveries == nil {
		r.recentTriggeredDeliveries = map[string]time.Time{}
	}
	if len(r.recentTriggeredDeliveries) >= replyInterruptPruneThreshold {
		for existing, at := range r.recentTriggeredDeliveries {
			if now.Sub(at) > triggeredDeliveryWindow {
				delete(r.recentTriggeredDeliveries, existing)
			}
		}
	}
	r.recentTriggeredDeliveries[key] = now
}

// recentTriggeredDeliveryFor 报告窗口内是否有提醒或触发任务找过这条消息的发送者。
func (r *Runtime) recentTriggeredDeliveryFor(event MessageEvent) bool {
	key := triggeredDeliveryKey(event)
	if key == "" {
		return false
	}
	r.triggeredDeliveryMu.Lock()
	defer r.triggeredDeliveryMu.Unlock()
	at, ok := r.recentTriggeredDeliveries[key]
	return ok && time.Since(at) <= triggeredDeliveryWindow
}

// triggeredDeliveryCoversProactiveReply 是发送前的那一道：路由时触发任务可能还没
// 认领，等主动接话生成完，它已经找过这个人了。
func (r *Runtime) triggeredDeliveryCoversProactiveReply(event MessageEvent) error {
	if !proactiveReplyCoveredByTrigger(event) {
		return nil
	}
	if r.recentTriggeredDeliveryFor(event) {
		return errProactiveReplyCoveredByTrigger
	}
	return nil
}

const triggeredDeliverySkipReason = "提醒或事件触发任务刚找过这个人，主动接话不再重复"

// proactiveReplyCoveredByTrigger 圈出能被提醒盖掉的那种回复：没人叫机器人、评分也没
// 判定是在跟机器人说话的主动接话。
func proactiveReplyCoveredByTrigger(event MessageEvent) bool {
	return (event.proactiveReply || event.chatInReply) && !event.routingDirected
}
