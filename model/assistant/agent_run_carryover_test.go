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
	stale := runtime.agentCarryovers[agentCarryoverKey(event)]
	stale.at = time.Now().Add(-agentCarryoverTTL - time.Minute)
	runtime.agentCarryovers[agentCarryoverKey(event)] = stale
	runtime.mu.Unlock()
	if _, ok := runtime.agentCarryoverMessage(event); ok {
		t.Fatal("过期存档不该再注入")
	}
}

// 存档不能跨发言人。群聊的 sessionKey 只有群号,只按会话存的话,
// Winter 没跑完的那轮会原样进 喵neko 的下一轮:注入正文里带着 Winter 的账号和
// 「关系等级：主人」,末尾还写着「直接从未完成的部分继续」,模型于是接着办了
// Winter 的事,还把 Winter 的身份当成了当前发言人的。
func TestAgentCarryoverDoesNotLeakAcrossSpeakers(t *testing.T) {
	runtime := carryoverRuntime()
	winter := MessageEvent{Kind: EventKindGroup, Platform: "onebot", GroupID: "g1", UserID: "winter-uid", MessageID: "m-winter"}
	neko := MessageEvent{Kind: EventKindGroup, Platform: "onebot", GroupID: "g1", UserID: "neko-uid", MessageID: "m-neko"}

	runtime.rememberAgentRunProgress(winter, &agent.Response{
		FinishReason: "tool_budget_exhausted",
		Steps: []agent.Step{
			{Tool: "diana.glossary", Input: map[string]any{"term": "西格玛男", "actor": "winter-uid"}, Output: "已记下：西格玛男 = 不做舔狗"},
			{Tool: "diana.relationship", Input: map[string]any{"target_user_id": "winter-uid"}, Output: "关系等级：主人"},
		},
	})

	if message, ok := runtime.agentCarryoverMessage(neko); ok {
		t.Fatalf("同群另一个人不该拿到 Winter 的存档：%s", message.Content)
	}

	// 同一个人接着说话仍然要拿得到——这才是存档存在的意义。
	message, ok := runtime.agentCarryoverMessage(winter)
	if !ok {
		t.Fatal("Winter 自己的下一轮应当拿到存档")
	}
	if !strings.Contains(message.Content, "西格玛男") {
		t.Fatalf("存档内容不对：%s", message.Content)
	}
}

// 一个人做完收口只清自己那份,不该顺手把同群别人的存档也抹掉。
func TestAgentCarryoverClearIsPerSpeaker(t *testing.T) {
	runtime := carryoverRuntime()
	winter := MessageEvent{Kind: EventKindGroup, Platform: "onebot", GroupID: "g1", UserID: "winter-uid"}
	neko := MessageEvent{Kind: EventKindGroup, Platform: "onebot", GroupID: "g1", UserID: "neko-uid"}

	for _, event := range []MessageEvent{winter, neko} {
		runtime.rememberAgentRunProgress(event, &agent.Response{
			FinishReason: "tool_budget_exhausted",
			Steps:        []agent.Step{{Tool: "lookup", Input: map[string]any{"who": event.UserID}, Output: "半成品"}},
		})
	}
	runtime.rememberAgentRunProgress(neko, &agent.Response{FinishReason: "final"})

	if _, ok := runtime.agentCarryoverMessage(neko); ok {
		t.Fatal("喵neko 做完了,自己那份该清掉")
	}
	if _, ok := runtime.agentCarryoverMessage(winter); !ok {
		t.Fatal("Winter 那份没做完,不该被别人的收口带走")
	}
}

// 认不出发言人时不存档：那等于回到一个人人都命中的公共桶。
func TestAgentCarryoverSkipsUnattributableSpeaker(t *testing.T) {
	runtime := carryoverRuntime()
	anonymous := MessageEvent{Kind: EventKindGroup, Platform: "onebot", GroupID: "g1"}
	runtime.rememberAgentRunProgress(anonymous, &agent.Response{
		FinishReason: "tool_budget_exhausted",
		Steps:        []agent.Step{{Tool: "lookup", Output: "半成品"}},
	})
	if _, ok := runtime.agentCarryoverMessage(anonymous); ok {
		t.Fatal("发言人认不出来时不该存档")
	}
	if _, ok := runtime.agentCarryoverMessage(MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "neko-uid"}); ok {
		t.Fatal("更不该被同群其他人命中")
	}
}

// 过期条目在写入时被顺手清掉,不必等那个人再开口。
func TestAgentCarryoverPrunesExpiredOnWrite(t *testing.T) {
	carryovers := map[string]agentRunCarryover{
		"group:g1\x00stale": {entries: []string{"- old() → x"}, at: time.Now().Add(-agentCarryoverTTL - time.Minute)},
		"group:g1\x00fresh": {entries: []string{"- new() → y"}, at: time.Now()},
	}
	pruneExpiredAgentCarryovers(carryovers, time.Now())
	if _, ok := carryovers["group:g1\x00stale"]; ok {
		t.Fatal("过期条目该被清掉")
	}
	if _, ok := carryovers["group:g1\x00fresh"]; !ok {
		t.Fatal("没过期的不该被误删")
	}
}
