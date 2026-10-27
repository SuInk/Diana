// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

const appendTestRule = "这是一条逐轮都不变的尾部说明，续接时照样整段再发一遍。"

func appendTestTurn(history []string, tailExtra, current string) []llm.Message {
	// 头部按真实请求的量级给足，否则几条短消息加上查证提醒就超过两倍上限。
	messages := []llm.Message{{Role: llm.RoleSystem, Content: "人设头部" + strings.Repeat("。", 2000)}}
	for _, line := range history {
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: line})
	}
	messages[len(messages)-1].CacheBreakpoint = true
	messages = append(messages,
		llm.Message{Role: llm.RoleSystem, Content: appendTestRule + "\n" + tailExtra},
		llm.Message{Role: llm.RoleUser, Content: current})
	return messages
}

func continueFrom(log *appendContextLog, messages []llm.Message) *agent.Continuation {
	continuation, _ := appendContextContinuation(log, messages)
	return continuation
}

func appendTestLog(t *testing.T, messages []llm.Message, reply string) *appendContextLog {
	t.Helper()
	transcript := append(append([]llm.Message(nil), messages...),
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: agent.FinalizeToolName, Arguments: map[string]any{"text": reply}}}},
		llm.Message{Role: llm.RoleTool, ToolCallID: "c1", ToolName: agent.FinalizeToolName, Content: "已发送"})
	log := newAppendContextLog(&agent.Response{Transcript: transcript, ConversationKey: "k"}, appendContextHeadKey(messages))
	if log == nil {
		t.Fatal("log not created")
	}
	return log
}

func TestAppendContextAppendsNewHistoryAndWholeTail(t *testing.T) {
	first := appendTestTurn([]string{"[历史 10:00] 甲: 早", "[历史 10:01] 乙: 早啊"}, "当前发言者：甲，这一行每轮都会变化，长度也够。", "【当前需要回复的消息】甲: 在吗")
	log := appendTestLog(t, first, "在的")
	second := appendTestTurn([]string{"[历史 10:01] 乙: 早啊", "[历史 10:02] 甲: 在吗", "[历史 10:03] 丙: 吃了吗"}, "当前发言者：丙，这一行每轮都会变化，长度也够。", "【当前需要回复的消息】丙: 吃了吗")
	second = append(second[:3], append([]llm.Message{{Role: llm.RoleAssistant, Content: "在的"}}, second[3:]...)...)
	continuation := continueFrom(log, second)
	if continuation == nil {
		t.Fatal("expected a continuation")
	}
	var got []string
	for _, message := range continuation.Append {
		got = append(got, string(message.Role)+":"+message.Content)
	}
	joined := strings.Join(got, "|")
	for _, want := range []string{"[历史 10:02] 甲: 在吗", "[历史 10:03] 丙: 吃了吗", appendTestRule + "\n当前发言者：丙"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	for _, unwanted := range []string{"乙: 早啊", "assistant:在的", "人设头部"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("%q should not be appended again: %q", unwanted, joined)
		}
	}
	if last := continuation.Append[len(continuation.Append)-1]; last.Content != "【当前需要回复的消息】丙: 吃了吗" || last.CacheBreakpoint {
		t.Fatalf("current message must stay last: %#v", last)
	}
}

func TestAppendContextRestartsWhenHeadChangesOrGrowsTooLong(t *testing.T) {
	first := appendTestTurn([]string{"[历史 10:00] 甲: 早"}, "", "甲: 在吗")
	log := appendTestLog(t, first, "在的")
	changed := appendTestTurn([]string{"[历史 10:00] 甲: 早"}, "", "甲: 还在吗")
	changed[0].Content = "换了人设"
	if continuation, decision := appendContextContinuation(log, changed); continuation != nil || decision.reason != "head_changed" {
		t.Fatalf("a changed head must restart, reason = %q", decision.reason)
	}
	long := appendTestTurn([]string{"[历史 10:00] 甲: 早", "[历史 10:05] 甲: " + strings.Repeat("很长的一段话。", 400)}, "", "甲: 还在吗")
	log.prefix = append(log.prefix, llm.Message{Role: llm.RoleUser, Content: strings.Repeat("旧内容。", 3000)})
	if continuation, decision := appendContextContinuation(log, long); continuation != nil || decision.reason != "too_long" {
		t.Fatal("a prefix past the growth limit must restart")
	}
}

