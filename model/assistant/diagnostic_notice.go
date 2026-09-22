// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
)

// 插件发进聊天的消息分两类，只有后者受「错误提示」开关控制：
//
//   - 内容：用户订阅的东西本身——提醒正文、RSS 和仓库更新、定时查询结果、编码任务
//     报告。关掉错误提示不该让这些消失，否则订阅等于停了。
//   - 诊断：订阅坏了、又好了这类告诉用户「系统状态」的话。开关关掉就不该再发。
//
// 两类以前共用 sendSubscriberNotice，于是开关只在回复出错那条路径上生效，插件各自
// 发各自的告警。现在诊断类统一走下面这两个出口，开关判断只有这一处，新插件照着用
// 就不会再漏。
//
// 被开关挡下时返回 nil 而不是错误：调用方据此照常标记「已告警」，否则每个周期都会
// 重试发送一次。失败本身仍然进事件、LastError 和应用日志，只是不打扰聊天。

// diagnosticAllowed 是三个出口共用的判断：机器人的「错误提示」和插件自己的
// 「发送错误通知」都开着才发。pluginID 为空表示这条诊断不属于任何插件（例如核心的
// 一次性提醒），只看机器人那个开关。
func (r *Runtime) diagnosticAllowed(event MessageEvent, pluginID string) bool {
	// 停用的机器人不该收到任何诊断消息：它的通道已经从 bindings 里摘掉了，发过去
	// 只会变成一条投递失败，再由失败告警变成第二条发不出去的消息。
	if r.profileDisabled(event.ProfileID) {
		return false
	}
	if !r.errorNoticeAllowed(event) {
		return false
	}
	return r.pluginErrorNoticeAllowed(event, pluginID)
}

// sendDiagnosticNotice 把一条诊断消息发给订阅者，开关关闭时静默丢弃。
func (r *Runtime) sendDiagnosticNotice(ctx context.Context, event MessageEvent, pluginID, text string) error {
	if !r.diagnosticAllowed(event, pluginID) {
		return nil
	}
	return r.sendSubscriberNotice(ctx, event, text)
}

// sendDiagnosticNoticeWithEvidence 是需要送达确认的诊断出口：仓库订阅要按配置的多个
// 通知目标逐个发，并据此判断这次告警算不算发出去了。和上面共用同一个开关判断。
func (r *Runtime) sendDiagnosticNoticeWithEvidence(ctx context.Context, event MessageEvent, pluginID, text string) ([]string, bool, error) {
	if !r.diagnosticAllowed(event, pluginID) {
		return nil, false, nil
	}
	return r.sendErrorNoticeWithEvidence(ctx, event, text)
}

// sendDiagnosticFollowup 是后台任务（含第三方插件任务）的诊断出口：任务跑挂了、结果
// 发不出去属于诊断；任务结果本身和进度播报是内容，照常走 sendSubagentFollowup。
func (r *Runtime) sendDiagnosticFollowup(ctx context.Context, event MessageEvent, pluginID, text string) error {
	if !r.diagnosticAllowed(event, pluginID) {
		return nil
	}
	return r.sendSubagentFollowup(ctx, event, text)
}

// pluginErrorNoticeAllowed 读这个插件的「发送错误通知」开关。插件没标 ReportsErrors、
// 没装或被禁用时按允许处理：这里只负责「插件说了不要发」这一种情况，插件本身能不能跑
// 由别处决定。
func (r *Runtime) pluginErrorNoticeAllowed(event MessageEvent, pluginID string) bool {
	if pluginID == "" {
		return true
	}
	_, settings, enabled := r.pluginWithSettingsForEvent(pluginID, event)
	if !enabled {
		return true
	}
	return settings.Bool(pluginErrorNoticeSetting, true)
}

// reminderDiagnosticPluginID 把提醒/订阅归到产生它的插件，用来读那个插件的开关。
// 普通提醒不属于任何插件，返回空串。
func reminderDiagnosticPluginID(item Reminder) string {
	switch {
	case reminderIsRepositoryWatch(item):
		return repositoryWatchPluginID
	case reminderIsRSSWatch(item):
		return rssWatchPluginID
	default:
		return ""
	}
}

// subagentTaskPluginID 取提交这个后台任务的插件。
func subagentTaskPluginID(item reservedSubagentTask) string {
	return item.task.PluginID
}
