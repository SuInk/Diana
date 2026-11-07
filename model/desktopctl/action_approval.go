// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"time"
)

const (
	CodeConfirmationRequired = "confirmation_required"
	// CodeConfirmationTimeout 表示主人在等待期内没有确认，输入没有发出。
	CodeConfirmationTimeout = "confirmation_timeout"
)

// ActionConfirmationWait 是写操作在执行器里等主人确认的最长时间。等待期间
// 调用方（Agent 工具）保持阻塞，主人确认后同一次调用当场复核画面并执行，
// 不必回到聊天里让模型用相同参数重试。
const ActionConfirmationWait = 5 * time.Minute

// observationTTL 是一次观察在没有待确认操作时的有效期。
const observationTTL = 2 * time.Minute

// ActionApproval is a one-shot grant bound to the exact command and observation.
// It is deliberately memory-only: reconnect/restart never restores a grant.
type ActionApproval struct {
	TargetLabel string             `json:"target_label,omitempty"`
	Preview     *ScreenshotPayload `json:"preview,omitempty"`
	ID          string             `json:"id"`
	Command     Command            `json:"command"`
	ExpiresAt   time.Time          `json:"expires_at"`
	Approved    bool               `json:"approved"`
}

// ConfirmationRequest 是新出现一条待确认操作时交给通知方的摘要，不带截图。
type ConfirmationRequest struct {
	ID          string
	Command     Command
	TargetLabel string
	// AppName 由 Hub 按窗口清单补上；执行器只知道 Bundle ID。
	AppName   string
	ExpiresAt time.Time
}

// ConfirmationNotifier 在新的待确认操作出现时被调用一次。它运行在独立
// goroutine 里，不能阻塞执行器，也不能替主人确认。
type ConfirmationNotifier func(ctx context.Context, req ConfirmationRequest)

type confirmationNotifierKey struct{}

// WithConfirmationNotifier 让这次 Dispatch 产生的待确认操作通知 fn。
// 本机连接沿用调用方的 context，所以通知方能知道是哪台机器人发起的。
func WithConfirmationNotifier(ctx context.Context, fn ConfirmationNotifier) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, confirmationNotifierKey{}, fn)
}

// ConfirmationNotifierFrom 取出 WithConfirmationNotifier 挂上的通知方。
func ConfirmationNotifierFrom(ctx context.Context) ConfirmationNotifier {
	fn, _ := ctx.Value(confirmationNotifierKey{}).(ConfirmationNotifier)
	return fn
}

func actionKey(c Command) string {
	c.IdempotencyKey = ""
	return fmt.Sprintf("%x", sha256.Sum256(rawJSON(c)))
}

// signalLocked wakes every caller waiting for a confirmation so it re-checks
// the approval, the observation and the scene. Caller holds the sequence gate.
func (a *ProcessAdapter) signalLocked() {
	close(a.changed)
	a.changed = make(chan struct{})
}

func (a *ProcessAdapter) pruneApprovals() {
	for id, p := range a.approvals {
		_, observed := a.observations[p.Command.Observation]
		if time.Now().After(p.ExpiresAt) || !observed {
			delete(a.approvals, id)
		}
	}
}

// pinned reports whether a live approval still refers to observation id, which
// keeps that observation valid past observationTTL while the owner decides.
func (a *ProcessAdapter) pinned(id int64) bool {
	now := time.Now()
	for _, p := range a.approvals {
		if p.Command.Observation == id && now.Before(p.ExpiresAt) {
			return true
		}
	}
	return false
}

// authorizeAction is called while holding the adapter sequence gate, before
// sending any input. It returns nil when an approved grant was consumed. When
// the command still needs the owner, it returns wait=true together with the
// moment the grant expires; created reports a newly registered request.
func (a *ProcessAdapter) authorizeAction(c Command, preview ScreenshotPayload, elements []Element, waited bool) (wait bool, created *ActionApproval, err error) {
	a.pruneApprovals()
	id := actionKey(c)
	if p, ok := a.approvals[id]; ok {
		if p.Approved {
			delete(a.approvals, id)
			return false, nil, nil
		}
		if a.confirmWait > 0 {
			return true, nil, nil
		}
		return false, nil, a.confirmationRequired()
	}
	if waited {
		// The request this call registered is gone: it expired, or a new
		// observation of the window replaced the one it was bound to.
		if _, ok := a.observations[c.Observation]; !ok {
			return false, nil, commandError(CodeStaleObservation, "等待确认期间窗口有了新的观察或执行器重连，操作没有执行，请重新观察")
		}
		return false, nil, commandError(CodeConfirmationTimeout, "主人没有在 %s 内确认，操作没有执行；需要的话重新观察后再发起", formatWait(a.confirmWait))
	}
	if len(a.approvals) >= 8 {
		return false, nil, commandError(CodeRateLimited, "待确认操作已达上限")
	}
	label := ""
	for _, el := range elements {
		if el.ID == c.ElementID {
			label = el.Label
		}
	}
	// A waiting caller gives the owner exactly confirmWait; the two-step retry
	// flow keeps the original two-minute grant.
	ttl := a.confirmWait
	if ttl <= 0 {
		ttl = observationTTL
	}
	p := ActionApproval{ID: id, Command: c, TargetLabel: label, Preview: &preview, ExpiresAt: time.Now().Add(ttl)}
	a.approvals[id] = p
	if a.confirmWait > 0 {
		return true, &p, nil
	}
	return false, &p, a.confirmationRequired()
}

func (a *ProcessAdapter) confirmationRequired() error {
	return commandError(CodeConfirmationRequired, "操作尚未执行，请主人在桌面控制台检查并确认本次具体操作；确认后使用相同参数重试，任务步骤使用新的幂等键")
}

func formatWait(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d 分钟", int(d/time.Minute))
	}
	return d.String()
}

func (a *ProcessAdapter) PendingActions(ctx context.Context) ([]ActionApproval, error) {
	if err := a.lockSequence(ctx); err != nil {
		return nil, err
	}
	defer a.unlockSequence()
	a.pruneApprovals()
	out := make([]ActionApproval, 0, len(a.approvals))
	for _, p := range a.approvals {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExpiresAt.Before(out[j].ExpiresAt) })
	return out, nil
}
func (a *ProcessAdapter) ConfirmAction(ctx context.Context, id string) error {
	if err := a.lockSequence(ctx); err != nil {
		return err
	}
	defer a.unlockSequence()
	a.pruneApprovals()
	p, ok := a.approvals[id]
	if !ok {
		return commandError(CodeStaleObservation, "操作确认已过期，请重新观察")
	}
	if _, ok := a.observations[p.Command.Observation]; !ok {
		delete(a.approvals, id)
		return commandError(CodeStaleObservation, "观察已失效，请重新观察")
	}
	p.Approved = true
	a.approvals[id] = p
	a.signalLocked()
	return nil
}
func (a *ProcessAdapter) Invalidate(ctx context.Context) {
	if a.lockSequence(ctx) != nil {
		return
	}
	defer a.unlockSequence()
	a.approvals = map[string]ActionApproval{}
	a.observations = map[int64]processObservation{}
	a.signalLocked()
}