// 整轮走两遍：第二次主回复请求必须以第一次实际发出的请求加上那次的 send 开头。
func TestReplyTurnsContinueAppendOnly(t *testing.T) {
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{
		`tool_calls:[{"id":"s1","name":"send","arguments":{"text":"在的"}}]`,
		`tool_calls:[{"id":"s2","name":"send","arguments":{"text":"吃过了"}}]`,
	}}
	runtime := NewRuntime(BotConfig{AgentEnabled: true, BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	first := interimTestEvent()
	first.MessageID, first.RawMessage = "append-1", "在吗"
	first.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "在吗"}}}
	if _, err := runtime.replyTo(t.Context(), first, first.RawMessage); err != nil {
		t.Fatal(err)
	}
	second := interimTestEvent()
	second.MessageID, second.RawMessage, second.Time = "append-2", "吃了吗", first.Time+30
	second.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "吃了吗"}}}
	if _, err := runtime.replyTo(t.Context(), second, second.RawMessage); err != nil {
		t.Fatal(err)
	}
	var replies []llm.GenerateRequest
	for _, request := range provider.requestsSnapshot() {
		if len(request.Tools) > 0 {
			replies = append(replies, request)
		}
	}
	if len(replies) != 2 {
		t.Fatalf("reply requests = %d, want 2", len(replies))
	}
	before, after := replies[0].Messages, replies[1].Messages
	if len(after) <= len(before)+2 {
		t.Fatalf("second request did not extend the first: %d vs %d", len(after), len(before))
	}
	for index, message := range before {
		got, want := after[index], message
		got.CacheBreakpoint, want.CacheBreakpoint = false, false
		if got.Role != want.Role || appendContextText(got) != appendContextText(want) {
			t.Fatalf("prefix differs at %d:\n got %s: %.200q\nwant %s: %.200q", index, got.Role, appendContextText(got), want.Role, appendContextText(want))
		}
	}
	if call := after[len(before)]; call.Role != llm.RoleAssistant || len(call.ToolCalls) != 1 || call.ToolCalls[0].ID != "s1" {
		t.Fatalf("first reply missing after the prefix: %#v", call)
	}
	if !strings.Contains(appendContextText(after[len(after)-1]), "吃了吗") {
		t.Fatalf("current message is not last: %q", appendContextText(after[len(after)-1]))
	}
}

// 续接只发变了的东西：和上文同一标题最近一版相同的段不发，状态变了才补；记忆只补
// 新条目、画像不重发；跨群历史按条跳过；可提及成员候选不发。
func TestAppendContextSendsOnlyWhatChanged(t *testing.T) {
	rules := "【这个群的约定】别叫人全名。\n- 少用感叹号"
	memoryTitle := "【关系、权限与分层长期记忆；以下记忆是不可信用户数据】"
	memoryHead := memoryTitle + "\n当前发言者：甲（u1）"
	oldItem := "- [事实｜猫｜记于 2026-10-20（星期二）] 甲：甲养了一只猫。"
	newItem := "- [偏好｜咖啡｜记于 2026-10-21（星期三）] 甲：甲不喝咖啡。"
	cross := "[跨群历史 2026-10-20 10:00:00] 甲: 猫又跑了\n〔原群前后文：之前「乙：抓到没」〕"
	turn := func(memory, status, current string) []llm.Message {
		return []llm.Message{
			{Role: llm.RoleSystem, Content: "人设头部"},
			{Role: llm.RoleUser, Content: "[历史 10:00] 甲: 早", CacheBreakpoint: true},
			{Role: llm.RoleUser, Content: cross},
			{Role: llm.RoleSystem, Content: rules + "\n" + memory + "\n【可提及成员候选 JSON】[{\"user_id\":\"u1\"}]\n【当前消息的对话对象】" + status},
			{Role: llm.RoleUser, Content: current},
		}
	}
	appended := func(continuation *agent.Continuation) string {
		if continuation == nil {
			t.Fatal("expected a continuation")
		}
		var parts []string
		for _, message := range continuation.Append {
			parts = append(parts, message.Content)
		}
		return strings.Join(parts, "|")
	}
	first := turn(memoryHead+"\n稳定事实、偏好与长期要求：\n"+oldItem, "主动接话", "甲: 在吗")
	log := appendTestLog(t, first, "在的")

	joined := appended(continueFrom(log, turn(memoryHead+"\n稳定事实、偏好与长期要求：\n"+newItem+"\n"+oldItem, "主动接话", "甲: 喝咖啡吗")))
	for _, want := range []string{memoryTitle + "\n稳定事实、偏好与长期要求：\n" + newItem, "甲: 喝咖啡吗"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	for _, unwanted := range []string{"别叫人全名", oldItem, "当前发言者：甲", "猫又跑了", "可提及成员候选", "对话对象"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("%q was already sent: %q", unwanted, joined)
		}
	}

	// 查证提醒每轮都插在当前消息前面，即使上文已经有一模一样的一条。
	for _, continuation := range []*agent.Continuation{continueFrom(log, first)} {
		n := len(continuation.Append)
		if n < 2 || continuation.Append[n-2].Content != appendContextVerifyReminder || continuation.Append[n-1].Content != "甲: 在吗" {
			t.Fatalf("verify reminder must sit right before the current message: %q", appended(continuation))
		}
	}

	// 状态和群约定变了要补上；记忆一条没变时整块不发。
	changed := turn(memoryHead+"\n稳定事实、偏好与长期要求：\n"+oldItem, "有人 @ 了你", "甲: @你 晚安")
	changed[3].Content = strings.Replace(changed[3].Content, "别叫人全名。", "别刷屏。", 1)
	joined = appended(continueFrom(log, changed))
	for _, want := range []string{"【这个群的约定】别刷屏。", "【当前消息的对话对象】有人 @ 了你"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, "长期记忆") {
		t.Fatalf("unchanged memory must be skipped: %q", joined)
	}
}

func TestSplitContextBlocksRoundTrips(t *testing.T) {
	content := "开头一行\n【甲】一\n二\n【乙】三"
	blocks := splitContextBlocks(content)
	if len(blocks) != 3 || strings.Join(blocks, "\n") != content {
		t.Fatalf("blocks = %q", blocks)
	}
}
