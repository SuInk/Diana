// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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
	continuation, _ := appendContextContinuation(log, messages, 0)
	return continuation
}

func appendTestLog(t *testing.T, messages []llm.Message, reply string) *appendContextLog {
	t.Helper()
	transcript := append(append([]llm.Message(nil), messages...),
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: agent.FinalizeToolName, Arguments: map[string]any{"text": reply}}}},
		llm.Message{Role: llm.RoleTool, ToolCallID: "c1", ToolName: agent.FinalizeToolName, Content: "已发送"})
	log := newAppendContextLog(&agent.Response{Transcript: transcript, ConversationKey: "k"}, appendContextHeadKey(messages), estimateMessagesTokens(messages), nil)
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
	if continuation, decision := appendContextContinuation(log, changed, 0); continuation != nil || decision.reason != "head_changed" {
		t.Fatalf("a changed head must restart, reason = %q", decision.reason)
	}
	long := appendTestTurn([]string{"[历史 10:00] 甲: 早", "[历史 10:05] 甲: " + strings.Repeat("很长的一段话。", 400)}, "", "甲: 还在吗")
	log.prefix = append(log.prefix, llm.Message{Role: llm.RoleUser, Content: strings.Repeat("旧内容。", 3000)})
	if continuation, decision := appendContextContinuation(log, long, 0); continuation != nil || decision.reason != "too_long" {
		t.Fatal("a prefix past the growth limit must restart")
	}
}

