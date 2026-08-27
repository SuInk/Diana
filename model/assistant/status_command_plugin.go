// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	goruntime "runtime"
	"strings"
	"time"
)

const statusCommandPluginID = "official.status-command"

// statusCommandTrigger 是这个插件唯一认的口令。要求整条消息就是它——群里聊天
// 提到「#diana 怎么样了」不该被当成查状态，那是给模型回答的。
const statusCommandTrigger = "#diana"

// StatusCommandPlugin 让群友发一句 #diana 就能看到机器人的运行状态，不经过模型。
//
// 这类「探活」需求本来也能问模型（它有 diana.version 工具），但问一次要花一轮
// 生成，回答还每次都不一样。想确认机器人还活着的时候，要的是一张格式固定、
// 立刻就回的卡片。
//
// 默认关闭：它会让机器人对一条没有触发词、没有 @ 的消息主动开口，是不是想要这个
// 得由群主自己定。
type StatusCommandPlugin struct{}

func NewStatusCommandPlugin() *StatusCommandPlugin { return &StatusCommandPlugin{} }

func (p *StatusCommandPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:      statusCommandPluginID,
		Name:    "状态查询",
		Version: "0.1.0",
		Description: "群里或私聊发一条 " + statusCommandTrigger +
			"（整条消息只有这一个词）就回一张运行状态卡片：版本、平台、已运行时长。" +
			"不经过模型，回复固定且立刻返回，用来确认机器人还活着。默认关闭。",
		Official:        true,
		BuiltIn:         true,
		DefaultDisabled: true,
		Permissions:     []string{"message:read", "message:send"},
	}
}

// ShouldHandle 让这条口令不需要触发词或 @ 也能叫醒机器人。
func (p *StatusCommandPlugin) ShouldHandle(_ MessageEvent, text string) bool {
	return isStatusCommand(text)
}

// Handle 每条消息都会被调用一次（ShouldHandle 只管准不准进门，不管派发给谁），
// 所以这里必须自己再判一次，否则插件一开机器人就对所有消息回状态卡片。
func (p *StatusCommandPlugin) Handle(ctx context.Context, req PluginRequest) (*PluginResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !isStatusCommand(req.Text) {
		return nil, nil
	}
	return &PluginResponse{
		Handled: true,
		Reply:   statusCardText(req.BuildInfo, time.Now()),
	}, nil
}

// isStatusCommand 判断整条消息是不是这个口令。
//
// 全匹配指的是「整条消息只有这一个词」，前后空白和大小写不算内容：手机输入法会
// 自动把首字母大写，末尾也常带一个空格，为这个不认反而像是坏了。
func isStatusCommand(text string) bool {
	return strings.EqualFold(strings.TrimSpace(text), statusCommandTrigger)
}

// statusCardText 拼出状态卡片。字段拿不到时写「未知」，不猜也不省略——少一行会让
// 人以为是自己看漏了。
func statusCardText(info BuildInfo, now time.Time) string {
	version := strings.TrimSpace(info.Version)
	if version == "" {
		version = "未知"
	}
	startedAt := info.StartedAt
	if startedAt.IsZero() {
		// 没注入 BuildInfo 时用进程自己的启动时刻：已经跑了多久跟版本号无关，
		// 这一项照样答得出来。
		startedAt = processStartedAt
	}
	return strings.Join([]string{
		"Diana 状态",
		"版本: " + version,
		"平台: " + goruntime.GOOS + "-" + goruntime.GOARCH,
		"运行时长: " + formatStatusUptime(now.Sub(startedAt)),
	}, "\n")
}

// formatStatusUptime 把时长写成「3天 11小时 51分钟」。
//
// 和 humanizeChineseDuration 不一样：那个是说给模型和人听的自然语言，只留两级
// 单位。这里是一张固定格式的卡片，从最大的非零单位一路写到分钟，中间的零也照写
// ——每次回复的行长差不多，扫一眼就知道哪个数字变了。
func formatStatusUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return "不到1分钟"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d天 %d小时 %d分钟", days, hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%d小时 %d分钟", hours, minutes)
	default:
		return fmt.Sprintf("%d分钟", minutes)
	}
}
