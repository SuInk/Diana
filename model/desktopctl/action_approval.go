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

const CodeConfirmationRequired = "confirmation_required"

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

func actionKey(c Command) string {
	c.IdempotencyKey = ""
	return fmt.Sprintf("%x", sha256.Sum256(rawJSON(c)))
}
func (a *ProcessAdapter) pruneApprovals() {
	for id, p := range a.approvals {
		_, observed := a.observations[p.Command.Observation]
		if time.Now().After(p.ExpiresAt) || !observed {
			delete(a.approvals, id)
		}
	}
}

// Called while holding the adapter sequence gate, before sending any input.
func (a *ProcessAdapter) authorizeAction(c Command, preview ScreenshotPayload, elements []Element) error {
	a.pruneApprovals()
	id := actionKey(c)
	if p, ok := a.approvals[id]; ok && p.Approved {
		delete(a.approvals, id)
		return nil
	}
	if _, ok := a.approvals[id]; !ok {
		if len(a.approvals) >= 8 {
			return commandError(CodeRateLimited, "待确认操作已达上限")
		}
		label := ""
		for _, el := range elements {
			if el.ID == c.ElementID {
				label = el.Label
			}
		}
		a.approvals[id] = ActionApproval{ID: id, Command: c, TargetLabel: label, Preview: &preview, ExpiresAt: time.Now().Add(2 * time.Minute)}
	}
	return commandError(CodeConfirmationRequired, "操作尚未执行，请主人在桌面控制台检查并确认本次具体操作；确认后使用相同参数重试，任务步骤使用新的幂等键")
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
	return nil
}
func (a *ProcessAdapter) Invalidate(ctx context.Context) {
	if a.lockSequence(ctx) != nil {
		return
	}
	defer a.unlockSequence()
	a.approvals = map[string]ActionApproval{}
	a.observations = map[int64]processObservation{}
}
