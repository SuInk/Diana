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
		defer recoverGoroutinePanic("storage_maintenance.go:20")
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
			if result, err := assistant.CleanupHistoryMedia(); err != nil {
				log.Printf("storage maintenance: history media cleanup: %v", err)
			} else if result.DeletedFiles > 0 {
				log.Printf("storage maintenance: deleted %d history media files (%d bytes)", result.DeletedFiles, result.DeletedBytes)
			}
			// 日志动作名改写成新名字的迁移在这里分批做，做完之前不清理日志，理由见 PruneLogs。
			migrateCtx, stopMigrate := context.WithTimeout(ctx, 10*time.Minute)
			if _, err := store.MigrateLogActionNames(migrateCtx); err != nil && ctx.Err() == nil {
				log.Printf("storage maintenance: log action rename: %v", err)
			}
			stopMigrate()
			now := time.Now()
			draftCtx, stopDrafts := context.WithTimeout(ctx, time.Minute)
			if count, err := store.PruneRepositoryIssueDrafts(draftCtx, assistant.RepositoryIssueDraftPurgeCutoff(now)); err != nil && ctx.Err() == nil {
				log.Printf("storage maintenance: prune repository issue drafts: %v", err)
			} else if count > 0 {
				log.Printf("storage maintenance: deleted %d expired repository issue drafts", count)
			}
			stopDrafts()
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
