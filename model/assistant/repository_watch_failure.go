// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
)

const repositoryWatchFailureAlertThreshold = 3

const (
	repositoryWatchFailureStagePolling  = "polling"
	repositoryWatchFailureStageSummary  = "summary"
	repositoryWatchFailureStageState    = "state"
	repositoryWatchFailureStageDelivery = "delivery"
)

type repositoryWatchRunError struct {
	Stage string
	Err   error
}

func (e *repositoryWatchRunError) Error() string {
	if e == nil || e.Err == nil {
		return "repository watch failed"
	}
	return e.Err.Error()
}

func (e *repositoryWatchRunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func repositoryWatchStageFailure(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &repositoryWatchRunError{Stage: stage, Err: err}
}

func repositoryWatchFailureDetails(err error) (stage, fingerprint, publicReason string) {
	stage = repositoryWatchFailureStagePolling
	var staged *repositoryWatchRunError
	if errors.As(err, &staged) && strings.TrimSpace(staged.Stage) != "" {
		stage = staged.Stage
	}
	publicReason = publicChatErrorMessage(err)
	sum := sha256.Sum256([]byte(stage + "\x00" + publicReason))
	return stage, fmt.Sprintf("%x", sum[:]), publicReason
}

func updateRepositoryWatchFailureState(item *Reminder, cause error) {
	stage, fingerprint, _ := repositoryWatchFailureDetails(cause)
	if item.LastFailureStage != stage || item.LastErrorFingerprint != fingerprint {
		item.ConsecutiveFailures = 0
		item.FailureAlertedAt = time.Time{}
	}
	item.LastError = cause.Error()
	item.LastFailureStage = stage
	item.LastErrorFingerprint = fingerprint
	item.ConsecutiveFailures++
	item.RecoveryNoticePending = false
}

func repositoryWatchFailureShouldAlert(item Reminder) bool {
	return reminderIsRepositoryWatch(item) &&
		item.ConsecutiveFailures >= repositoryWatchFailureAlertThreshold &&
		strings.TrimSpace(item.LastErrorFingerprint) != "" &&
		item.FailureAlertedAt.IsZero()
}

func resetRepositoryWatchFailureStateAfterSuccess(item *Reminder) {
	hadAcknowledgedAlert := !item.FailureAlertedAt.IsZero()
	item.LastFailureStage = ""
	item.LastErrorFingerprint = ""
	item.FailureAlertedAt = time.Time{}
	item.RecoveryNoticePending = item.RecoveryNoticePending || hadAcknowledgedAlert
}

func clearRepositoryWatchFailureState(item *Reminder) {
	item.LastError = ""
	item.ConsecutiveFailures = 0
	item.LastFailureStage = ""
	item.LastErrorFingerprint = ""
	item.FailureAlertedAt = time.Time{}
	item.RecoveryNoticePending = false
}

func repositoryWatchFailureStageLabel(stage string) string {
	switch stage {
	case repositoryWatchFailureStageDelivery:
		return "消息发送"
	case repositoryWatchFailureStageSummary:
		return "更新摘要生成"
	case repositoryWatchFailureStageState:
		return "订阅状态保存"
	default:
		return "仓库更新检查"
	}
}

func (r *Runtime) notifyRepositoryWatchFailure(ctx context.Context, item Reminder, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	stage, _, reason := repositoryWatchFailureDetails(cause)
	notice := fmt.Sprintf(
		"仓库订阅 %s 连续 %d 次%s失败：%s Diana 会继续自动重试。",
		item.Repository,
		item.ConsecutiveFailures,
		repositoryWatchFailureStageLabel(stage),
		reason,
	)
	acknowledged := false
	var firstErr error
	for _, target := range repositoryWatchDeliveryTargets(item) {
		_, delivered, err := r.sendErrorNoticeWithEvidence(ctx, target, notice)
		if delivered {
			acknowledged = true
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if !acknowledged {
		return fmt.Errorf("仓库订阅失败告警未取得发送确认")
	}
	return nil
}

func (r *Runtime) acknowledgeRepositoryWatchFailureAlert(id, fingerprint string, alertedAt time.Time) (Reminder, error) {
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	for index := range items {
		item := &items[index]
		if item.ID != id || !reminderIsRepositoryWatch(*item) {
			continue
		}
		if item.LastErrorFingerprint != fingerprint || item.ConsecutiveFailures < repositoryWatchFailureAlertThreshold {
			return *item, fmt.Errorf("仓库订阅 %s 的失败状态已变化", id)
		}
		if item.FailureAlertedAt.IsZero() {
			item.FailureAlertedAt = alertedAt
		}
		if err := r.reminders.SaveReminders(items); err != nil {
			return *item, fmt.Errorf("保存仓库订阅失败告警状态: %w", err)
		}
		return *item, nil
	}
	return Reminder{}, fmt.Errorf("没有找到仓库更新订阅 %s", id)
}

func (r *Runtime) notifyRepositoryWatchRecovery(ctx context.Context, item Reminder) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	notice := fmt.Sprintf("仓库订阅 %s 已恢复，后续更新将继续正常推送。", item.Repository)
	if err := r.sendRepositoryWatch(ctx, item, notice); err != nil {
		return err
	}
	return nil
}

func (r *Runtime) clearRepositoryWatchRecoveryNotice(id string) error {
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	for index := range items {
		item := &items[index]
		if item.ID != id || !reminderIsRepositoryWatch(*item) {
			continue
		}
		item.RecoveryNoticePending = false
		if err := r.reminders.SaveReminders(items); err != nil {
			return fmt.Errorf("保存仓库订阅恢复通知状态: %w", err)
		}
		return nil
	}
	return fmt.Errorf("没有找到仓库更新订阅 %s", id)
}
