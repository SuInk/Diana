// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"reflect"
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

// withoutTurnOnly 去掉只用于本轮的消息，和 Runner 记录前缀时的做法一样。
func withoutTurnOnly(messages []llm.Message) []llm.Message {
	var kept []llm.Message
	for _, message := range messages {
		if !message.TurnOnly {
			message.CacheBreakpoint = false
			kept = append(kept, message)
		}
	}
	return kept
}

// appendTestPrefix 是完整组装的一轮记下来的前缀：尾部照常留下，只去掉源头标成只用于
// 本轮的消息。
func appendTestPrefix(messages []llm.Message) []llm.Message {
	return withoutTurnOnly(messages)
}

func appendTestLog(t *testing.T, messages []llm.Message, reply string) *appendContextLog {
	t.Helper()
	transcript := append(appendTestPrefix(messages), agent.HistoryReplyMessages("c1", reply)...)
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

// appendTestReplyRequests 取出主回复的请求（带工具定义的那些）。
func appendTestReplyRequests(provider *sequenceLLMProvider) []llm.GenerateRequest {
	var replies []llm.GenerateRequest
	for _, request := range provider.requestsSnapshot() {
		if len(request.Tools) > 0 {
			replies = append(replies, request)
		}
	}
	return replies
}

// requirePrefixOf 要求 request 以 prefix 逐条开头，而且比它长。
func requirePrefixOf(t *testing.T, prefix, request []llm.Message) {
	t.Helper()
	if len(request) <= len(prefix) {
		t.Fatalf("request does not extend the prefix: %d vs %d", len(request), len(prefix))
	}
	for index, want := range prefix {
		got := request[index]
		if got.TurnOnly || got.Role != want.Role || appendContextText(got) != appendContextText(want) || !reflect.DeepEqual(got.ToolCalls, want.ToolCalls) || got.ToolCallID != want.ToolCallID {
			t.Fatalf("prefix differs at %d:\n got %s: %.200q\nwant %s: %.200q", index, got.Role, appendContextText(got), want.Role, appendContextText(want))
		}
	}
}

// requireChatOnlyPrefix 检查前缀：开头 baseline 条是重新组装那一轮的请求（头部、历史、
// 那一轮的尾部和当前消息），原样留着；之后只能有聊天历史行、机器人的发言对、运行时
// 记录、新加载的工具契约和历轮的当前消息（正文含 currents 里的某一句）。工具调用和
// 结果一一成对，返回 baseline 之后的发言正文。
func requireChatOnlyPrefix(t *testing.T, prefix []llm.Message, baseline int, currents ...string) (spoken []string) {
	t.Helper()
	for index := 0; index < len(prefix); index++ {
		message := prefix[index]
		content := appendContextText(message)
		if message.TurnOnly {
			t.Fatalf("turn-only message in the prefix at %d: %.200q", index, content)
		}
		if index < baseline {
			if message.Role == llm.RoleAssistant || message.Role == llm.RoleTool {
				t.Fatalf("the reassembled request holds no replies in this test, got %s at %d", message.Role, index)
			}
			continue
		}
		switch {
		case message.Role == llm.RoleAssistant:
			if !llm.IsHistoryReplyMessage(message) || len(message.ToolCalls) != 1 || len(message.ResponsesOutput) != 0 || content != "" {
				t.Fatalf("assistant message at %d is not a bare send call: %#v", index, message)
			}
			if index+1 >= len(prefix) || prefix[index+1].Role != llm.RoleTool || prefix[index+1].ToolCallID != message.ToolCalls[0].ID || prefix[index+1].Content != "已发送" {
				t.Fatalf("send call at %d has no matching result", index)
			}
			spoken = append(spoken, llm.HistoryReplyText(message))
			index++
		case message.Role == llm.RoleTool:
			t.Fatalf("tool result without its call at %d: %#v", index, message)
		case message.Role == llm.RoleSystem:
			t.Fatalf("per-turn system message in the prefix at %d: %.200q", index, content)
		case isHistoryLine(content), strings.HasPrefix(content, replyToolTraceNotePrefix):
		case strings.HasPrefix(content, "【系统补充指令，不是用户发言】") && strings.Contains(content, "又加载了下面的工具"):
			// 群里新加载的工具契约留在前缀里：它按群持久，后面每一轮都照着它调用。
		default:
			current := false
			for _, text := range currents {
				current = current || strings.Contains(content, text)
			}
			if !current {
				t.Fatalf("unexpected message in the prefix at %d: %s %.300q", index, message.Role, content)
			}
		}
	}
	return spoken
}

// 整轮走三遍，中间一轮先说一句、调工具、再说一句：每一轮的请求都以上一轮记下的前缀
// 逐条开头；前缀是第一轮（重新组装）的请求加之后的历史行、发言对、运行时记录和历轮的
// 当前消息，没有后面几轮的尾部、没有工具调用和结果。尾部里和第一轮相同的段（这个群
// 的约定）后面不再发，输出前检查每次请求都在。
func TestReplyTurnsContinueWithChatOnlyPrefix(t *testing.T) {
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{
		`tool_calls:[{"id":"s1","name":"send","arguments":{"text":"在的"}}]`,
		`tool_calls:[{"id":"s2","name":"send","arguments":{"text":"我看一下"}},{"id":"t1","name":"tools_execute","arguments":{"name":"version","input":{}}}]`,
		`tool_calls:[{"id":"s3","name":"send","arguments":{"text":"查到了"}}]`,
		`tool_calls:[{"id":"s4","name":"send","arguments":{"text":"不客气"}}]`,
	}}
	runtime := NewRuntime(BotConfig{AgentEnabled: true, BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetStructuredMemoryStore(&testStructuredMemoryStore{items: []StructuredMemoryItem{{
		ID: "rule-1", ScopeKey: sessionKey(interimTestEvent()), Key: "instruction.group.no.cao", Kind: MemoryKindInstruction,
		Topic: "说话要求", Content: "群友10002要求 Diana 在本群不再说「草」",
		Visibility: MemoryVisibilitySession, Confidence: 0.95, Importance: 0.7,
	}}})
	texts := []string{"在吗", "你现在是什么版本", "谢谢"}
	var prefixes [][]llm.Message
	base := interimTestEvent().Time + 600
	// 先有一条群聊历史：历史区的终点（缓存断点）在，第一轮才分得清哪些是尾部。
	seed := interimTestEvent()
	seed.MessageID, seed.UserID, seed.SenderName, seed.RawMessage, seed.Time = "append-seed", "10002", "Bob", "早上好", base-60
	seed.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "早上好"}}}
	runtime.remember(seed)
	for index, text := range texts {
		event := interimTestEvent()
		event.MessageID, event.RawMessage, event.Time = "append-"+string(rune('1'+index)), text, base+int64(index)*30
		event.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}
		if _, err := runtime.replyTo(withReplyToolTrace(t.Context()), event, text); err != nil {
			t.Fatal(err)
		}
		log := runtime.appendContexts.get(appendContextKey(event))
		if log == nil {
			t.Fatalf("turn %d left no record", index+1)
		}
		prefixes = append(prefixes, log.prefix)
	}
	replies := appendTestReplyRequests(provider)
	if len(replies) != 4 {
		t.Fatalf("reply requests = %d, want 4 (the second turn takes two steps)", len(replies))
	}
	// 同一轮的第二步照常看得到这一轮的工具调用和结果。
	sawResult := false
	for _, message := range replies[2].Messages {
		sawResult = sawResult || message.Role == llm.RoleTool && message.ToolCallID == "t1"
	}
	if !sawResult {
		t.Fatal("the second step of a turn must still see that turn's tool result")
	}
	// 每一轮记下的前缀 = 这一轮第一步的请求去掉只用于本轮的消息，加这一轮的发言（和
	// 一行工具记录）；下一轮的请求以它逐条开头。模型客户端看到的请求经过隐私代理改写
	// （第一条 system、账号别名），所以拿客户端看到的两份请求互相比。
	firstStep := []int{0, 1, 3}
	added := []int{2, 5}
	baseline := len(withoutTurnOnly(replies[0].Messages))
	for turn, wantAdded := range added {
		kept := withoutTurnOnly(replies[firstStep[turn]].Messages)
		prefix, next := prefixes[turn], replies[firstStep[turn+1]].Messages
		if len(prefix) != len(kept)+wantAdded {
			t.Fatalf("turn %d: prefix = %d messages, want the %d kept ones plus %d", turn+1, len(prefix), len(kept), wantAdded)
		}
		requirePrefixOf(t, kept, next)
		requirePrefixOf(t, append(kept, prefix[len(kept):]...), next)
		requireChatOnlyPrefix(t, next[:len(prefix)], baseline, texts[:turn+1]...)
	}

	wantSpoken := [][]string{{"在的"}, {"在的", "我看一下", "查到了"}, {"在的", "我看一下", "查到了", "不客气"}}
	for index, prefix := range prefixes {
		spoken := requireChatOnlyPrefix(t, prefix, baseline, texts[:index+1]...)
		if !reflect.DeepEqual(spoken, wantSpoken[index]) {
			t.Fatalf("turn %d: spoken = %q, want %q", index+1, spoken, wantSpoken[index])
		}
		for _, message := range prefix {
			content := appendContextText(message)
			for _, banned := range []string{"【输出前检查】", "【本轮工具执行状态", appendContextVerifyReminder, "当前运行时钟"} {
				if strings.Contains(content, banned) {
					t.Fatalf("turn %d: %q leaked into the prefix: %.200q", index+1, banned, content)
				}
			}
		}
	}
	// 第一轮的尾部留在前缀里作基准，后面几轮变了的段只给当轮用：每个标题在前缀里始终
	// 只有第一轮那一版。
	for _, title := range []string{"【当前消息的对话对象】", "【当前发言者工具环境】", "【这个群的约定】"} {
		count := 0
		for _, message := range prefixes[2] {
			count += strings.Count(appendContextText(message), title)
		}
		if count != 1 {
			t.Fatalf("%s appears %d times in the final prefix, want only the reassembled turn's version", title, count)
		}
	}
	// 这个群的约定每轮都一样：每份请求里正文只出现一次，后面几轮那一次在前缀里。
	for index, request := range replies {
		rules, at := 0, -1
		for position, message := range request.Messages {
			if n := strings.Count(appendContextText(message), "【这个群的约定】"); n > 0 {
				rules += n
				at = position
			}
		}
		if rules != 1 {
			t.Fatalf("request %d: the group rules appear %d times, want once", index+1, rules)
		}
		if index > 0 && at >= baseline {
			t.Fatalf("request %d: the group rules must come from the prefix, found at %d past %d", index+1, at, baseline)
		}
	}
	// 用过工具的那一轮只留一行运行时记录，紧跟在收尾那句后面。
	notes := 0
	for index, message := range prefixes[2] {
		if !strings.HasPrefix(message.Content, replyToolTraceNotePrefix) {
			continue
		}
		notes++
		if want, _ := replyToolTraceNote([]ReplyToolCall{{Tool: "version"}}); message.Content != want.Content || message.Role != want.Role {
			t.Fatalf("tool note = %q", message.Content)
		}
		if before := prefixes[2][index-2]; llm.HistoryReplyText(before) != "查到了" {
			t.Fatalf("the tool note must follow the closing reply, got %#v", before)
		}
	}
	if notes != 1 {
		t.Fatalf("tool notes in the prefix = %d, want 1", notes)
	}
	// 每份请求末尾都有输出前检查；续接的轮次当前消息前有查证提醒。
	check := "【输出前检查】\n" + replyUnclosedBracketRule
	for index, request := range replies {
		checks := 0
		for _, message := range request.Messages {
			if message.Role == llm.RoleSystem && message.Content == check {
				checks++
			}
		}
		if last := request.Messages[len(request.Messages)-1]; checks != 1 || last.Content != check || !last.TurnOnly {
			t.Fatalf("request %d: the output check must be the last message, exactly once (got %d)", index+1, checks)
		}
	}
	for _, index := range []int{1, 3} {
		found := false
		for _, message := range replies[index].Messages {
			found = found || message.TurnOnly && message.Content == appendContextVerifyReminder
		}
		if !found {
			t.Fatalf("request %d: a continued turn must carry the verify reminder", index+1)
		}
	}
}