// 续接前缀是 Runner 发出的整份请求，含它自己加的系统提示、契约和工具段；上限要
// 按同一口径比，否则这些占一半时续一两轮就判 too_long（11-02 生产 46% 的主回复）。
func TestAppendContextGrowthCountsRunnerOverhead(t *testing.T) {
	first := appendTestTurn([]string{"[历史 10:00] 甲: 早"}, "", "甲: 在吗")
	full := estimateMessagesTokens(first)
	runnerPart := llm.Message{Role: llm.RoleSystem, Content: strings.Repeat("工具契约。", 2000)}
	overhead := llm.EstimateMessageTokens(runnerPart)
	transcript := append([]llm.Message{runnerPart}, first...)
	transcript = append(transcript, llm.Message{Role: llm.RoleAssistant, Content: "在的"})
	fresh := newAppendContextLog(&agent.Response{Transcript: transcript, ConversationKey: "k", PromptTokens: full + overhead}, appendContextHeadKey(first), full, nil)
	if fresh.overhead != overhead {
		t.Fatalf("overhead = %d, want %d", fresh.overhead, overhead)
	}
	second := appendTestTurn([]string{"[历史 10:00] 甲: 早", "[历史 10:01] 甲: 在吗"}, "", "甲: 今天吃什么")
	continuation, decision := appendContextContinuation(fresh, second, 0)
	if continuation == nil {
		t.Fatalf("a prefix about as long as one full request must continue, reason = %q", decision.reason)
	}
	carried := newAppendContextLog(&agent.Response{Transcript: append(continuation.Prefix, continuation.Append...), ConversationKey: "k", Continued: true, PromptTokens: decision.prefixTokens + decision.appendTokens}, appendContextHeadKey(second), decision.fullTokens, fresh)
	if carried.overhead != overhead {
		t.Fatalf("continued run must keep the overhead from the fresh one: %d", carried.overhead)
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
	// 上一轮只调 send 的收尾原样留成 send 调用加「已发送」，和历史里自己的发言同一个形态。
	if reply, result := after[len(before)], after[len(before)+1]; reply.Role != llm.RoleAssistant || len(reply.ToolCalls) != 1 ||
		reply.ToolCalls[0].Name != agent.FinalizeToolName || reply.ToolCalls[0].Arguments["text"] != "在的" ||
		result.Role != llm.RoleTool || result.ToolCallID != reply.ToolCalls[0].ID {
		t.Fatalf("first reply missing after the prefix: %#v / %#v", reply, result)
	}
	// 输出前检查只在重新组装的那一轮加：续接的请求里上一轮那条还在前缀里，不再重复，
	// 当前消息就是最后一条。
	check := "【输出前检查】\n" + replyUnclosedBracketRule
	checks := 0
	for _, message := range after {
		if message.Role == llm.RoleSystem && message.Content == check {
			checks++
		}
	}
	if checks != 1 {
		t.Fatalf("the output check must appear exactly once, got %d", checks)
	}
	if current := after[len(after)-1]; current.Role != llm.RoleUser || !strings.Contains(appendContextText(current), "吃了吗") {
		t.Fatalf("current message is not last: %#v", current)
	}
}

// 续接时发过的资料不再发：带记忆条目的段（含画像之后那段）按条去重，无标题的固定说明
// 发过就不发；当前消息连同它的注解照发；带标题的状态段 A、B 交替时照样发回 A。
func TestAppendContextSendsOnlyNewContent(t *testing.T) {
	portrait := "【当前发言者画像结束；其他人的身份以对应姓名的消息或来源为准】\n\n稳定事实、偏好与长期要求：\n- [偏好｜甲] 喜欢猫\n- [偏好｜乙] 不吃香菜"
	stateA := "【当前消息的对话对象】\n{\"reply_target\":\"a\"}"
	stateB := "【当前消息的对话对象】\n{\"reply_target\":\"b\"}"
	fixed := "Telegram 不使用合并转发，也不自动拼接多条消息。"
	current := func(text string) string {
		return "【当前需要回复的消息】【当前发言者】甲\n" + text + "\n\n群聊接话：闲聊随口一两句。"
	}
	build := func(state, memory, text string) []llm.Message {
		return []llm.Message{
			{Role: llm.RoleSystem, Content: "人设"},
			{Role: llm.RoleUser, Content: "[历史 1] 甲: 你好", CacheBreakpoint: true},
			{Role: llm.RoleUser, Content: memory},
			{Role: llm.RoleSystem, Content: state},
			{Role: llm.RoleSystem, Content: fixed},
			{Role: llm.RoleUser, Content: current(text)},
		}
	}
	first := build(stateA, portrait, "在吗")
	log := newAppendContextLog(&agent.Response{Transcript: first, ConversationKey: "k"}, appendContextHeadKey(first), estimateMessagesTokens(first), nil)
	// 测试消息很短，前缀几轮就会超过「完整组装的两倍」；把 Runner 那侧的开销放大，只测去重。
	log.overhead = 1 << 20

	// 同一个人、同样的状态，画像段多一条新记忆：续接不带记忆条目，新的也不带。
	second := build(stateA, portrait+"\n- [偏好｜丙] 周末爬山", "吃了吗")
	continuation, decision := appendContextContinuation(log, second, 0)
	if continuation == nil {
		t.Fatalf("没有续接：%s", decision.reason)
	}
	var sent []string
	for _, message := range continuation.Append {
		sent = append(sent, appendContextText(message))
	}
	joined := strings.Join(sent, "\n---\n")
	for _, unwanted := range []string{"喜欢猫", "不吃香菜", "周末爬山", fixed, "reply_target"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("发过的 %q 又发了一遍：\n%s", unwanted, joined)
		}
	}
	for _, wanted := range []string{"吃了吗", "群聊接话"} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("新内容 %q 没发：\n%s", wanted, joined)
		}
	}

	// 状态 A → B → A：切回 A 时要发回 A 那一版，不能因为「发过」就省掉。
	log = newAppendContextLog(&agent.Response{Transcript: append(log.prefix, continuation.Append...), ConversationKey: "k", Continued: true}, appendContextHeadKey(first), estimateMessagesTokens(second), log)
	third := build(stateB, portrait, "那你呢")
	continuation, _ = appendContextContinuation(log, third, 0)
	if continuation == nil {
		t.Fatal("第三轮没有续接")
	}
	sentB := false
	for _, message := range continuation.Append {
		if strings.Contains(appendContextText(message), "\"b\"") {
			sentB = true
		}
	}
	if !sentB {
		t.Fatal("状态变成 B 应当发出")
	}
	log = newAppendContextLog(&agent.Response{Transcript: append(log.prefix, continuation.Append...), ConversationKey: "k", Continued: true}, appendContextHeadKey(first), estimateMessagesTokens(third), log)
	fourth := build(stateA, portrait, "好的")
	continuation, _ = appendContextContinuation(log, fourth, 0)
	found := false
	for _, message := range continuation.Append {
		if strings.Contains(appendContextText(message), "\"a\"") {
			found = true
		}
	}
	if !found {
		t.Fatal("状态切回 A 时应当发回 A 那一版")
	}
}

