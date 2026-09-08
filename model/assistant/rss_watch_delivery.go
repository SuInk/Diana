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