// 尾部按段和前缀比。重新组装那一轮的尾部进前缀作基准（源头标成只用于本轮的除外）；
// 续接时带标题的段和前缀里同标题那一版逐字相同就不发，不同就发、只用于本轮，不带
// 标题的段前缀里出现过就不发。发出去的不进前缀，所以同一处变化下一轮还会再发，
// A → B → A 时换回 A 又和前缀相同、不发。
func TestAppendContextSendsOnlyTailBlocksThatDifferFromPrefix(t *testing.T) {
	rules := "【这个群的约定】别说草。\n- 少用感叹号"
	fixed := "Telegram 不使用合并转发，也不自动拼接多条消息。"
	cross := "[跨群历史 2026-10-20 10:00:00] 甲: 猫又跑了"
	state := func(target string) string {
		return "【当前消息的对话对象】\n{\"reply_target\":\"" + target + "\"}"
	}
	build := func(history []string, target, extra, current string) []llm.Message {
		messages := []llm.Message{{Role: llm.RoleSystem, Content: "人设头部" + strings.Repeat("。", 2000)}}
		for _, line := range history {
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: line})
		}
		messages[len(messages)-1].CacheBreakpoint = true
		messages = append(messages,
			llm.Message{Role: llm.RoleUser, Content: cross, TurnOnly: true},
			llm.Message{Role: llm.RoleSystem, Content: rules + "\n" + state(target)},
			llm.Message{Role: llm.RoleSystem, Content: fixed})
		if extra != "" {
			messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: extra})
		}
		return append(messages, llm.Message{Role: llm.RoleUser, Content: current})
	}
	history := []string{"[历史 10:00] 甲: 早"}
	first := build(history, "a", "", "【当前需要回复的消息】甲: 在吗")
	log := appendTestLog(t, first, "在的")
	// 完整组装那一轮：尾部进前缀，源头标成只用于本轮的跨群参考不进。
	if len(log.prefix) != len(first)-1+2 {
		t.Fatalf("the reassembled turn must keep its tail in the prefix: %d messages", len(log.prefix))
	}
	for _, message := range log.prefix {
		if strings.Contains(message.Content, "猫又跑了") {
			t.Fatal("a turn-only message stayed in the prefix")
		}
	}
	baseline := len(log.prefix)
	extra := "这一轮才有的一段无标题说明。"
	for round, tc := range []struct {
		target, extra string
		want          []string // 尾部里应该发出的段，按顺序；跨群参考和查证提醒另算
	}{
		{"a", "", nil},                  // 和前缀一样：尾部一段都不发
		{"b", "", []string{state("b")}}, // 对话对象变了：只发这一段
		{"b", "", []string{state("b")}}, // 前缀没变，同一处变化还要再发
		{"a", extra, []string{extra}},   // 换回 A 不发；新的无标题段要发
		{"a", extra, []string{extra}},   // 无标题段没进前缀，下一轮还发
		{"c", "", []string{state("c")}},
	} {
		current := "第" + string(rune('1'+round)) + "问"
		history = append(history, "[历史 10:0"+string(rune('1'+round))+"] 甲: "+current)
		full := build(history, tc.target, tc.extra, "【当前需要回复的消息】甲: "+current)
		continuation, decision := appendContextContinuation(log, full, 0)
		if continuation == nil {
			t.Fatalf("round %d: no continuation: %s", round, decision.reason)
		}
		appended := continuation.Append
		if appended[0].Content != history[len(history)-1] || appended[0].TurnOnly {
			t.Fatalf("round %d: new history line = %#v", round, appended[0])
		}
		if appended[1].Content != cross || !appended[1].TurnOnly {
			t.Fatalf("round %d: the turn-only cross-group line must be sent every turn: %#v", round, appended[1])
		}
		var got []string
		for _, message := range appended[2 : len(appended)-2] {
			if !message.TurnOnly {
				t.Fatalf("round %d: a kept tail block must be turn-only: %#v", round, message)
			}
			got = append(got, message.Content)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("round %d: tail sent %q, want %q", round, got, tc.want)
		}
		if reminder := appended[len(appended)-2]; reminder.Content != appendContextVerifyReminder || !reminder.TurnOnly {
			t.Fatalf("round %d: verify reminder must sit right before the current message: %#v", round, reminder)
		}
		if last := appended[len(appended)-1]; !strings.HasSuffix(last.Content, current) || last.TurnOnly {
			t.Fatalf("round %d: current message = %#v", round, last)
		}
		transcript := append(append([]llm.Message(nil), continuation.Prefix...), withoutTurnOnly(appended)...)
		transcript = append(transcript, agent.HistoryReplyMessages("r"+string(rune('1'+round)), "回复"+current)...)
		log = newAppendContextLog(&agent.Response{Transcript: transcript, ConversationKey: "k", Continued: true}, appendContextHeadKey(full), estimateMessagesTokens(full), log)
		// 前缀里每个标题始终只有重新组装那一轮的那一版，这一轮才有的内容不留。
		if log.latest["【当前消息的对话对象】"] != state("a") || log.latest["【这个群的约定】"] != rules {
			t.Fatalf("round %d: the prefix must keep only the reassembled turn's blocks: %q", round, log.latest)
		}
		for _, message := range log.prefix[baseline:] {
			if message.Role == llm.RoleSystem || strings.Contains(message.Content, "reply_target") || strings.Contains(message.Content, extra) {
				t.Fatalf("round %d: tail content entered the prefix: %#v", round, message)
			}
		}
		if len(log.prefix) != baseline+(round+1)*4 {
			t.Fatalf("round %d: each continued turn adds one history line, the current message and one reply: %d", round, len(log.prefix)-baseline)
		}
	}
}

