// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

// startStorageMaintenance returns a stop function that joins the worker before
// its database is closed. A timeout bounds catch-up work on large old databases.
//
// codingReferenced 取「coding/<name> 还有没有机器人在用」的判断，给工作目录清理报告
// 闲置编码工作区；运行时还没建好时返回 nil，那一轮就不报。
func startStorageMaintenance(parent context.Context, store *storage.SQLiteStore, cfg storageConfig, codingReferenced func() func(string) bool) func() {
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
			runWorkspaceMaintenance(ctx, store, time.Now(), codingReferenced)
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
			// 旧版本把视觉模型「我没收到图片」的拒答当成图片描述缓存了下来，缓存命中
			// 不会再调模型，那些图会一直被描述成没收到。删掉让它们重新识别，只跑一次。
			purgeCtx, stopPurge := context.WithTimeout(ctx, 2*time.Minute)
			if count, err := store.PurgeRefusedImageDescriptions(purgeCtx); err != nil && ctx.Err() == nil {
				log.Printf("storage maintenance: purge refused image descriptions: %v", err)
			} else if count > 0 {
				log.Printf("storage maintenance: deleted %d cached vision refusals; those images will be described again", count)
			}
			stopPurge()
			now := time.Now()
			draftCtx, stopDrafts := context.WithTimeout(ctx, time.Minute)
			if count, err := store.PruneRepositoryIssueDrafts(draftCtx, assistant.RepositoryIssueDraftPurgeCutoff(now)); err != nil && ctx.Err() == nil {
				log.Printf("storage maintenance: prune repository issue drafts: %v", err)
			} else if count > 0 {
				log.Printf("storage maintenance: deleted %d expired repository issue drafts", count)
			}
			stopDrafts()
			// 好感度评估记录每条回复一条，和日志一样按普通日志的保留天数清理。
			evalCtx, stopEval := context.WithTimeout(ctx, 2*time.Minute)
			if count, err := store.PruneRelationshipEvaluations(evalCtx, logRetentionCutoff(now, cfg.LogRetentionDays, 30)); err != nil && ctx.Err() == nil {
				log.Printf("storage maintenance: prune relationship evaluations: %v", err)
			} else if count > 0 {
				log.Printf("storage maintenance: deleted %d expired relationship evaluations", count)
			}
			stopEval()
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
			if days, err := store.PruneDebugTraceFiles(logRetentionCutoff(now, cfg.DebugLogRetentionDays, 7)); err != nil {
				log.Printf("storage maintenance: prune debug trace files: %v", err)
			} else if days > 0 {
				log.Printf("storage maintenance: deleted %d days of expired debug trace files", days)
			}
			compressCtx, stopCompress := context.WithTimeout(ctx, 10*time.Minute)
			if count, err := store.CompressDebugTraceFiles(compressCtx, now); err != nil && ctx.Err() == nil {
				log.Printf("storage maintenance: compress debug trace files: %v", err)
			} else if count > 0 {
				log.Printf("storage maintenance: compressed %d debug trace files from earlier days", count)
			}
			stopCompress()
			jobsCtx, stopJobs := context.WithTimeout(ctx, 2*time.Minute)
			if count, err := store.PruneCompletedMemoryJobs(jobsCtx, now.AddDate(0, 0, -completedMemoryJobRetentionDays)); err != nil && ctx.Err() == nil {
				log.Printf("storage maintenance: prune completed memory jobs: %v", err)
			} else if count > 0 {
				log.Printf("storage maintenance: deleted %d completed memory jobs", count)
			}
			stopJobs()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

// completedMemoryJobRetentionDays 是已完成记忆任务留作入队去重的天数，理由见
// PruneCompletedMemoryJobs。
const completedMemoryJobRetentionDays = 7

func logRetentionCutoff(now time.Time, days, fallback int) time.Time {
	if days < 0 {
		return time.Time{}
	}
	if days == 0 {
		days = fallback
	}
	return now.AddDate(0, 0, -days)
}

// runWorkspaceMaintenance 清理 Agent 工作目录里过期的下载、产出、临时文件和回收站，
// 再扫掉系统临时目录里 Diana 残留的目录。第一轮在启动时就跑，于是也兼做启动清扫。
// 结果写进应用日志：删掉了什么、根下还散落着哪些没人整理的文件，主人在日志中心看得到。
func runWorkspaceMaintenance(ctx context.Context, logs applog.Writer, now time.Time, codingReferenced func() func(string) bool) {
	var referenced func(string) bool
	if codingReferenced != nil {
		referenced = codingReferenced()
	}
	report, err := assistant.CleanupAgentWorkspace(now, referenced)
	if err != nil {
		log.Printf("storage maintenance: workspace cleanup: %v", err)
	}
	sweep, sweepErr := assistant.SweepDianaTempDirs(now)
	if sweepErr != nil {
		log.Printf("storage maintenance: temp dir sweep: %v", sweepErr)
	}
	if report.DeletedFiles == 0 && sweep.Deleted == 0 && len(report.LooseFiles) == 0 && len(report.IdleCoding) == 0 {
		return
	}
	message := fmt.Sprintf("工作目录清理：删除 %d 个过期文件（%s），清掉 %d 个系统临时目录残留（%s）",
		report.DeletedFiles, formatMaintenanceBytes(report.DeletedBytes), sweep.Deleted, formatMaintenanceBytes(sweep.Bytes))
	if len(report.LooseFiles) > 0 {
		message += fmt.Sprintf("；根下散落 %d 个文件未整理（不会自动删除）", len(report.LooseFiles))
	}
	if len(report.IdleCoding) > 0 {
		message += fmt.Sprintf("；%d 个编码工作区长期未用且没有机器人引用（不会自动删除）", len(report.IdleCoding))
	}
	log.Printf("storage maintenance: %s", message)
	// 应用日志只记真删了东西的那几次：散落文件每天都在，天天记一条只会把日志中心刷满，
	// 它们在设置页的工作目录里一直看得到。
	if logs == nil || (report.DeletedFiles == 0 && sweep.Deleted == 0) {
		return
	}
	metadata := map[string]any{
		"deleted_files":      report.DeletedFiles,
		"deleted_bytes":      report.DeletedBytes,
		"by_area":            report.ByArea,
		"temp_deleted":       sweep.Deleted,
		"temp_deleted_bytes": sweep.Bytes,
	}
	if len(report.LooseFiles) > 0 {
		metadata["loose_files"] = maintenancePaths(report.LooseFiles)
	}
	if len(report.IdleCoding) > 0 {
		metadata["idle_coding"] = maintenancePaths(report.IdleCoding)
	}
	appendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := logs.AppendLog(appendCtx, applog.Entry{
		Kind:     applog.KindOperation,
		Level:    applog.LevelInfo,
		Action:   "workspace_cleanup",
		Message:  message,
		Target:   assistant.AgentWorkspaceDir(),
		Metadata: metadata,
	}); err != nil {
		log.Printf("storage maintenance: workspace cleanup log skipped: %v", err)
	}
}

// maintenancePaths 取报告里的路径，最多二十个：日志条目不是文件清单。
func maintenancePaths(items []agent.WorkspaceFileInfo) []string {
	out := make([]string, 0, min(len(items), 20))
	for _, item := range items {
		if len(out) >= 20 {
			break
		}
		out = append(out, item.Path)
	}
	return out
}

func formatMaintenanceBytes(value int64) string {
	switch {
	case value >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(value)/(1<<30))
	case value >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(value)/(1<<20))
	case value >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(value)/(1<<10))
	default:
		return fmt.Sprintf("%d B", value)
	}
}
