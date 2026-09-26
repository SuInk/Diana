// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"log"
	"strings"
)

// 主人的强格式管理命令。
//
// 命令放在一张表里，执行（handleOwnerCommand）和纯判断（wouldHandleOwnerCommand）
// 都从这张表走，不会一边加了命令另一边忘了。纯判断是给连发交接（sender_burst.go）
// 用的：它要在不执行的前提下知道一条消息是不是主人命令——命令和确认码必须在自己
// 那一轮生效，既不能被后一条接走，也不该去接别人：命令的回复是固定文本，接过来的
// 问题没人回答。
type ownerCommand struct {
	// exact 和 prefix 二选一：整句等于 exact，或者以 prefix 开头（参数在后面）。
	exact  string
	prefix string
	run    func(r *Runtime, event MessageEvent, arg string) string
}

func (command ownerCommand) match(text string) (string, bool) {
	if command.exact != "" {
		return "", text == command.exact
	}
	if strings.HasPrefix(text, command.prefix) {
		return strings.TrimSpace(strings.TrimPrefix(text, command.prefix)), true
	}
	return "", false
}

// 「lllm 当前」和「lllm 切换」跟着「激活配置」一起去掉了：没有激活项之后，「当前用
// 哪个」由本次调用的用途和分组顺序决定，不再是一个能被切换的全局状态。
var ownerCommands = []ownerCommand{
	{exact: "lllm 列表", run: func(r *Runtime, _ MessageEvent, _ string) string { return r.renderLLMProfiles() }},
	{exact: "群 列表", run: func(r *Runtime, event MessageEvent, _ string) string { return r.renderDisabledGroups(event) }},
	{prefix: "群 禁用 ", run: func(r *Runtime, event MessageEvent, groupID string) string {
		return r.setGroupDisabled(event, groupID, true)
	}},
	{prefix: "群 启用 ", run: func(r *Runtime, event MessageEvent, groupID string) string {
		return r.setGroupDisabled(event, groupID, false)
	}},
	{exact: "提醒 列表", run: func(r *Runtime, event MessageEvent, _ string) string { return r.renderReminders(event) }},
	{prefix: "提醒 取消 ", run: func(r *Runtime, event MessageEvent, id string) string {
		if _, err := r.cancelOneTimeReminder(event.UserID, id); err != nil {
			if _, triggerErr := r.cancelEventTrigger(event.UserID, id); triggerErr == nil {
				return "触发任务已取消并释放额度，记录仍保留。"
			}
			return "取消提醒失败：" + err.Error()
		}
		return "提醒已取消并释放额度，记录仍保留。"
	}},
	{prefix: "提醒 删除 ", run: func(r *Runtime, event MessageEvent, id string) string { return r.deleteReminder(event, id) }},
	{prefix: "提醒 添加 ", run: func(r *Runtime, event MessageEvent, args string) string { return r.addReminder(event, args) }},
	{exact: "订阅 列表", run: func(r *Runtime, event MessageEvent, _ string) string { return r.renderScheduledQueries(event.UserID) }},
	{prefix: "订阅 取消 ", run: func(r *Runtime, event MessageEvent, id string) string {
		if _, err := r.cancelScheduledQuery(event.UserID, id); err != nil {
			return "取消定时订阅失败：" + err.Error()
		}
		return "定时订阅已取消并释放额度，记录仍保留。"
	}},
	{prefix: "订阅 删除 ", run: func(r *Runtime, event MessageEvent, id string) string {
		removed, err := r.deleteScheduledQuery(event.UserID, id)
		if err != nil {
			return "删除定时订阅失败：" + err.Error()
		}
		if !removed {
			return "没有找到对应的定时订阅。"
		}
		return "定时订阅已删除。"
	}},
	{prefix: "订阅 添加 ", run: func(r *Runtime, event MessageEvent, args string) string {
		return r.addScheduledQueryCommand(event, args)
	}},
	{exact: "清空上下文", run: runOwnerContextReset},
	{exact: "清除上下文", run: runOwnerContextReset},
	{exact: "帮助", run: runOwnerHelp},
	{exact: "菜单", run: runOwnerHelp},
}