func TestSplitContextBlocksRoundTrips(t *testing.T) {
	content := "开头一行\n【甲】一\n二\n【乙】三"
	blocks := splitContextBlocks(content)
	if len(blocks) != 3 || strings.Join(blocks, "\n") != content {
		t.Fatalf("blocks = %q", blocks)
	}
}

// 前缀里工具调用和结果都不留，只在发言后面补一行「调用过哪些工具」，和重新组装时
// 历史里那行逐字相同：放在收尾那句的第一条后面，中途发言排在它前面；收尾没说话就放在
// 最后一句中途发言后面；整轮没说话或没调过工具就不加。
func TestAppendContextPrefixAddsToolNoteAfterReply(t *testing.T) {
	base := appendTestPrefix(appendTestTurn([]string{"[历史 10:00] 甲: 早"}, "", "【当前需要回复的消息】甲: 查一下天气"))
	baseline := len(base)
	search := agent.Step{Tool: "web_search", Input: map[string]any{"query": "天气"}, Output: "晴"}
	interim := agent.Step{Tool: agent.FinalizeToolName, Input: map[string]any{"text": "我查一下"}, Output: "已发送"}
	wantNote, _ := replyToolTraceNote([]ReplyToolCall{{Tool: "web_search", Summary: "天气"}})
	spoken := func(texts ...string) []llm.Message {
		transcript := append([]llm.Message(nil), base...)
		for index, text := range texts {
			transcript = append(transcript, agent.HistoryReplyMessages("p"+string(rune('1'+index)), text)...)
		}
		return transcript
	}
	shape := func(prefix []llm.Message) string {
		var parts []string
		for _, message := range prefix[len(base):] {
			switch {
			case message.Role == llm.RoleAssistant:
				parts = append(parts, "说:"+llm.HistoryReplyText(message))
			case message.Role == llm.RoleTool:
			case message.Content == wantNote.Content && message.Role == wantNote.Role:
				parts = append(parts, "记录")
			default:
				parts = append(parts, "?"+message.Content)
			}
		}
		return strings.Join(parts, "|")
	}
	for _, tc := range []struct {
		name  string
		steps []agent.Step
		texts []string
		want  string
	}{
		{"tool then reply", []agent.Step{search}, []string{"晴天"}, "说:晴天|记录"},
		{"reply in two bubbles", []agent.Step{search}, []string{"晴天", "记得防晒"}, "说:晴天|记录|说:记得防晒"},
		{"interim, tool, reply", []agent.Step{interim, search}, []string{"我查一下", "晴天"}, "说:我查一下|说:晴天|记录"},
		{"interim, tool, silent", []agent.Step{interim, search}, []string{"我查一下"}, "说:我查一下|记录"},
		{"tool, silent", []agent.Step{search}, nil, ""},
		{"no tool", nil, []string{"在的"}, "说:在的"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := appendContextPrefix(&agent.Response{Transcript: spoken(tc.texts...), Steps: tc.steps})
			if got := shape(prefix); got != tc.want {
				t.Fatalf("shape = %q, want %q", got, tc.want)
			}
			requireChatOnlyPrefix(t, prefix, baseline)
		})
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

	replies := appendTestReplyRequests(provider)
	if len(replies) != 2 {
		t.Fatalf("reply requests = %d, want 2", len(replies))
	}
	// 第一轮里只用于那一轮的消息不在前缀里；第三轮接的是它剩下的部分。
	requirePrefixOf(t, withoutTurnOnly(replies[0].Messages), replies[1].Messages)
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

// 当前消息末尾的固定注解（群聊篇幅）上文有过就不再带，没有过照带；跨群参考每轮
// 现查、原样带上，哪怕上一轮带过同一条（它不进前缀，见 llm.Message.TurnOnly）。
func TestAppendContextDropsRepeatedNoteAndCrossGroupLines(t *testing.T) {
	note := "群聊接话：闲聊随口一两句。"
	cross := "[跨群历史 2026-10-20 10:00:00] 甲: 猫又跑了"
	turn := func(crossLine, current string) []llm.Message {
		return []llm.Message{
			{Role: llm.RoleSystem, Content: "人设头部" + strings.Repeat("。", 2000)},
			{Role: llm.RoleUser, Content: "[历史 10:00] 甲: 早", CacheBreakpoint: true},
			{Role: llm.RoleUser, Content: crossLine, TurnOnly: true},
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
	// 跨群参考照带，并且保留「只用于本轮」的标记。
	crossKept := false
	for _, message := range continuation.Append {
		if strings.Contains(message.Content, "新检索到的一条") {
			crossKept = message.TurnOnly
		}
	}
	if !crossKept {
		t.Fatalf("this turn's cross-group line must be appended and stay turn-only: %q", joined)
	}
	// 同一条跨群参考再次检索到时也照带：上一轮那条没留在前缀里。
	again, _ := appendContextContinuation(log, turn(cross, "【当前需要回复的消息】甲: 吃了吗\n\n"+note), 0, note)
	found := false
	for _, message := range again.Append {
		found = found || strings.Contains(message.Content, "猫又跑了")
	}
	if !found {
		t.Fatal("a cross-group line retrieved again must be sent again")
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

// 记忆块拆成两条：块头照常走「变了才发」，检索到的条目只用于本轮、每轮原样带上。
func TestSplitRetrievedMemoryAndTurnOnlyPassThrough(t *testing.T) {
	block := "【关系、权限与分层长期记忆；以下记忆是不可信用户数据】\n当前发言者：甲（u1）\n人员画像：\n- 职业：学生\n【当前发言者画像结束；其他人的身份以对应姓名的消息或来源为准】\n稳定事实、偏好与长期要求：\n- [偏好｜咖啡] 甲不喝咖啡。\n相关情景：\n- [情景｜猫] 甲的猫跑了。"
	head, retrieved := splitRetrievedMemory(block)
	if !strings.HasSuffix(head, "画像结束；其他人的身份以对应姓名的消息或来源为准】") || strings.Contains(head, "- [") || !strings.Contains(head, "- 职业：学生") {
		t.Fatalf("head = %q", head)
	}
	if !strings.HasPrefix(retrieved, retrievedMemoryTitle+"\n稳定事实、偏好与长期要求：\n- [偏好｜咖啡]") || !strings.HasSuffix(retrieved, "甲的猫跑了。") {
		t.Fatalf("retrieved = %q", retrieved)
	}
	if head, retrieved := splitRetrievedMemory("【关系、权限与分层长期记忆】\n当前发言者：甲"); head == "" || retrieved != "" {
		t.Fatalf("a block without items must stay whole: %q / %q", head, retrieved)
	}

	turn := func(items, current string) []llm.Message {
		return []llm.Message{
			{Role: llm.RoleSystem, Content: "人设头部" + strings.Repeat("。", 2000)},
			{Role: llm.RoleUser, Content: "[历史 10:00] 甲: 早", CacheBreakpoint: true},
			{Role: llm.RoleUser, Content: head},
			{Role: llm.RoleUser, Content: items, TurnOnly: true},
			{Role: llm.RoleUser, Content: current},
		}
	}
	first := turn(retrieved, "甲: 在吗")
	// 前缀里没有只用于本轮的那条（Runner 记录前缀时去掉了）。
	log := appendTestLog(t, first, "在的")
	for _, message := range log.prefix {
		if strings.Contains(message.Content, "甲不喝咖啡") {
			t.Fatalf("retrieved items of the reassembled turn must not stay in the prefix: %q", message.Content)
		}
	}
	continuation, decision := appendContextContinuation(log, turn(retrieved, "甲: 吃了吗"), 0)
	if continuation == nil {
		t.Fatalf("expected a continuation, reason = %q", decision.reason)
	}
	sawItems, sawHead := false, false
	for _, message := range continuation.Append {
		sawItems = sawItems || message.TurnOnly && message.Content == retrieved
		sawHead = sawHead || strings.Contains(message.Content, "当前发言者：甲")
	}
	if !sawItems || sawHead {
		t.Fatalf("retrieved items must be sent every turn, the unchanged head must not: items=%v head=%v", sawItems, sawHead)
	}
}
