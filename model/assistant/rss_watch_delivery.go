package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// Persist successful targets so a retry after one platform fails does not
// resend the same update to all the other subscribers.
func (r *Runtime) sendRSSWatchTargets(ctx context.Context, item Reminder, message string) error {
	completed := make(map[string]bool, len(item.PendingDeliveredTargets))
	for _, key := range item.PendingDeliveredTargets {
		completed[key] = true
	}
	var failures []error
	ctx = withSubscriptionPush(ctx, subscriptionPushRSSWatch)
	for _, target := range repositoryWatchDeliveryTargets(item) {
		key := messageEventDeliveryKey(target)
		if completed[key] {
			continue
		}
		if err := r.sendSubscriberNotice(withDeliveryProgress(ctx, r.rssWatchDeliveryProgress(item, key)), target, message); err != nil {
			if errors.Is(err, ErrDeliveryTargetDisabled) {
				// 机器人停用是长期状态，重试不会好。跳过这个目标，别让它把整条
				// 订阅拖成「连续失败」——重新启用之后下一轮自然会投。
				continue
			}
			if !outboundDeliveryOutcomeUnconfirmed(err) {
				failures = append(failures, err)
				continue
			}
		}
		completed[key] = true
		if r.reminders == nil {
			continue
		}
		_, err := r.mutateRSSWatch(item.OwnerID, item.ID, func(saved *Reminder) error {
			saved.PendingDeliveredTargets = append(saved.PendingDeliveredTargets, key)
			return nil
		})
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(failures...)
}

// rssWatchDeliveryProgress 让这个目标的补投跳过上一轮已经送达的分段。分段进度和
// 目标进度存在同一个列表里，写成「目标键#分段键」；新内容入库时整表清空。
func (r *Runtime) rssWatchDeliveryProgress(item Reminder, targetKey string) *deliveryProgress {
	prefix := targetKey + "#"
	var done []string
	for _, key := range item.PendingDeliveredTargets {
		if step, ok := strings.CutPrefix(key, prefix); ok {
			done = append(done, step)
		}
	}
	return newDeliveryProgress(done, func(step string) error {
		if r.reminders == nil {
			return nil
		}
		_, err := r.mutateRSSWatch(item.OwnerID, item.ID, func(saved *Reminder) error {
			saved.PendingDeliveredTargets = append(saved.PendingDeliveredTargets, prefix+step)
			return nil
		})
		return err
	})
}

// rssWatchMaxDeliveryAttempts 是同一份推送最多投几次。三次都发不出去多半是内容本身
// 被平台拒收，再补投只会卡住这条订阅：有待投内容时不会去抓新条目。
const rssWatchMaxDeliveryAttempts = 3

// dropRSSWatchPendingAfterMaxAttempts 在一轮失败后给待投内容记一次失败，到上限就丢弃，
// 下一轮照常抓新条目。游标在内容入库时已经推进过，丢弃不会让同一条再被抓出来。
// 连续失败次数和错误照留，失败告警照常按阈值报。
func dropRSSWatchPendingAfterMaxAttempts(item *Reminder) {
	if !reminderIsRSSWatch(*item) || strings.TrimSpace(item.PendingDelivery) == "" {
		return
	}
	item.PendingDeliveryAttempts++
	if item.PendingDeliveryAttempts < rssWatchMaxDeliveryAttempts {
		return
	}
	log.Printf("diana rss_watch %s pending delivery dropped after %d failed attempts: %s", item.ID, item.PendingDeliveryAttempts, item.LastError)
	item.LastError = fmt.Sprintf("推送连续 %d 次发送失败，已放弃这一条：%s", item.PendingDeliveryAttempts, item.LastError)
	item.PendingDelivery = ""
	item.PendingDeliveredTargets = nil
	item.PendingDeliveryAttempts = 0
	item.PendingSince = time.Time{}
}
