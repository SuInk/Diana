// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"time"
)

// 闲聊关掉、只开着回应提问时，接话评分只剩一个问题：这条没 @、没引用、没点名的
// 消息是不是在跟机器人说话。机器人很久没在这个群开过口，答案几乎总是「不是」——
// 冲着它来的话会 @ 它、引用它或叫它的名字，那些走直接触发，不经过这里。为这类
// 消息每条都调一次评分模型，钱基本白花。
//
// 所以回应提问只在机器人上次在本会话说话后的一段时间里生效：刚聊过，群友顺着往下
// 问「那 xx 呢」不带名字，评分照常判；过了窗口就不调模型，等下一次直接触发把窗口
// 重新打开。闲聊开着时不受影响，评分那一路本来就要看每条消息。

// relevanceOnlyActiveWindow 是闲聊关闭时，机器人在本会话说过话后回应提问保持判断的时长。
const relevanceOnlyActiveWindow = 30 * time.Minute

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
	return !r.repliedWithin(event, now, relevanceOnlyActiveWindow)
}

// repliedWithin 报告机器人在本会话最后一次说话是否在 window 以内。先看回复发出时
// 记下的时间，它不受上下文条数限制，热闹的群里几分钟就能把机器人那句挤出最近历史；
// 内存里没有（刚重启）或已经过期时再从历史里找，提醒这类不走回复流程的发言也在里面。
func (r *Runtime) repliedWithin(event MessageEvent, now time.Time, window time.Duration) bool {
	r.mu.RLock()
	last, ok := r.groupLastReplyAt[chatInCooldownKey(event)]
	r.mu.RUnlock()
	if ok && now.Sub(last) <= window {
		return true
	}
	last, ok = r.lastBotReplyAt(event)
	return ok && now.Sub(last) <= window
}

// markGroupReplied 在群回复真正发出去后记下时间，供 repliedWithin 使用。
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
