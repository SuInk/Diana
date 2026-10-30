// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"time"
)

// 闲聊关掉、只开着回应提问时，接话评分只剩一个问题：这条没 @、没引用、没点名的
// 消息是不是在跟机器人说话。机器人很久没在这个群开过口，答案几乎总是「不是」——
// 冲着它来的话会 @ 它、引用它或叫它的名字，那些走直接触发，不经过这里。为这类
// 消息每条都调一次评分模型，钱基本白花。
//
// 所以回应提问只在机器人上次在本会话说话之后的一段里生效：30 分钟以内，或者它说话
// 后群里还没过 3 条消息，两个都过了才不调模型。只看时间的话，安静的群里隔一个多
// 小时有人顺着机器人那句往下问「那 xx 呢」就接不上；只看条数，热闹的群里几分钟就
// 刷过去了。过了窗口等下一次直接触发把它重新打开。闲聊开着时不受影响，评分那一路
// 本来就要看每条消息。

const (
	// relevanceOnlyActiveWindow 是闲聊关闭时，机器人在本会话说过话后回应提问保持判断的时长。
	relevanceOnlyActiveWindow = 30 * time.Minute
	// relevanceOnlyFollowupMessages 是机器人说话后，不论隔多久都照常评分的群消息条数。
	relevanceOnlyFollowupMessages = 3
)

// relevanceOnlyDormant 报告这条消息该不该因为「只开回应提问、机器人又很久没说话」
// 跳过接话评分。
func (r *Runtime) relevanceOnlyDormant(event MessageEvent, cfg BotConfig, now time.Time) bool {
	participation := cfg.chatInSettings().Participation
	if participation == nil {
		return false
	}
	if relevance, chat := participation.ratingLevels(); relevance != "on" || chat != "off" {
		return false
	}
	// 先看回复发出时记下的时间，它不受上下文条数限制：热闹的群里几分钟就能把机器人
	// 那句挤出最近历史。内存里没有（刚重启）或已经过期时再从历史里找，提醒这类不走
	// 回复流程的发言也在里面。
	r.mu.RLock()
	last, ok := r.groupLastReplyAt[chatInCooldownKey(event)]
	r.mu.RUnlock()
	if ok && now.Sub(last) <= relevanceOnlyActiveWindow {
		return false
	}
	last, since, ok := r.lastBotReplyInHistory(event, cfg)
	if !ok {
		return true
	}
	return since >= relevanceOnlyFollowupMessages && (last.IsZero() || now.Sub(last) > relevanceOnlyActiveWindow)
}

// lastBotReplyInHistory 在本会话最近历史里找机器人最后一句话，返回它的时间（历史
// 没记时间时为零值）和它后面又过了几条消息，当前这条不算。判定口径和
// lastBotReplyAt 一致。
func (r *Runtime) lastBotReplyInHistory(event MessageEvent, cfg BotConfig) (time.Time, int, bool) {
	botID := firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(event.SelfID))
	history, _ := r.sessionContextHistory(event)
	since := 0
	for i := len(history) - 1; i >= 0; i-- {
		item := history[i]
		if strings.TrimSpace(item.MessageID) != "" && item.MessageID == event.MessageID {
			continue
		}
		if strings.TrimSpace(item.botReply) == "" && !assistantHistoryEvent(item, botID) {
			since++
			continue
		}
		if item.Time <= 0 {
			return time.Time{}, since, true
		}
		return time.Unix(item.Time, 0), since, true
	}
	return time.Time{}, since, false
}

// markGroupReplied 在群回复真正发出去后记下时间，供 relevanceOnlyDormant 使用。
func (r *Runtime) markGroupReplied(event MessageEvent) {
	if event.Kind != EventKindGroup {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.groupLastReplyAt == nil {
		r.groupLastReplyAt = map[string]time.Time{}
	}
	r.groupLastReplyAt[chatInCooldownKey(event)] = time.Now()
}
