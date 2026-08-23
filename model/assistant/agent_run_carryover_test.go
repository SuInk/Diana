// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

func carryoverEvent() MessageEvent {
	return MessageEvent{Kind: EventKindGroup, Platform: "onebot", GroupID: "g1", UserID: "u1", MessageID: "m1"}
}

func carryoverRuntime() *Runtime {
	return NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
}

// 预算耗尽的运行要把工具观察存档,下一次同会话运行能拿到注入消息;
// 正常完成的运行必须清档,否则不相干的新任务会背着旧存档开跑。
func TestAgentCarryoverSurvivesExhaustionAndClearsOnFinal(t *testing.T) {
	runtime := carryoverRuntime()
	event := carryoverEvent()

	runtime.rememberAgentRunProgress(event, &agent.Response{
		FinishReason: "tool_budget_exhausted",
		Steps: []agent.Step{
			{Tool: "group.members", Input: map[string]any{"group_id": "g1"}, Output: "Winter、美海A、美海B、Diana 共 4 人"},
			{Tool: "member_avatar", Input: map[string]any{"user_id": "10001"}, Output: "https://q.qlogo.cn/g?b=qq&nk=10001"},
			{Tool: "lookup", Skipped: true, Output: "被跳过的重复调用"},
		},
	})
	message, ok := runtime.agentCarryoverMessage(event)
	if !ok {
		t.Fatal("预算耗尽后应有存档可注入")
	}
	if !strings.Contains(message.Content, "group.members") || !strings.Contains(message.Content, "美海A") {
		t.Fatalf("存档应包含工具名与观察内容:%s", message.Content)
	}
	if strings.Contains(message.Content, "被跳过的重复调用") {
		t.Fatalf("被跳过的步骤没有信息量,不该进存档:%s", message.Content)
	}
	if !strings.Contains(message.Content, "不要重复执行相同的查询或核验") {
		t.Fatalf("存档要明确告诉模型直接续跑:%s", message.Content)
	}

	runtime.rememberAgentRunProgress(event, &agent.Response{FinishReason: "final"})
	if _, ok := runtime.agentCarryoverMessage(event); ok {
		t.Fatal("任务完成后存档必须清掉")
	}
}

// 连续多轮没做完时观察要追加而不是覆盖,并且条目有上限;过期存档不再注入。
func TestAgentCarryoverAccumulatesAndExpires(t *testing.T) {
	runtime := carryoverRuntime()
	event := carryoverEvent()

	runtime.rememberAgentRunProgress(event, &agent.Response{
		FinishReason: "tool_budget_exhausted",
		Steps:        []agent.Step{{Tool: "step.one", Output: "第一轮的结论"}},
	})
	runtime.rememberAgentRunProgress(event, &agent.Response{
		FinishReason: "protocol_repair_exhausted",
		Steps:        []agent.Step{{Tool: "step.two", Output: "第二轮的结论"}},
	})
	message, ok := runtime.agentCarryoverMessage(event)
	if !ok || !strings.Contains(message.Content, "第一轮的结论") || !strings.Contains(message.Content, "第二轮的结论") {
		t.Fatalf("多轮观察应当累积:%v %s", ok, message.Content)
	}

	steps := make([]agent.Step, 0, agentCarryoverMaxEntries+5)
	for range agentCarryoverMaxEntries + 5 {
		steps = append(steps, agent.Step{Tool: "noise", Output: "刷屏"})
	}
	runtime.rememberAgentRunProgress(event, &agent.Response{FinishReason: "tool_budget_exhausted", Steps: steps})
	message, _ = runtime.agentCarryoverMessage(event)
	if count := strings.Count(message.Content, "\n- "); count > agentCarryoverMaxEntries {
		t.Fatalf("存档条目应有上限 %d,实际 %d", agentCarryoverMaxEntries, count)
	}

	runtime.mu.Lock()
	stale := runtime.agentCarryovers[sessionKey(event)]
	stale.at = time.Now().Add(-agentCarryoverTTL - time.Minute)
	runtime.agentCarryovers[sessionKey(event)] = stale
	runtime.mu.Unlock()
	if _, ok := runtime.agentCarryoverMessage(event); ok {
		t.Fatal("过期存档不该再注入")
	}
}
