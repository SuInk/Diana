// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// Agent 的工具观察此前不跨回复保留:预算耗尽收口后,下一条「继续」只带着
// 聊天历史里那句「已确认无误」重新开跑,没有任何证据数据,模型只能重新
// 核验——预算又烧在核验上,任务永远推不到下一步。
//
// 现在预算耗尽类收口(tool_budget_exhausted / protocol_repair_exhausted /
// finalization_reserved)会把本轮工具观察压成摘要按会话存档,同会话的下一次
// Agent 运行把摘要注入上下文,明确告知这些结果仍然有效、直接从未完成的部分
// 继续。任务真正完成(final 或异步图片任务已入队)就清档;摘要有时效,过期
// 不再注入,避免几小时后一个不相干的任务背着陈旧存档开跑。

const (
	agentCarryoverTTL         = 10 * time.Minute
	agentCarryoverMaxEntries  = 12
	agentCarryoverOutputRunes = 300
)

type agentRunCarryover struct {
	entries []string
	at      time.Time
}

func agentRunUnfinished(reason string) bool {
	switch reason {
	case "tool_budget_exhausted", "protocol_repair_exhausted", "finalization_reserved":
		return true
	}
	return false
}

// agentCarryoverEntries 把一次运行的工具步骤压成一行一条的摘要。
// 被跳过的步骤(重复调用、超限)不进摘要——它们没有产生新信息。
func agentCarryoverEntries(steps []agent.Step) []string {
	entries := make([]string, 0, len(steps))
	for _, step := range steps {
		if step.Skipped || strings.TrimSpace(step.Tool) == "" {
			continue
		}
		keys := make([]string, 0, len(step.Input))
		for key := range step.Input {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		inputs := make([]string, 0, len(keys))
		for _, key := range keys {
			inputs = append(inputs, fmt.Sprintf("%s=%v", key, step.Input[key]))
		}
		outcome := strings.TrimSpace(step.Output)
		if step.Error != "" {
			outcome = "失败:" + step.Error
		}
		if runes := []rune(outcome); len(runes) > agentCarryoverOutputRunes {
			outcome = string(runes[:agentCarryoverOutputRunes]) + "…"
		}
		entries = append(entries, fmt.Sprintf("- %s(%s) → %s", step.Tool, strings.Join(inputs, ", "), outcome))
	}
	return entries
}

// rememberAgentRunProgress 在一次 Agent 运行结束后更新会话存档:
// 预算耗尽类收口把本轮观察并入存档,正常完成则清档。
func (r *Runtime) rememberAgentRunProgress(event MessageEvent, resp *agent.Response) {
	if r == nil || resp == nil {
		return
	}
	session := sessionKey(event)
	if session == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !agentRunUnfinished(resp.FinishReason) {
		delete(r.agentCarryovers, session)
		return
	}
	entries := agentCarryoverEntries(resp.Steps)
	if len(entries) == 0 {
		return
	}
	if r.agentCarryovers == nil {
		r.agentCarryovers = map[string]agentRunCarryover{}
	}
	// 连续多轮都没做完时新观察追加在旧存档后面,只保留最近的若干条:
	// 早期核验的结论会被后续步骤引用到最终回复里,而条目本身要控制体积。
	previous := r.agentCarryovers[session]
	merged := append(append([]string(nil), previous.entries...), entries...)
	if len(merged) > agentCarryoverMaxEntries {
		merged = merged[len(merged)-agentCarryoverMaxEntries:]
	}
	r.agentCarryovers[session] = agentRunCarryover{entries: merged, at: time.Now()}
}

// agentCarryoverMessage 返回要注入本次 Agent 运行的存档消息;没有新鲜存档
// 时返回 false。存档不在这里删除——本次运行结束时 rememberAgentRunProgress
// 会按结果决定清档还是续档。
func (r *Runtime) agentCarryoverMessage(event MessageEvent) (llm.Message, bool) {
	session := sessionKey(event)
	if r == nil || session == "" {
		return llm.Message{}, false
	}
	r.mu.Lock()
	carryover, ok := r.agentCarryovers[session]
	if ok && time.Since(carryover.at) > agentCarryoverTTL {
		delete(r.agentCarryovers, session)
		ok = false
	}
	r.mu.Unlock()
	if !ok || len(carryover.entries) == 0 {
		return llm.Message{}, false
	}
	return llm.Message{
		Role: llm.RoleUser,
		Content: "上一条回复的工具执行进度存档(任务当时没做完):\n" +
			strings.Join(carryover.entries, "\n") +
			"\n以上结果仍然有效。不要重复执行相同的查询或核验,直接从未完成的部分继续。",
		Priority: llm.MessagePriorityPlugin,
	}, true
}
