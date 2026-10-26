// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"time"

	"github.com/SuInk/diana/model/hostinfo"
)

// StorageCleanupReport 是主人手动触发的一次存储清理结果。
type StorageCleanupReport struct {
	FreedBytes       int64          `json:"freed_bytes"`
	Freed            string         `json:"freed"`
	Steps            map[string]any `json:"steps"`
	Errors           []string       `json:"errors,omitempty"`
	StoragePath      string         `json:"storage_path"`
	AvailableBefore  uint64         `json:"available_bytes_before,omitempty"`
	AvailableAfter   uint64         `json:"available_bytes_after,omitempty"`
	Available        string         `json:"available,omitempty"`
	StorageUsedPct   float64        `json:"storage_used_percent,omitempty"`
	LooseFiles       int            `json:"loose_files,omitempty"`
	IdleCodingSpaces int            `json:"idle_coding_workspaces,omitempty"`
	Note             string         `json:"note"`
}

// cleanupDianaStorage 立即跑一遍每日存储维护里只动 Diana 自己文件的那几步：下载缓存、
// Agent 工作目录的过期文件、系统临时目录残留、超出保留策略的历史媒体。
//
// 和每日维护用同一套规则（保留时长、容量上限），不会因为是手动触发就删得更狠：
// 主人要的是「现在就把该清的清掉」，不是另一套更激进的策略。数据库里的日志和调试
// 记录仍由每日维护按保留天数清理，删行也不会立刻缩小数据库文件，这里不碰。
func (r *Runtime) cleanupDianaStorage(ctx context.Context, now time.Time) StorageCleanupReport {
	report := StorageCleanupReport{Steps: map[string]any{}, StoragePath: AgentWorkspaceDir()}
	if _, _, available, err := hostinfo.StorageUsage(report.StoragePath); err == nil {
		report.AvailableBefore = available
	}
	fail := func(step string, err error) {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", step, err))
	}

	if dir, err := mediaDownloadCacheDir(); err != nil {
		fail("download_cache", err)
	} else {
		before := dirSize(dir)
		if err := CleanupMediaDownloadCache(); err != nil {
			fail("download_cache", err)
		}
		freed := max(before-dirSize(dir), 0)
		report.FreedBytes += freed
		report.Steps["download_cache"] = map[string]any{"freed_bytes": freed}
	}
	if ctx.Err() != nil {
		return report.finish(ctx.Err())
	}

	var referenced func(string) bool
	if r != nil {
		referenced = r.CodingWorkspaceReferenced()
	}
	workspace, err := CleanupAgentWorkspace(now, referenced)
	if err != nil {
		fail("workspace", err)
	}
	report.FreedBytes += workspace.DeletedBytes
	report.LooseFiles = len(workspace.LooseFiles)
	report.IdleCodingSpaces = len(workspace.IdleCoding)
	report.Steps["workspace"] = map[string]any{"deleted_files": workspace.DeletedFiles, "freed_bytes": workspace.DeletedBytes}

	sweep, err := SweepDianaTempDirs(now)
	if err != nil {
		fail("temp_dirs", err)
	}
	report.FreedBytes += sweep.Bytes
	report.Steps["temp_dirs"] = map[string]any{"deleted": sweep.Deleted, "freed_bytes": sweep.Bytes}
	if ctx.Err() != nil {
		return report.finish(ctx.Err())
	}

	media, err := CleanupHistoryMedia()
	if err != nil {
		fail("history_media", err)
	}
	report.FreedBytes += media.DeletedBytes
	report.Steps["history_media"] = map[string]any{"deleted_files": media.DeletedFiles, "freed_bytes": media.DeletedBytes, "remaining_bytes": media.RemainingBytes}

	if total, used, available, err := hostinfo.StorageUsage(report.StoragePath); err == nil {
		report.AvailableAfter = available
		report.Available = humanBytes(available)
		if total > 0 {
			report.StorageUsedPct = hostinfo.RoundPercent(float64(used) / float64(total) * 100)
		}
	}
	return report.finish(nil)
}

func (report StorageCleanupReport) finish(err error) StorageCleanupReport {
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
	}
	report.Freed = humanBytes(uint64(max(report.FreedBytes, 0)))
	report.Note = "按每日维护的同一套保留规则清理 Diana 自己的缓存、临时文件和过期媒体；数据库日志按保留天数每日清理；根下散落文件和闲置编码工作区只报告不删除。"
	return report
}

// summary 是给聊天指令的一段话。
func (report StorageCleanupReport) summary() string {
	text := "已清理 Diana 自身存储，释放 " + report.Freed + "。"
	if report.Available != "" {
		text += fmt.Sprintf("当前可用 %s（已用 %.1f%%）。", report.Available, report.StorageUsedPct)
	}
	if report.LooseFiles > 0 || report.IdleCodingSpaces > 0 {
		text += fmt.Sprintf("工作目录根下散落 %d 个文件、%d 个闲置编码工作区未自动删除，可在设置页的工作目录里查看。", report.LooseFiles, report.IdleCodingSpaces)
	}
	if len(report.Errors) > 0 {
		text += "部分步骤失败：" + report.Errors[0]
		if len(report.Errors) > 1 {
			text += fmt.Sprintf(" 等 %d 项", len(report.Errors))
		}
		text += "。"
	}
	return text
}
