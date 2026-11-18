// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// 主动接话的排队和合并按「群 + 发言人」分开：两个人几乎同时说话、都被判为值得接时，
// 两轮各自生成，谁也看不到对方。生产上两条间隔 1 秒的「十八个月的猫要画成老奶奶」
// 就是这么来的。没人叫到的接话晚一步就没必要再说，这里在发送前看一眼：这一轮开始后
// 自己已经在这个群主动接过话，就不发了。被 @、被引用、被点名的回复不受影响，也不拦别人：
// 11-17 TG 群里有人回复机器人追问「具体烂在哪里」，同时另一个人贴图问「这些什么区别」；
// 后者查了 76 秒写好答案，却因为前者那条先发出去被整轮丢掉，看着就是不会主动回答。
// 被叫到的回复是在答别人的问题，跟这条主动接话不是同一件事，所以时钟只记主动接话。

var errChatInPreemptedByOwnReply = errors.New("diana: chat-in reply preempted by an earlier reply in the same group")

const chatInPreemptedReason = "这一轮主动接话生成期间，机器人已经在这个群发过话，这条不再发送"

// groupOwnSendClock 记每台机器人在每个群最近一次发出主动接话的时间。零值可用。
type groupOwnSendClock struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func groupOwnSendKey(event MessageEvent) string {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return ""
	}
	return strings.TrimSpace(event.ProfileID) + "|" + sessionKey(event)
}

func (c *groupOwnSendClock) note(event MessageEvent, at time.Time) {
	key := groupOwnSendKey(event)
	if key == "" || !event.chatInReply {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last = make(map[string]time.Time)
	}
	if at.After(c.last[key]) {
		c.last[key] = at
	}
}

func (c *groupOwnSendClock) lastSent(event MessageEvent) time.Time {
	key := groupOwnSendKey(event)
	if key == "" {
		return time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last[key]
}

// chatInPreempted 判断这条主动接话该不该在发送前放弃：只管没人叫到的接话，
// 只看这一轮的第一条（分几条发的后几条不会被自己的前一条拦住）。
func (r *Runtime) chatInPreempted(ctx context.Context, event MessageEvent) bool {
	if event.Kind != EventKindGroup || !event.chatInReply {
		return false
	}
	if turn := outboundTurnFromContext(ctx); turn != nil && turn.sentMessages.Load() > 0 {
		return false
	}
	start, ok := replyTurnStartFromContext(ctx)
	if !ok {
		return false
	}
	return r.groupOwnSends.lastSent(event).After(start)
}