// 续接只发变了的东西：和上文同一标题最近一版相同的段不发，状态变了才补；记忆条目
// 不带（新检索到的也不带）、画像不重发；跨群历史按条跳过；可提及成员候选不发。
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
	if !strings.Contains(joined, "甲: 喝咖啡吗") {
		t.Fatalf("missing the current message in %q", joined)
	}
	for _, unwanted := range []string{"别叫人全名", oldItem, newItem, memoryTitle, "当前发言者：甲", "猫又跑了", "可提及成员候选", "对话对象"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("%q was already sent: %q", unwanted, joined)
		}
	}

	// 续接不再插查证提醒：追加的最后一条就是当前消息。
	for _, continuation := range []*agent.Continuation{continueFrom(log, first)} {
		n := len(continuation.Append)
		if n < 1 || continuation.Append[n-1].Content != "甲: 在吗" || strings.Contains(appended(continuation), "【本轮提醒】") {
			t.Fatalf("the current message must come last with no reminder: %q", appended(continuation))
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

// 随发言者变化的工具规则在尾部单独成段：对话对象每条消息都变，规则没变就不跟着重发；
// 换了规则不同的人说话才整段再给，没有规则时也要写明。
func TestAppendContextSendsSpeakerToolRulesOnlyWhenTheyChange(t *testing.T) {
	admin := speakerToolRulesBlock("\nplatform 的群管操作对主人、群主和群管理员开放。")
	member := speakerToolRulesBlock("")
	turn := func(rules, target, current string) []llm.Message {
		return []llm.Message{
			// 头部给足量级，否则几条短消息就超过续接的两倍上限。
			{Role: llm.RoleSystem, Content: "人设头部" + strings.Repeat("。", 2000)},
			{Role: llm.RoleUser, Content: "[历史 10:00] 甲: 早", CacheBreakpoint: true},
			{Role: llm.RoleSystem, Content: rules + "\n【当前消息的对话对象】\n" + target},
			{Role: llm.RoleUser, Content: current},
		}
	}
	appended := func(messages []llm.Message, log *appendContextLog) string {
		continuation := continueFrom(log, messages)
		if continuation == nil {
			t.Fatal("expected a continuation")
		}
		var parts []string
		for _, message := range continuation.Append {
			parts = append(parts, message.Content)
		}
		return strings.Join(parts, "|")
	}
	log := appendTestLog(t, turn(admin, `{"reply_target":"none"}`, "甲: 禁言乙"), "好")

	joined := appended(turn(admin, `{"reply_target":"self"}`, "甲: 再解禁"), log)
	if strings.Contains(joined, speakerToolRulesTitle) || strings.Contains(joined, "群管操作") {
		t.Fatalf("unchanged speaker rules were sent again: %q", joined)
	}
	if !strings.Contains(joined, `【当前消息的对话对象】`+"\n"+`{"reply_target":"self"}`) {
		t.Fatalf("the changed addressing block is missing: %q", joined)
	}

	joined = appended(turn(member, `{"reply_target":"self"}`, "丙: 我也要禁言他"), log)
	if !strings.Contains(joined, speakerToolRulesTitle) || !strings.Contains(joined, "没有额外的工具规则") || strings.Contains(joined, "群管操作") {
		t.Fatalf("a speaker without the rules must get an explicit empty block: %q", joined)
	}
}
func TestSplitContextBlocksRoundTrips(t *testing.T) {
	content := "开头一行\n【甲】一\n二\n【乙】三"
	blocks := splitContextBlocks(content)
	if len(blocks) != 3 || strings.Join(blocks, "\n") != content {
		t.Fatalf("blocks = %q", blocks)
	}
}

// 中间一轮失败不清空记录：下一轮接着上一次成功的请求续，不用整段重新组装。
func TestReplyTurnContinuesFromLastSuccessAfterFailure(t *testing.T) {
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{
		`tool_calls:[{"id":"s1","name":"send","arguments":{"text":"在的"}}]`,
		`tool_calls:[{"id":"s3","name":"send","arguments":{"text":"好"}}]`,
	}}
	failing := false
	runtime := NewRuntime(BotConfig{AgentEnabled: true, BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		if failing {
			return failingLLMProvider{err: errors.New("upstream down")}, nil
		}
		return provider, nil
	})
	turn := func(id, text string, at int64) {
		event := interimTestEvent()
		event.MessageID, event.RawMessage, event.Time = id, text, at
		event.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}
		_, _ = runtime.replyTo(t.Context(), event, text)
	}
	base := interimTestEvent().Time
	turn("keep-1", "在吗", base)
	failing = true
	turn("keep-2", "失败这条", base+30)
	failing = false
	turn("keep-3", "继续", base+60)

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
	for index, message := range before {
		got, want := after[index], message
		if got.Role != want.Role || appendContextText(got) != appendContextText(want) {
			t.Fatalf("third turn did not continue the first at %d", index)
		}
	}
}

