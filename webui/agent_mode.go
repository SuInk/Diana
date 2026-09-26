// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strconv"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

// codingJobCountRuntime 让界面在切到安全模式前说出「有几个编码任务还在跑」。做成可选
// 接口：测试里的假运行时不必为此实现一个空方法，拿不到时按 0 处理。
type codingJobCountRuntime interface {
	RunningCodingJobCount(profileID string) int
}

// safeModeHeldTaskRuntime 让界面说出「切到安全模式会停发几个往别处发的任务」。
type safeModeHeldTaskRuntime interface {
	SafeModeHeldTaskCount(profileID string) int
}

func (h *BotHandler) safeModeHeldTasks(profileID string) int {
	runtime, ok := h.runtime.(safeModeHeldTaskRuntime)
	if !ok {
		return 0
	}
	return runtime.SafeModeHeldTaskCount(profileID)
}

func (h *BotHandler) runningCodingJobs(profileID string) int {
	runtime, ok := h.runtime.(codingJobCountRuntime)
	if !ok {
		return 0
	}
	return runtime.RunningCodingJobCount(profileID)
}

// agentModeImpact 返回切到安全模式时会受影响、又能便宜查到的现场情况。只读。
// 已启用的 MCP 服务数界面直接从扩展列表数，这里只补界面自己查不到的那一项。
func (h *BotHandler) agentModeImpact(c *gin.Context) {
	profileID := botProfileScope(c)
	c.JSON(http.StatusOK, gin.H{
		"running_coding_jobs": h.runningCodingJobs(profileID),
		// 往当前会话以外投递的已有任务，安全模式下到点停发、任务保留。
		"held_tasks": h.safeModeHeldTasks(profileID),
	})
}

// agentModeLabel 是日志里给人看的模式名。
func agentModeLabel(mode string) string {
	switch assistant.NormalizeAgentMode(mode) {
	case assistant.AgentModeStandard:
		return "标准模式"
	case assistant.AgentModeSafe:
		return "安全模式"
	}
	return "未设置"
}

// recordAgentModeChange 把 Agent 模式的切换单独记一条操作日志：谁（操作人取自请求）、
// 什么时候（日志时间）、从哪个模式切到哪个。模式决定主人会话里能不能跑命令、动浏览器，
// 它的变更要能单独查到，不能淹在一条笼统的「配置已保存」里。
//
// 切到安全模式时正在跑的编码任务不会被打断，这里把数量一并记下，事后查得到。
// 新建、复制机器人时 before 传零值，origin 写「新建」「复制出的」，记下它建成了哪个模式。
func (h *BotHandler) recordAgentModeChange(c *gin.Context, origin string, before, after assistant.BotConfig) {
	from := assistant.NormalizeAgentMode(before.AgentMode)
	to := assistant.NormalizeAgentMode(after.AgentMode)
	if from == to {
		return
	}
	metadata := botLogMetadata(after)
	metadata["agent_mode_from"] = from
	metadata["agent_mode_to"] = to
	message := "机器人 Agent 模式已从" + agentModeLabel(from) + "切换为" + agentModeLabel(to)
	if from == "" {
		// 新建、复制出来的机器人也记一条：新建默认标准模式，一开始就选了安全模式的也要查得到。
		message = origin + "机器人，Agent 模式为" + agentModeLabel(to)
	}
	if to == assistant.AgentModeSafe {
		if held := h.safeModeHeldTasks(after.ID); held > 0 {
			metadata["held_tasks"] = held
			message += "；有 " + strconv.Itoa(held) + " 个往当前会话以外投递的任务暂停发送"
		}
		if running := h.runningCodingJobs(after.ID); running > 0 {
			metadata["running_coding_jobs"] = running
			message += "；有 " + strconv.Itoa(running) + " 个编码任务仍在运行，不会被中断，之后不能再派新任务"
		}
	}
	recordRequestOperation(c, h.logs, "agent_mode_change", message, after.ID, metadata)
}
