// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// 主人命令的纯判断：这条消息进了回复入口会不会被 handleOwnerCommand 接住。
//
// handleOwnerCommand 边认边执行（加提醒、清上下文、放行编码任务），不能拿来当判断用。
// 连发交接（sender_burst.go）要在不执行的前提下知道一条消息是不是主人命令：命令和
// 确认码必须在自己那一轮生效，既不能被后一条接走，也不该去接别人——命令的回复是
// 固定文本，接过来的问题没人回答。
//
// 这里的清单和 handleOwnerCommand 的 switch 一一对应，改那边的命令要同步改这里。
var (
	ownerCommandExact = []string{
		"lllm 列表", "群 列表", "提醒 列表", "订阅 列表",
		"清空上下文", "清除上下文", "帮助", "菜单",
	}
	ownerCommandPrefixes = []string{
		"群 禁用 ", "群 启用 ",
		"提醒 取消 ", "提醒 删除 ", "提醒 添加 ",
		"订阅 取消 ", "订阅 删除 ", "订阅 添加 ",
	}
)

// wouldHandleOwnerCommand 报告这条消息会不会被 handleOwnerCommand 接住，不产生任何副作用。
func (r *Runtime) wouldHandleOwnerCommand(event MessageEvent, text string) bool {
	if r == nil {
		return false
	}
	cfg := r.effectiveConfigForEvent(event)
	if !cfg.IsOwnerEvent(event) {
		return false
	}
	command := strings.TrimSpace(r.cleanInput(event, text))
	if replySuppressionOwnerCommandKind(command) != "" {
		return true
	}
	if r.codingApprovalCodePending(event, command) {
		return true
	}
	for _, exact := range ownerCommandExact {
		if command == exact {
			return true
		}
	}
	for _, prefix := range ownerCommandPrefixes {
		if strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}

// codingApprovalCodePending 报告消息里有没有一个正在等待的编码任务确认码，只看不放行。
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
