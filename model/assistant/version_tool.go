// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const dianaVersionToolName = "diana.version"

// processStartedAt 是进程启动时刻，作为没有注入 BuildInfo 时的运行时长基准。
var processStartedAt = time.Now()

// 「你现在是什么版本」「多久没更新了」这类问题以前只能靠猜：版本号写在构建期注入的
// 变量里，控制台看得到，机器人自己看不到，于是它要么说不知道，要么按训练记忆编一个。
//
// 更新时间取的是可执行文件的修改时间，不是构建时间：用户问的是「这台机器上的 Diana
// 什么时候换的新」，自更新替换文件、重新安装、手动覆盖都会落在这个时间上，而构建时间
// 只说明这个包什么时候编出来的——同一个包装了三个月也还是那个时间。

// BuildInfo 描述当前这次运行的身份，由启动时注入。
type BuildInfo struct {
	// Version 是解析过的运行时版本号（源码构建会带 -dev 后缀）。
	Version string
	// BuildType 是 release 或 source。
	BuildType string
	// StartedAt 是本次进程启动时间。
	StartedAt time.Time
}

// SetBuildInfo 注入版本信息。没注入时 diana.version 如实说不知道，而不是编一个。
func (r *Runtime) SetBuildInfo(info BuildInfo) {
	if info.StartedAt.IsZero() {
		info.StartedAt = time.Now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buildInfo = info
}

func (r *Runtime) currentBuildInfo() BuildInfo {
	if r == nil {
		return BuildInfo{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.buildInfo
}

type dianaVersionTool struct {
	runtime *Runtime
}

type dianaVersionResult struct {
	Version   string `json:"version,omitempty"`
	BuildType string `json:"build_type,omitempty"`
	// UpdatedAt 是当前可执行文件的落盘时间，也就是这台机器上这个版本装上的时间。
	UpdatedAt     string `json:"updated_at,omitempty"`
	UpdatedAgo    string `json:"updated_ago,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	Uptime        string `json:"uptime,omitempty"`
	Message       string `json:"message,omitempty"`
	ReplyGuidance string `json:"reply_guidance,omitempty"`
}

const dianaVersionReplyGuidance = "像回答一句闲聊那样说，别把字段抄成清单：版本号说出来，" +
	"问到「多久没更新」再讲更新时间和已经跑了多久。查不到的项直接说不知道，不要编。"

func newDianaVersionTool(runtime *Runtime) *dianaVersionTool {
	return &dianaVersionTool{runtime: runtime}
}

func (*dianaVersionTool) Name() string { return dianaVersionToolName }

func (*dianaVersionTool) Description() string {
	return "读取 Diana 自己的版本号、这台机器上的更新时间和本次已运行时长。" +
		"用户问你是什么版本、更新到哪一版了、什么时候更新的、跑了多久时调用；不要凭记忆或按历史消息猜。无需参数。"
}

func (*dianaVersionTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{})
}

func (t *dianaVersionTool) Run(ctx context.Context, _ map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana version: runtime is not configured")
	}
	info := t.runtime.currentBuildInfo()
	result := dianaVersionResult{
		Version:       info.Version,
		BuildType:     buildTypeLabel(info.BuildType),
		ReplyGuidance: dianaVersionReplyGuidance,
	}
	now := time.Now()
	// 「跑了多久」和有没有注入版本号无关：没注入时用进程自己的启动时刻，
	// 照样答得出来。
	startedAt := info.StartedAt
	if startedAt.IsZero() {
		startedAt = processStartedAt
	}
	result.StartedAt = startedAt.Format("2006-01-02 15:04:05")
	result.Uptime = humanizeChineseDuration(now.Sub(startedAt))
	if updatedAt, ok := executableUpdatedAt(); ok {
		result.UpdatedAt = updatedAt.Format("2006-01-02 15:04:05")
		result.UpdatedAgo = humanizeChineseDuration(now.Sub(updatedAt))
	}
	if result.Version == "" {
		result.Message = "这次运行没有拿到版本号，多半是直接跑的源码构建。"
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("编码版本信息: %w", err)
	}
	return string(body), nil
}

func buildTypeLabel(buildType string) string {
	switch buildType {
	case "release":
		return "正式发布版"
	case "source":
		return "源码构建"
	default:
		return ""
	}
}

// executableUpdatedAt 返回当前可执行文件的落盘时间。软链接要跟到真身，
// 否则拿到的是链接自己的时间，装在 /usr/local/bin 下就永远不变。
func executableUpdatedAt() (time.Time, bool) {
	path, err := os.Executable()
	if err != nil {
		return time.Time{}, false
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != "" {
		path = resolved
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	updatedAt := info.ModTime()
	if updatedAt.IsZero() {
		return time.Time{}, false
	}
	return updatedAt, true
}

// humanizeChineseDuration 把时长说成人话。只保留两级单位：聊天里说「3 天 4 小时」
// 就够了，精确到秒反而没人读。
func humanizeChineseDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d < time.Minute {
		return "不到 1 分钟"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	case days > 0:
		return fmt.Sprintf("%d 天", days)
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%d 小时", hours)
	default:
		return fmt.Sprintf("%d 分钟", minutes)
	}
}
