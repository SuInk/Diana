// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strings"
	"time"
)

// 群里几个人快速你来我往时，每来一条都跑一次接话评分，大半是重复劳动：下一条几秒后就到，
// 评分看的上下文几乎一样。生产 7 天回放（2026-10-22～28，24929 次评分）里，同群下一次
// 评分在 3 秒内开始的占 19.5%，token 占 20%。
//
// 做法沿用积压合并：要进接话评分的消息先等 proactiveRouteDebounce，期间同会话又有消息
// 排队，就把这条（连同它之前攒下的候选）登记进积压包，交给后面那条一起评一次。
// 同会话的消息本来就是串行处理的，后面那条一定会来取。@、引用、叫名字这些直接触发的
// 不走这里，不多等。
//
// 合并成一次评分后，模型的注意力落在最后那条上，前面那句「是的」「看完了」这种要靠上下文
// 才认得出在接机器人话的短句会漏判：回放 66 条前一条在叫机器人的连发，只标连发、换写法
// 最好也只认出 58 条。所以机器人刚参与过的对话不合并（proactiveRouteNearBot），这 66 条
// 都落在这层护栏里、照常单独评；剩下能合并的是机器人没掺和的群友闲聊，约占原先可省的三分之二。
const (
	defaultProactiveRouteDebounce = 3 * time.Second
	// 机器人在这个群说过话后多久之内不合并。
	proactiveRouteBotActiveWindow = 10 * time.Minute
	// 发送者和机器人有来回（@、回复它，或被它点名）后多久之内不合并。
	proactiveRouteSenderTalkWindow = time.Hour
)

// deferProactiveRouteToNewer 等一个防抖窗口，报告同会话是不是有更新的消息在排队。
func (r *Runtime) deferProactiveRouteToNewer(ctx context.Context, event MessageEvent) bool {
	wait := r.proactiveRouteDebounce
	if wait <= 0 || event.Kind != EventKindGroup || event.backlogProbe == nil {
		return false
	}
	item := *event.backlogProbe
	// 重试的那条可能已经发出去一半，主人的消息要在自己那一轮生效，都和积压交接同一口径。
	if item.Attempts > 1 || r.effectiveConfigForEvent(event).IsOwnerEvent(event) || r.proactiveRouteNearBot(event) {
		return false
	}
	r.mu.RLock()
	store, _ := r.inboundStore.(InboundBacklogStore)
	r.mu.RUnlock()
	if store == nil {
		return false
	}
	newer := func() bool {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		pending, err := store.InboundSessionHasNewerPending(checkCtx, item)
		if err != nil {
			log.Printf("diana proactive route debounce check failed: %v", err)
			return false
		}
		return pending
	}
	// 已经有人排着就不用再等。
	if newer() {
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return false
	}
	return newer()
}

// proactiveRouteNearBot 报告这条消息是不是落在机器人刚参与过的对话里：机器人最近在这个
// 群说过话，或者发送者不久前和机器人有过来回。这种消息可能是在接机器人的话，要单独评分。
func (r *Runtime) proactiveRouteNearBot(event MessageEvent) bool {
	cfg := r.effectiveConfigForEvent(event)
	botID := firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(event.SelfID))
	history := event.replyHistory
	if !event.replyHistoryLoaded {
		history, _ = r.sessionContextHistory(event)
	}
	for i := len(history) - 1; i >= 0; i-- {
		item := history[i]
		if item.MessageID != "" && item.MessageID == event.MessageID {
			continue
		}
		age := time.Duration(event.Time-item.Time) * time.Second
		if item.Time <= 0 || age > proactiveRouteSenderTalkWindow {
			if item.Time > 0 {
				break
			}
			continue
		}
		if strings.TrimSpace(item.botReply) != "" || assistantHistoryEvent(item, botID) {
			if age <= proactiveRouteBotActiveWindow || proactiveReplyBotMessageAddressesUser(item, history, event.UserID) {
				return true
			}
			continue
		}
		if item.UserID == event.UserID && (eventDirectlyMentionsBot(item, cfg) || eventRepliesToBot(item, cfg)) {
			return true
		}
	}
	return false
}

// holdProactiveRouteForNewer 把这条和它之前攒下的候选按原顺序放回积压包。
func (r *Runtime) holdProactiveRouteForNewer(event MessageEvent, text string, now time.Time) {
	key := sessionKey(event)
	held := make([]backlogMessage, 0, len(event.backlogProactive))
	for _, candidate := range event.backlogProactive {
		held = append(held, backlogMessage{proactiveReplyCandidate: candidate, Proactive: true})
	}
	if len(held) > 0 {
		r.backlogMu.Lock()
		if r.backlogMessages == nil {
			r.backlogMessages = map[string][]backlogMessage{}
		}
		items := append(r.backlogMessages[key], held...)
		if len(items) > backlogMessageMaxItems {
			items = items[len(items)-backlogMessageMaxItems:]
		}
		r.backlogMessages[key] = items
		r.backlogMu.Unlock()
	}
	event.backlogProactive = nil
	r.holdBacklogMessage(event, text, false, now)
}