// 11-02 生产样本：上一轮回复在记录里是不带标记的正文，下一轮历史区把同一条出站
// 消息按 reply 段渲染成带引用标记的 assistant 行，续接时不能再追加一遍。
func TestAppendContextSkipsOwnReplyRenderedWithQuoteMarker(t *testing.T) {
	const reply = "对，正好四个全是 y，y 家军实锤了"
	first := appendTestTurn([]string{"[历史 10:07:03] 池宇健: 我发现活跃的bot都是y开头的"}, "", "【当前需要回复的消息】池宇健: 我发现活跃的bot都是y开头的")
	transcript := append(append([]llm.Message(nil), first...), llm.Message{Role: llm.RoleAssistant, Content: reply})
	log := newAppendContextLog(&agent.Response{Transcript: transcript, ConversationKey: "k"}, appendContextHeadKey(first), estimateMessagesTokens(first), nil)
	if log == nil {
		t.Fatal("log not created")
	}
	second := appendTestTurn([]string{
		"[历史 10:07:03] 池宇健: 我发现活跃的bot都是y开头的",
		"[历史 10:07:17] 池宇健: 幽夜 月白 yuki 柚子",
		"[历史 10:07:58] suakitsu: 你们四个y  是要起y吗？",
	}, "", "【当前需要回复的消息】suakitsu: 你们四个y  是要起y吗？")
	second = append(second[:3], append([]llm.Message{{Role: llm.RoleAssistant, Content: `<diana-quote id="im_message_ab257222a6f8"/>` + reply}}, second[3:]...)...)
	continuation := continueFrom(log, second)
	if continuation == nil {
		t.Fatal("expected a continuation")
	}
	for _, message := range continuation.Append {
		if message.Role == llm.RoleAssistant {
			t.Fatalf("own reply appended again: %q", message.Content)
		}
	}
	if !strings.Contains(continuation.Append[0].Content, "幽夜 月白 yuki 柚子") {
		t.Fatalf("new history missing: %#v", continuation.Append)
	}

	// 真正新的一句话仍然照常追加。
	second[3].Content = `<diana-quote id="im_message_ab257222a6f8"/>哈哈 y 势力集结了，不过他们四个好像不太想带我玩`
	continuation = continueFrom(log, second)
	found := false
	for _, message := range continuation.Append {
		found = found || message.Role == llm.RoleAssistant && message.Content == second[3].Content
	}
	if !found {
		t.Fatalf("new reply missing: %#v", continuation.Append)
	}
}

