// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"time"
)

// PruneLogs deletes expired logs in small transactions. Zero cutoffs disable
// that category. Usage records remain available for historical token statistics.
func (s *SQLiteStore) PruneLogs(ctx context.Context, debugBefore, otherBefore time.Time) (int64, error) {
	var deleted int64
	for _, policy := range []struct {
		predicate string
		before    time.Time
	}{
		{"kind = 'debug'", debugBefore},
		{"kind != 'debug'", otherBefore},
	} {
		if policy.before.IsZero() {
			continue
		}
		// Whole-second cutoffs avoid RFC3339Nano's variable fractional width.
		cutoff := policy.before.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05")
		for {
			result, err := s.db.ExecContext(ctx, `DELETE FROM app_logs WHERE id IN (
SELECT id FROM app_logs WHERE `+policy.predicate+` AND created_at < ?
AND action NOT IN ('assistant.llm_usage', 'chatbot.llm_usage', 'diana.llm_usage')
ORDER BY created_at LIMIT 500)`, cutoff)
			if err != nil {
				return deleted, fmt.Errorf("prune logs: %w", err)
			}
			n, err := result.RowsAffected()
			deleted += n
			if err != nil {
				return deleted, err
			}
			if n < 500 {
				break
			}
			// Let normal requests use the single database connection between batches.
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return deleted, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return deleted, nil
}
