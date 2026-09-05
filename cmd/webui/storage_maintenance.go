// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"context"
	"log"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

// startStorageMaintenance returns a stop function that joins the worker before
// its database is closed. A timeout bounds catch-up work on large old databases.
func startStorageMaintenance(parent context.Context, store *storage.SQLiteStore, cfg storageConfig) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			if err := assistant.CleanupMediaDownloadCache(); err != nil {
				log.Printf("storage maintenance: download cache cleanup: %v", err)
			}
			now := time.Now()
			runCtx, stop := context.WithTimeout(ctx, 2*time.Minute)
			count, err := store.PruneLogs(runCtx,
				logRetentionCutoff(now, cfg.DebugLogRetentionDays, 7),
				logRetentionCutoff(now, cfg.LogRetentionDays, 30))
			stop()
			if err != nil && ctx.Err() == nil {
				log.Printf("storage maintenance: deleted %d expired logs: %v", count, err)
			} else if count > 0 {
				log.Printf("storage maintenance: deleted %d expired logs; freed database pages can be reused", count)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func logRetentionCutoff(now time.Time, days, fallback int) time.Time {
	if days < 0 {
		return time.Time{}
	}
	if days == 0 {
		days = fallback
	}
	return now.AddDate(0, 0, -days)
}
