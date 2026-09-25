// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"time"
)

// 周期订阅跑在不可靠的网络上：一次 TLS 握手超时、一次 502，下个周期多半自己就好了。
// 每次都往群里丢一条「本次执行失败」，真正的故障反而淹在噪音里，久了没人再看这类消息。
// 所以周期订阅（RSS、定时查询、仓库更新）连续失败到阈值才报一次，而且一轮故障只报一次；
// 一次性提醒仍然立刻报——它没有「下个周期」，不报就等于这条提醒悄悄丢了。
//
// 仓库订阅早就是这个规矩，这里把同一套判断推广到其余周期订阅上，阈值也共用一个。

const defaultRecurringFailureAlertThreshold = 5

// recurringFailureAlertSettingKey 是 RSS 订阅、仓库订阅两个插件里「连续失败几次才报」
// 那一项的键。
const recurringFailureAlertSettingKey = "failure_alert_threshold"

// recurringFailureAlertThreshold 取这条订阅生效的告警阈值，0 表示不报。
//
// 报不报跟着机器人的「出错时在聊天里提示」和插件自己的「发送错误通知」走：任一关了，
// 订阅坏了也不在聊天里说——另起一个开关分开管，关掉一个另一个还在响，用户只会以为关不掉。
// 连续失败几次才报是订阅自己的事，在各自插件的设置里改，可以按群覆盖；定时查询
// 没有插件，用默认值。
func (r *Runtime) recurringFailureAlertThreshold(item Reminder) int {
	event := reminderSourceEvent(item)
	pluginID := reminderDiagnosticPluginID(item)
	// 和发送出口用同一道判断：机器人的「错误提示」和插件的「发送错误通知」任一关着，
	// 就不计数也不告警，免得记下「报过警」却什么也没发，恢复时凭空冒出一句「好了」。
	if !r.diagnosticAllowed(event, pluginID) {
		return 0
	}
	threshold := defaultRecurringFailureAlertThreshold
	if pluginID != "" && r.plugins != nil {
		_, settings, _ := r.pluginWithSettingsForEvent(pluginID, event)
		threshold = settings.Int(recurringFailureAlertSettingKey, defaultRecurringFailureAlertThreshold)
	}
	return min(max(threshold, 1), maxRecurringFailureAlertThreshold)
}

// recurringFailureShouldAlert 判断这次失败要不要打扰订阅者。
func recurringFailureShouldAlert(item Reminder, threshold int) bool {
	if threshold <= 0 {
		// 机器人关了「出错时在聊天里提示」：一次性提醒失败也不说。
		return false
	}
	if reminderIsRepositoryWatch(item) {
		return repositoryWatchFailureShouldAlert(item, threshold)
	}
	if !reminderIsRecurring(item) {
		// 一次性提醒：失败即报。它没有「下个周期」，阈值管不到它——
		// 不报就等于这条提醒悄悄丢了。
		return true
	}
	return threshold > 0 && item.ConsecutiveFailures >= threshold && item.FailureAlertedAt.IsZero()
}

// resetRecurringFailureStateAfterSuccess 一次成功就把失败流水清掉；如果这轮故障确实报过警，
// 留下「该说一声恢复了」的标记，免得订阅者只收到坏消息、不知道什么时候好的。
func resetRecurringFailureStateAfterSuccess(item *Reminder) {
	hadAcknowledgedAlert := !item.FailureAlertedAt.IsZero()
	item.LastFailureStage = ""
	item.LastErrorFingerprint = ""
	item.FailureAlertedAt = time.Time{}
	item.RecoveryNoticePending = item.RecoveryNoticePending || hadAcknowledgedAlert
}

// recurringSubscriptionKindLabel 给通知里的订阅起个人话名字。
// 不带订阅 ID：这些通知会发到群里，ID 对群友没有意义。
func recurringSubscriptionKindLabel(item Reminder) string {
	if reminderIsRSSWatch(item) {
		return "RSS 订阅"
	}
	return "定时订阅"
}

// markReminderFailureAlerted 记下「这轮故障已经报过警了」，让后续失败保持安静。
// 只有当失败状态没变（仍然连续失败到阈值、还没标记过）时才落库，
// 否则说明中途已经恢复又坏了，那是新的一轮，应当重新计数。
func (r *Runtime) markReminderFailureAlerted(id string, threshold int, alertedAt time.Time) (Reminder, error) {
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	for index := range items {
		item := &items[index]
		if item.ID != id || !reminderIsRecurring(*item) {
			continue
		}
		if item.ConsecutiveFailures < threshold {
			return *item, fmt.Errorf("周期订阅 %s 的失败状态已变化", id)
		}
		if item.FailureAlertedAt.IsZero() {
			item.FailureAlertedAt = alertedAt
		}
		if err := r.reminders.SaveReminders(items); err != nil {
			return *item, fmt.Errorf("保存周期订阅失败告警状态: %w", err)
		}
		return *item, nil
	}
	return Reminder{}, fmt.Errorf("没有找到周期订阅 %s", id)
}

// notifyRecurringFailureRecovery 在报过警的订阅恢复后说一声。
func (r *Runtime) notifyRecurringFailureRecovery(ctx context.Context, item Reminder) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	notice := fmt.Sprintf("%s已恢复，后续结果会继续正常发送。", recurringSubscriptionKindLabel(item))
	return r.sendDiagnosticNotice(ctx, reminderSourceEvent(item), reminderDiagnosticPluginID(item), notice)
}

// clearReminderRecoveryNotice 把「待发恢复通知」标记落下去，避免重复通知。
func (r *Runtime) clearReminderRecoveryNotice(id string) error {
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	for index := range items {
		item := &items[index]
		if item.ID != id || !reminderIsRecurring(*item) {
			continue
		}
		item.RecoveryNoticePending = false
		if err := r.reminders.SaveReminders(items); err != nil {
			return fmt.Errorf("保存周期订阅恢复通知状态: %w", err)
		}
		return nil
	}
	return fmt.Errorf("没有找到周期订阅 %s", id)
}

// reportRecurringReminderFailure 处理一次周期订阅失败：够不够报警、报了要不要标记、
// 以及无论报没报都得留下的运行日志。日志一直写，安静的是群里那条消息，不是排查线索。
func (r *Runtime) reportRecurringReminderFailure(ctx context.Context, item Reminder, cause error) {
	var noticeErr error
	noticeAttempted := false
	threshold := r.recurringFailureAlertThreshold(item)
	if ctx.Err() == nil && recurringFailureShouldAlert(item, threshold) {
		noticeAttempted = true
		noticeErr = r.notifyReminderFailure(ctx, item, cause)
		if noticeErr == nil {
			// 发出去了才记标记：发送失败时留着零值，下个周期还会再试一次。
			if alerted, markErr := r.markReminderFailureAlerted(item.ID, threshold, time.Now()); markErr != nil {
				noticeErr = markErr
			} else {
				item = alerted
			}
		}
	}
	r.recordReminderRetryAttempt(item, cause, noticeErr, noticeAttempted)
}

// deliverRecurringRecoveryNotice 在报过警的订阅重新跑通后补一条恢复通知。
func (r *Runtime) deliverRecurringRecoveryNotice(ctx context.Context, item Reminder) {
	if !item.RecoveryNoticePending || ctx.Err() != nil {
		return
	}
	if err := r.notifyRecurringFailureRecovery(ctx, item); err != nil {
		r.setError(err.Error())
		return
	}
	if err := r.clearReminderRecoveryNotice(item.ID); err != nil {
		r.setError(err.Error())
	}
}