// 历史里自己的发言是 send 调用加「已发送」：记录里已有的那句两半都不再追加，新的那句
// 两半一起追加，不留不成对的调用。
func TestAppendContextKeepsHistoryReplyPairsTogether(t *testing.T) {
	first := appendTestTurn([]string{"[历史 10:00] 甲: 早"}, "", "【当前需要回复的消息】甲: 在吗")
	log := appendTestLog(t, first, "在的")
	second := appendTestTurn([]string{"[历史 10:00] 甲: 早", "[历史 10:02] 乙: 吃了吗"}, "", "【当前需要回复的消息】乙: 吃了吗")
	old := agent.HistoryReplyMessages("old", `<diana-quote id="im_message_1"/>在的`)
	fresh := agent.HistoryReplyMessages("new", "刚吃完")
	history := append(append([]llm.Message{second[1]}, old...), second[2])
	history = append(history, fresh...)
	history[len(history)-1].CacheBreakpoint = true
	second[2].CacheBreakpoint = false
	second = append(append([]llm.Message{second[0]}, history...), second[3:]...)
	continuation := continueFrom(log, second)
	if continuation == nil {
		t.Fatal("expected a continuation")
	}
	var calls, results int
	for index, message := range continuation.Append {
		if !llm.IsHistoryReplyMessage(message) {
			continue
		}
		if message.Role == llm.RoleAssistant {
			calls++
			if text := llm.HistoryReplyText(message); text != "刚吃完" {
				t.Fatalf("already-sent reply appended again: %q", text)
			}
			if next := continuation.Append[index+1]; next.Role != llm.RoleTool || next.ToolCallID != message.ToolCalls[0].ID {
				t.Fatalf("call not followed by its result: %#v", continuation.Append)
			}
			continue
		}
		results++
	}
	if calls != 1 || results != 1 {
		t.Fatalf("calls=%d results=%d in %#v", calls, results, continuation.Append)
	}
}

// auditBlockingProvider 让第一次发送前审核停住，等测试把同一个人的补充消息并进来。
type auditBlockingProvider struct {
	*sequenceLLMProvider
	once         sync.Once
	auditStarted chan struct{}
	releaseAudit chan struct{}
}

func (p *auditBlockingProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	for _, message := range req.Messages {
		if strings.Contains(message.Content, directReplyTopicPrompt) {
			return &llm.GenerateResponse{Text: `{"relation":"supplement","confidence":0.99}`}, nil
		}
		if strings.Contains(message.Content, "发送前审核器") {
			p.once.Do(func() {
				close(p.auditStarted)
				<-p.releaseAudit
			})
		}
	}
	return p.sequenceLLMProvider.Generate(ctx, req)
}

// 11-04 生产：回复答好了正在审核，对方补了个表情，这一版因为漏了补充被丢掉重来。
// 续接记录里还留着那条没发出的草稿，重来的一版以为「刚答过」就静默了，问题被漏掉。
func TestSupplementedReplyDoesNotContinueFromDiscardedDraft(t *testing.T) {
	disabled := false
	provider := &auditBlockingProvider{
		sequenceLLMProvider: &sequenceLLMProvider{replies: []string{
			`tool_calls:[{"id":"s1","name":"send","arguments":{"text":"只回答第一条的草稿"}}]`,
			`tool_calls:[{"id":"s2","name":"send","arguments":{"text":"两条一起回答"}}]`,
		}},
		auditStarted: make(chan struct{}), releaseAudit: make(chan struct{}),
	}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{AgentEnabled: true, BotAccount: "42", BotReplyLoopDetectionEnabled: &disabled},
		channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })

	root := directedGroupMessage("root", "user-1", "你是什么模型")
	runtime.noteDirectedInbound(root)
	runtime.remember(root)
	done := make(chan error, 1)
	go func() {
		_, err := runtime.replyAndRecord(withOutboundTurn(context.Background(), "root-turn"), root, root.RawMessage, "replied")
		done <- err
	}()
	select {
	case <-provider.auditStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("send audit did not start")
	}
	followUp := directedGroupMessage("follow-up", "user-1", "顺便说下上下文多长")
	runtime.noteDirectedInbound(followUp)
	if _, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), followUp); handled || outcome != "merged_into_reply" {
		t.Fatalf("follow-up handled=%v outcome=%q", handled, outcome)
	}
	close(provider.releaseAudit)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("merged reply did not finish")
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || sent[0].Text != "两条一起回答" {
		t.Fatalf("sent=%#v", sent)
	}
	var replies []llm.GenerateRequest
	for _, request := range provider.requestsSnapshot() {
		if len(request.Tools) > 0 {
			replies = append(replies, request)
		}
	}
	if len(replies) != 2 {
		t.Fatalf("reply requests = %d, want draft plus regeneration", len(replies))
	}
	for _, message := range replies[1].Messages {
		if strings.Contains(appendContextText(message), "只回答第一条的草稿") {
			t.Fatalf("regeneration still sees the discarded draft: %s %q", message.Role, appendContextText(message))
		}
		for _, call := range message.ToolCalls {
			if text, _ := call.Arguments["text"].(string); strings.Contains(text, "只回答第一条的草稿") {
				t.Fatalf("regeneration still sees the discarded draft call: %#v", call)
			}
		}
	}
}

