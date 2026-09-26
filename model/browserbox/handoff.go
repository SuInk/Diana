// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 机器人请主人亲手在内置浏览器里做一步：登录、扫码、输验证码、过人机验证。
//
// 做法照 Cloudflare Browser Run 的 handoff 和 OpenAI Operator：机器人说明要人做什么，
// 人接手做完点「完成」（做不了点「做不了」），机器人再接着往下做。不同的是一轮对话最多
// 跑几分钟，不能让工具一直等着人——所以这里只登记请求、到时候回调，机器人那一轮先结束，
// 结果出来时由调用方（model/assistant）在原来的对话里再跑一轮。

// 交接的结果。
const (
	// HandoffDone 是主人做完了、点了「完成，交还给机器人」。
	HandoffDone = "done"
	// HandoffFailed 是主人点了「做不了」。
	HandoffFailed = "failed"
	// HandoffExpired 是等满期限没人处理。
	HandoffExpired = "expired"
	// HandoffCancelled 是被新的交接顶掉、或浏览器停了：不再叫醒机器人。
	HandoffCancelled = "cancelled"
)

const (
	// DefaultHandoffTimeout 是默认等多久。登录、扫码、等短信验证码，一刻钟够了。
	DefaultHandoffTimeout = 15 * time.Minute
	// MaxHandoffTimeout 是最多等多久，和 Cloudflare 的上限一样。
	MaxHandoffTimeout = 30 * time.Minute
)

// Handoff 是一条等主人处理的交接，WebUI 据此显示「机器人请你帮忙」。
type Handoff struct {
	ID          string    `json:"id"`
	Reason      string    `json:"reason"`
	RequestedAt time.Time `json:"requested_at"`
	Deadline    time.Time `json:"deadline"`
}

type pendingHandoff struct {
	info   Handoff
	done   func(outcome string)
	expire *time.Timer
}

// RequestHandoff 登记一条交接，返回它的 ID。同一台机器人同时只有一条：新的来了，旧的
// 按「取消」收尾——机器人已经换了想法，旧的那一步不用再做。done 在交接有了结果之后
// 在单独的 goroutine 里调用一次。
func (b *Bot) RequestHandoff(reason string, timeout time.Duration, done func(outcome string)) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", errors.New("要说清楚请主人做什么")
	}
	if !b.m.Settings().Enabled {
		return "", errors.New("内置浏览器没有打开，没法请主人在里面操作")
	}
	if timeout <= 0 {
		timeout = DefaultHandoffTimeout
	}
	if timeout > MaxHandoffTimeout {
		timeout = MaxHandoffTimeout
	}
	now := b.m.now()
	next := &pendingHandoff{
		info: Handoff{ID: uuid.NewString(), Reason: reason, RequestedAt: now, Deadline: now.Add(timeout)},
		done: done,
	}
	b.m.mu.Lock()
	inst := b.m.instanceLocked(b.id)
	previous := inst.handoff
	inst.handoff = next
	id := next.info.ID
	next.expire = time.AfterFunc(timeout, func() {
		defer recoverGoroutinePanic("handoffExpire")
		b.ResolveHandoff(id, HandoffExpired)
	})
	b.m.mu.Unlock()
	if previous != nil {
		previous.finish(HandoffCancelled)
	}
	b.m.notify()
	return id, nil
}

// PendingHandoff 返回正在等主人处理的那条交接。
func (b *Bot) PendingHandoff() (Handoff, bool) {
	b.m.mu.RLock()
	defer b.m.mu.RUnlock()
	if inst := b.m.bots[b.id]; inst != nil && inst.handoff != nil {
		return inst.handoff.info, true
	}
	return Handoff{}, false
}

// ResolveHandoff 给交接一个结果。id 不是当前那条（已经处理过、被顶掉了）时什么都不做，
// 返回 false。完成和做不了都顺手交还接管：人已经说了「好了」，浏览器该回到机器人手里。
func (b *Bot) ResolveHandoff(id, outcome string) bool {
	b.m.mu.Lock()
	inst := b.m.bots[b.id]
	if inst == nil || inst.handoff == nil || inst.handoff.info.ID != id {
		b.m.mu.Unlock()
		return false
	}
	pending := inst.handoff
	inst.handoff = nil
	releasing := inst.takeover && (outcome == HandoffDone || outcome == HandoffFailed)
	b.m.mu.Unlock()
	if releasing {
		b.SetTakeover(false)
	}
	pending.finish(outcome)
	b.m.notify()
	return true
}

// cancelHandoffLocked 在浏览器停掉时收掉等着的交接。调用方持有写锁。
func cancelHandoffLocked(inst *instance) *pendingHandoff {
	pending := inst.handoff
	inst.handoff = nil
	return pending
}

func (p *pendingHandoff) finish(outcome string) {
	if p == nil {
		return
	}
	if p.expire != nil {
		p.expire.Stop()
	}
	if p.done != nil {
		go func() {
			defer recoverGoroutinePanic("handoffDone")
			p.done(outcome)
		}()
	}
}