func runOwnerContextReset(r *Runtime, event MessageEvent, _ string) string {
	if err := r.clearSessionHistory(event); err != nil {
		log.Printf("diana context reset failed: %v", err)
		return "清空上下文失败，请稍后重试或检查服务日志。"
	}
	return "已清空当前会话上下文；聊天记录、长期记忆和人设仍保留。"
}

func runOwnerHelp(*Runtime, MessageEvent, string) string {
	return "可用命令：lllm 列表、lllm 当前、lllm 切换 <名称>、群 列表、群 禁用 <群号>、群 启用 <群号>、响应限制 列表、响应限制 解除 <账号>、提醒 添加 <时长> <内容>、提醒 列表、提醒 取消 <ID>、提醒 删除 <ID>、订阅 添加 <周期> <查询内容>、订阅 列表、订阅 取消 <ID>、订阅 删除 <ID>、清空上下文。也可以直接说：1 分钟后提醒我睡觉，或者每 1 分钟查询某件事并通知我。"
}

// handleOwnerCommand 边认边执行主人命令，text 是 cleanInput 之后的文本。
func (r *Runtime) handleOwnerCommand(event MessageEvent, text string) (string, bool) {
	// 按事件所属的机器人认主人：多机器人时每台的主人只管自己那台。
	cfg := r.effectiveConfigForEvent(event)
	if !cfg.IsOwnerEvent(event) {
		return "", false
	}
	// 这些是强格式管理命令；自然语言切模型由机器人内建配置命令处理。
	command := strings.TrimSpace(text)
	if reply, handled := r.handleReplySuppressionOwnerCommand(event, command); handled {
		return reply, true
	}
	// 编码任务的确认码。放在这里是因为它本来就只对主人有意义，而且必须在进入
	// 模型那一轮之前就被认出来——等着放行的 CLI 进程正停在那儿。
	if reply, handled := r.handleCodingApprovalReply(event, command); handled {
		return reply, true
	}
	for _, owner := range ownerCommands {
		if arg, ok := owner.match(command); ok {
			return owner.run(r, event, arg), true
		}
	}
	return "", false
}

// wouldHandleOwnerCommand 报告这条消息会不会被 handleOwnerCommand 接住，不产生任何副作用。
// 判断顺序和 handleOwnerCommand 一致，只是把每一步的「执行」换成「认出来」。
func (r *Runtime) wouldHandleOwnerCommand(event MessageEvent, text string) bool {
	if r == nil {
		return false
	}
	cfg := r.effectiveConfigForEvent(event)
	if !cfg.IsOwnerEvent(event) {
		return false
	}
	command := strings.TrimSpace(r.cleanInput(event, text))
	if replySuppressionOwnerCommandKind(command) != "" || r.codingApprovalCodePending(event, command) {
		return true
	}
	for _, owner := range ownerCommands {
		if _, ok := owner.match(command); ok {
			return true
		}
	}
	return false
}

// codingApprovalCodePending 报告消息里有没有一个正在等待的编码任务确认码，只看不放行。
// 认码的规则和 handleCodingApprovalReply 一致。
func (r *Runtime) codingApprovalCodePending(event MessageEvent, text string) bool {
	if r == nil || r.codingJobRegistry == nil {
		return false
	}
	lowered := strings.ToLower(text)
	for _, request := range r.codingJobs().pendingApprovals() {
		if !r.sameProfileAsEvent(request.ProfileID, event) {
			continue
		}
		allowCode, alwaysCode, denyCode := codingApprovalCodes(request)
		if strings.TrimSpace(request.Pattern) == "" {
			alwaysCode = ""
		}
		for _, code := range []string{allowCode, alwaysCode, denyCode} {
			if containsStandaloneCode(lowered, code) {
				return true
			}
		}
	}
	return false
}
