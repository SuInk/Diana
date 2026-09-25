// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// CleanupAgentWorkspace 按分区保留时长清理 Agent 工作目录，见 agent.CleanupWorkspace。
// referenced 报告 coding/<name> 还有没有机器人在用，为 nil 时不报告闲置的编码工作区。
func CleanupAgentWorkspace(now time.Time, referenced func(string) bool) (agent.WorkspaceCleanupReport, error) {
	return agent.CleanupWorkspace(AgentWorkspaceDir(), agent.WorkspaceCleanupOptions{Now: now, CodingReferenced: referenced})
}

// CodingWorkspaceReferenced 返回一个判断 coding/<name> 是否还被某台机器人的编码代理
// 配置引用的函数：工作区白名单里登记的仓库，以及托管工作区 managed-<配置名>。
//
// 只看机器人级的插件设置。群里单独覆盖的工作区看不到，会被报成闲置——闲置只报告
// 不删，宁可多报一个也不漏掉真没人用的。
func (r *Runtime) CodingWorkspaceReferenced() func(string) bool {
	names := map[string]bool{}
	if r != nil {
		root := filepath.Clean(CodingWorkspaceRoot())
		for _, profile := range r.ProfileConfigs() {
			_, settings, enabled := r.pluginWithSettingsForEvent(codingAgentPluginID, MessageEvent{ProfileID: profile.ID, Platform: profile.Platform})
			if !enabled {
				continue
			}
			if workspaces, err := parseCodingWorkspaces(settings.String(codingAgentSettingWorkspaces, "")); err == nil {
				for _, item := range workspaces {
					if rel, err := filepath.Rel(root, filepath.Clean(item.Dir)); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
						names[strings.ToLower(strings.Split(filepath.ToSlash(rel), "/")[0])] = true
					}
				}
			}
			if profiles, err := codingProfiles(settings); err == nil {
				for _, item := range profiles {
					if item.ManagedWorkspace {
						names["managed-"+strings.ToLower(item.ID)] = true
					}
				}
			}
		}
	}
	return func(name string) bool { return names[strings.ToLower(name)] }
}

// dianaTempDirPrefixes 是 Diana 在系统临时目录里建的目录和文件的前缀。它们本该用完
// 就删，但进程被杀、转码中途崩掉时会留下来：线上 3 天攒了 132 个 diana-agent-image-*。
var dianaTempDirPrefixes = []string{
	"diana-agent-image-",
	"diana-agent-command-",
	"diana-attachment-",
	"diana-bili-video-",
	"diana-coding-check-",
	"diana-image-ocr-",
	"diana-pdf-",
	"diana-render-frames-",
	"diana-render-image-",
	renderMediaTempPrefix,
	"diana-resolver-image-",
	"diana-resolver-video-",
	"diana-sandbox-probe-",
	"diana-stt-",
	"diana-video-context-",
	"diana-voice-source-",
}

// TempSweepResult 是一次临时目录清扫的结果。
type TempSweepResult struct {
	Deleted int
	Bytes   int64
}

// dianaTempMaxAge 是临时目录里 Diana 残留多久以后清掉。正在用的临时目录活不过
// 一次请求，一天足够把「正在转码的长视频」排除在外。
const dianaTempMaxAge = 24 * time.Hour

// SweepDianaTempDirs 清掉系统临时目录里 Diana 自己留下、超过一天的目录和文件。
// 同一台机器上可能跑着几份 Diana，它们共用临时目录，所以按修改时间判断，
// 不按「是不是本进程建的」。
func SweepDianaTempDirs(now time.Time) (TempSweepResult, error) {
	return sweepDianaTempDirs(os.TempDir(), now)
}

func sweepDianaTempDirs(tempRoot string, now time.Time) (TempSweepResult, error) {
	var result TempSweepResult
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		return result, err
	}
	cutoff := now.Add(-dianaTempMaxAge)
	var errs []error
	for _, entry := range entries {
		name := entry.Name()
		if !hasDianaTempPrefix(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.ModTime().Before(cutoff) {
			continue
		}
		target := filepath.Join(tempRoot, name)
		size := int64(0)
		if info.IsDir() {
			size = dirSize(target)
		} else {
			size = info.Size()
		}
		if err := os.RemoveAll(target); err != nil {
			errs = append(errs, err)
			continue
		}
		result.Deleted++
		result.Bytes += size
	}
	return result, errors.Join(errs...)
}

func hasDianaTempPrefix(name string) bool {
	for _, prefix := range dianaTempDirPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}
