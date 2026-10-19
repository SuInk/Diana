// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync"
	"time"
)

const (
	statusCommandPluginID        = "official.status-command"
	statusCommandCooldownSetting = "cooldown_seconds"
	defaultStatusCommandCooldown = 5 * time.Second
	maximumStatusCommandCooldown = time.Hour
)

// statusCommandTrigger 是这个插件唯一认的口令。要求整条消息就是它——群里聊天
// 提到「#diana 怎么样了」不该被当成查状态，那是给模型回答的。
const statusCommandTrigger = "#diana"

// StatusCommandPlugin 让群友发一句 #diana 就能看到机器人的运行状态，不经过模型。
//
// 这类「探活」需求本来也能问模型（它有 version 工具），但问一次要花一轮
// 生成，回答还每次都不一样。想确认机器人还活着的时候，要的是一张格式固定、
// 立刻就回的卡片。
//
// 默认开启，但同一会话有短冷却，避免多人同时探活时刷屏；不需要的机器人仍可在
// 插件页单独关闭。
type StatusCommandPlugin struct {
	mu        sync.Mutex
	lastReply map[string]time.Time
	now       func() time.Time
}

func NewStatusCommandPlugin() *StatusCommandPlugin {
	return &StatusCommandPlugin{lastReply: map[string]time.Time{}, now: time.Now}
}

func (p *StatusCommandPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:      statusCommandPluginID,
		Name:    "状态查询",
		Version: "0.1.1",
		Description: "群里或私聊发一条 " + statusCommandTrigger +
			"（整条消息只有这一个词）就回一张运行状态卡片：版本、平台、已运行时长。" +
			"不经过模型，回复固定且立刻返回，用来确认机器人还活着。默认开启，同一会话默认冷却 5 秒。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"message:read", "message:send"},
		Settings: []PluginSettingSpec{{
			Key:         statusCommandCooldownSetting,
			Label:       "冷却时间",
			Description: "同一群或私聊两次状态回复之间的最短间隔；设为 0 可关闭冷却。",
			Type:        PluginSettingTypeNumber,
			Default:     int(defaultStatusCommandCooldown / time.Second),
			Min:         settingRange(0),
			Max:         settingRange(float64(maximumStatusCommandCooldown / time.Second)),
			Step:        1,
			Unit:        "秒",
		}},
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
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	cooldown := time.Duration(req.Settings.Int(statusCommandCooldownSetting, int(defaultStatusCommandCooldown/time.Second))) * time.Second
	if !p.takeCooldown(req.Event, now, cooldown) {
		// Handled=true 让运行时静默消费冷却中的口令，避免它继续进入普通回复链路。
		return &PluginResponse{Handled: true}, nil
	}
	return &PluginResponse{
		Handled: true,
		Reply:   statusCardText(req.BuildInfo, now),
	}, nil
}

func (p *StatusCommandPlugin) takeCooldown(event MessageEvent, now time.Time, cooldown time.Duration) bool {
	if cooldown <= 0 {
		return true
	}
	if cooldown > maximumStatusCommandCooldown {
		cooldown = maximumStatusCommandCooldown
	}
	key := strings.Join([]string{
		strings.TrimSpace(event.Platform),
		firstNonEmpty(strings.TrimSpace(event.ProfileID), strings.TrimSpace(event.SelfID)),
		sessionKey(event),
	}, "\x00")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastReply == nil {
		p.lastReply = map[string]time.Time{}
	}
	if last, ok := p.lastReply[key]; ok && now.Sub(last) < cooldown {
		return false
	}
	p.lastReply[key] = now
	// 状态查询可能活很久，顺手清掉已经不可能参与任何合法冷却的会话。
	for scope, last := range p.lastReply {
		if now.Sub(last) > maximumStatusCommandCooldown {
			delete(p.lastReply, scope)
		}
	}
	return true
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
	version := trimVersionPrefix(info.Version)
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

// trimVersionPrefix 去掉版本号的 v 前缀：卡片上只写数字。
//
// 只在 v 后面直接跟数字时才去——那才是 vX.Y.Z 的写法。像 dev 这种非语义化的
// 版本串原样留着，砍掉首字母只会把它变成看不懂的东西。
func trimVersionPrefix(version string) string {
	version = strings.TrimSpace(version)
	if len(version) < 2 || (version[0] != 'v' && version[0] != 'V') {
		return version
	}
	if version[1] < '0' || version[1] > '9' {
		return version
	}
	return version[1:]
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

// statusCommandActive 判断这条消息是不是已开启的 #diana 口令。
//
// 回复链路上有不少环节会顺手花模型 token：长期记忆入队、跨群语义检索、语义索引、
// 话题承接解析、发送前审核。这些对一张本地拼出来的固定卡片都没有意义，入口处用它
// 把这些环节短路掉。插件关着时 #diana 只是一条普通消息，照常走原来的路径。
func (r *Runtime) statusCommandActive(event MessageEvent, text string) bool {
	if r == nil || r.plugins == nil || !isStatusCommand(text) {
		return false
	}
	return r.plugins.EnabledWithOverrides(statusCommandPluginID, r.pluginOverridesForEvent(event))
}

// replyStatusCommand 在进入任何模型环节之前直接生成状态卡片。
func (r *Runtime) replyStatusCommand(ctx context.Context, event MessageEvent, text string) (string, bool) {
	if !r.statusCommandActive(event, text) {
		return "", false
	}
	resp, err := r.plugins.RunOneWithGroupOverrides(ctx, statusCommandPluginID, PluginRequest{
		Event:     event,
		Text:      text,
		Channel:   r.channel,
		BuildInfo: r.currentBuildInfo(),
	}, r.pluginOverridesForEvent(event), r.pluginSettingOverridesForEvent(event))
	if err != nil || resp == nil || !resp.Handled {
		return "", false
	}
	return strings.TrimSpace(resp.Reply), true
}
