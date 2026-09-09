package storage

import (
	"context"
	"database/sql"
	"log"
	"time"
)

const slowStorageThreshold = 250 * time.Millisecond

// Write to the process log, never app_logs: a blocked database must not be
// needed to explain its own failure. No SQL parameters or message bodies leak.
func (s *SQLiteStore) observeStorage(ctx context.Context, operation string, pool string) func() {
	if s == nil || s.db == nil {
		return func() {}
	}
	db := s.db
	if pool == "read" {
		db = s.eventReader()
	}
	before := db.Stats()
	started := time.Now()
	return func() {
		elapsed := time.Since(started)
		if elapsed < slowStorageThreshold && ctx.Err() == nil {
			return
		}
		after := db.Stats()
		log.Printf("diana sqlite slow operation=%s pool=%s elapsed_ms=%d pool_wait_count_delta=%d pool_wait_ms_delta=%d in_use=%d idle=%d context_error=%v", operation, pool, elapsed.Milliseconds(), after.WaitCount-before.WaitCount, (after.WaitDuration - before.WaitDuration).Milliseconds(), after.InUse, after.Idle, ctx.Err())
	}
}

func (s *SQLiteStore) beginWriteTx(ctx context.Context, operation string) (*sql.Tx, error) {
	defer s.observeStorage(ctx, operation+".begin_transaction", "write")()
	return s.db.BeginTx(ctx, nil)
}

func observeTransaction(operation string) func() {
	started := time.Now()
	return func() {
		if elapsed := time.Since(started); elapsed >= slowStorageThreshold {
			log.Printf("diana sqlite slow transaction=%s elapsed_ms=%d", operation, elapsed.Milliseconds())
		}
	}
}
