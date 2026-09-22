package assistant

import (
	"context"
	"errors"
)

// Persist successful targets so a retry after one platform fails does not
// resend the same update to all the other subscribers.
func (r *Runtime) sendRSSWatchTargets(ctx context.Context, item Reminder, message string) error {
	completed := make(map[string]bool, len(item.PendingDeliveredTargets))
	for _, key := range item.PendingDeliveredTargets {
		completed[key] = true
	}
	var failures []error
	for _, target := range repositoryWatchDeliveryTargets(item) {
		key := messageEventDeliveryKey(target)
		if completed[key] {
			continue
		}
		if err := r.sendSubscriberNotice(ctx, target, message); err != nil {
			if errors.Is(err, ErrDeliveryTargetDisabled) {
				// 机器人停用是长期状态，重试不会好。跳过这个目标，别让它把整条
				// 订阅拖成「连续失败」——重新启用之后下一轮自然会投。
				continue
			}
			failures = append(failures, err)
			continue
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