// 当前消息末尾的固定注解（群聊篇幅）上文有过就不再带，没有过照带；跨群历史续接时
// 一律不追加，哪怕是这一轮新检索到的。
func TestAppendContextDropsRepeatedNoteAndCrossGroupLines(t *testing.T) {
	note := "群聊接话：闲聊随口一两句。"
	cross := "[跨群历史 2026-10-20 10:00:00] 甲: 猫又跑了"
	turn := func(crossLine, current string) []llm.Message {
		return []llm.Message{
			{Role: llm.RoleSystem, Content: "人设头部" + strings.Repeat("。", 2000)},
			{Role: llm.RoleUser, Content: "[历史 10:00] 甲: 早", CacheBreakpoint: true},
			{Role: llm.RoleUser, Content: crossLine},
			{Role: llm.RoleUser, Content: current},
		}
	}
	first := turn(cross, "【当前需要回复的消息】甲: 在吗\n\n"+note)
	log := appendTestLog(t, first, "在的")
	second := turn("[跨群历史 2026-10-21 09:00:00] 乙: 新检索到的一条", "【当前需要回复的消息】甲: 吃了吗\n\n"+note)

	continuation, decision := appendContextContinuation(log, second, 0, note)
	if continuation == nil {
		t.Fatalf("expected a continuation, reason = %q", decision.reason)
	}
	var parts []string
	for _, message := range continuation.Append {
		parts = append(parts, message.Content)
	}
	joined := strings.Join(parts, "|")
	if last := parts[len(parts)-1]; last != "【当前需要回复的消息】甲: 吃了吗" {
		t.Fatalf("the repeated note must be dropped from the current message: %q", last)
	}
	if strings.Contains(joined, "跨群历史") {
		t.Fatalf("cross-group lines must not be appended: %q", joined)
	}
	// 上文没有过这条注解时照带。
	fresh := appendTestLog(t, turn(cross, "【当前需要回复的消息】甲: 在吗"), "在的")
	continuation, _ = appendContextContinuation(fresh, second, 0, note)
	if last := continuation.Append[len(continuation.Append)-1].Content; !strings.HasSuffix(last, note) {
		t.Fatalf("a note not yet in the prefix must stay: %q", last)
	}
}

// 配了「单次请求上下文上限」时续接也要守：续接后的请求超过它就重新组装；0 表示不限，
// 只看两倍上限。
func TestAppendContextRestartsPastConfiguredContextLimit(t *testing.T) {
	first := appendTestTurn([]string{"[历史 10:00] 甲: 早"}, "", "甲: 在吗")
	log := appendTestLog(t, first, "在的")
	second := appendTestTurn([]string{"[历史 10:00] 甲: 早", "[历史 10:06] 甲: 新的一句"}, "", "甲: 还在吗")

	continuation, decision := appendContextContinuation(log, second, 0)
	if continuation == nil {
		t.Fatalf("no limit configured must continue, reason = %q", decision.reason)
	}
	sent := decision.prefixTokens + decision.appendTokens
	if continuation, decision := appendContextContinuation(log, second, sent); continuation == nil {
		t.Fatalf("a request exactly at the limit must continue, reason = %q", decision.reason)
	}
	if continuation, decision := appendContextContinuation(log, second, sent-1); continuation != nil || decision.reason != "context_limit" {
		t.Fatalf("a request past the limit must restart, reason = %q", decision.reason)
	}
}
