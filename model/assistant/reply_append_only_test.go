// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// appendOnlyProvider 每轮第一步直接 send 一句，记下主回复请求（带工具定义的那些）。
type appendOnlyProvider struct {
	mu       sync.Mutex
	reply    string
	calls    int
	requests []llm.GenerateRequest
}

func (p *appendOnlyProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(req.Tools) == 0 {
		for _, message := range req.Messages {
			if strings.Contains(message.Content, "发送前审核器") {
				return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"send_confidence":0.99,"reason":"ok"}`}, nil
			}
		}
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: "{}"}, nil
	}
	p.requests = append(p.requests, req)
	p.calls++
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test",
		ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("send-%d", p.calls), Name: "send", Arguments: map[string]any{"text": p.reply}}}}, nil
}

// appendOnlyConversation 是一段合成的群聊：几个人轮流说话，机器人隔几条接一句。账号都是
// 合成的。
func appendOnlyConversation() (lines []struct{ user, name, text string }, replies map[int]string) {
	// 五位的老号码：全局别名表不收，靠按会话记住，前几轮留下的旧块里才不会变回真实号码。
	people := []struct{ user, name string }{{"10101", "Alice"}, {"10102", "Bob"}, {"10103", "Carol"}, {"10104", "Dave"}}
	texts := []string{
		"早上好", "今天谁去食堂", "我去，帮你带饭吗", "Diana 你吃了吗", "我刚看到一个新的开源项目",
		"链接发一下", "https://example.com/project", "Diana 这个项目是干嘛的", "看着像个笔记工具",
		"中午有人打球吗", "我下午有课", "Diana 推荐一首歌", "最近在听周杰伦", "晚上一起开黑吗",
		"Diana 你会玩吗", "我只会看", "那你当裁判", "Diana 帮我记一下明天交作业", "好的谢谢",
		"今天天气不错", "Diana 晚安", "晚安各位",
	}
	replies = map[int]string{}
	for index, text := range texts {
		person := people[index%len(people)]
		lines = append(lines, struct{ user, name, text string }{person.user, person.name, text})
		if strings.HasPrefix(text, "Diana") {
			replies[index] = "嗯，" + strings.TrimPrefix(text, "Diana ") + " 这个我想想"
		}
	}
	return lines, replies
}

// 主回复请求只追加：每一轮第一步的请求必须以上一轮第一步的请求原样开头（再接上上一轮
// 的发言），工具定义不变；第一步里没有只用于本轮、下一轮会撤掉的消息。会变的状态块
// （关系、对话对象、日期、工具权限……）变了才追加一版，旧的不撤。
//
// 同时按块统计每次请求各占多少、每一轮新发的（动态的）各占多少，打印成表；动态部分里
// 不是新消息的那些（状态块、附注等）设了上限。
func TestReplyRequestsAreAppendOnly(t *testing.T) {
	provider := &appendOnlyProvider{}
	runtime := NewRuntime(BotConfig{AgentEnabled: true, BotAccount: "42"}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	lines, replies := appendOnlyConversation()
	base := interimTestEvent().Time
	// 时钟跟着消息时间走：每条消息隔 40 秒，机器人在消息后 5 秒答完。用墙上时钟的话，
	// 整段对话几毫秒跑完，隔了几条的消息都会被当成「回答之前补的」连续消息。
	var clock atomic.Int64
	runtime.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	event := func(index int) MessageEvent {
		line := lines[index]
		return MessageEvent{Kind: EventKindGroup, SelfID: "42", GroupID: "20001", UserID: line.user, SenderName: line.name,
			MessageID: fmt.Sprintf("append-only-%d", index), Time: base + int64(index)*40, RawMessage: line.text,
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": line.text}}}, proactiveReply: true}
	}
	turns := 0
	for index := range lines {
		reply, ok := replies[index]
		if !ok {
			runtime.remember(event(index))
			continue
		}
		provider.mu.Lock()
		provider.reply = reply
		provider.mu.Unlock()
		current := event(index)
		clock.Store(current.Time + 5)
		if _, err := runtime.replyTo(t.Context(), current, current.RawMessage); err != nil {
			t.Fatalf("turn %d: %v", turns+1, err)
		}
		turns++
	}
	requests := provider.requests
	if len(requests) != turns || turns < 6 {
		t.Fatalf("reply requests = %d for %d turns", len(requests), turns)
	}

	key := func(message llm.Message) string {
		calls, _ := json.Marshal(message.ToolCalls)
		return string(message.Role) + "\x00" + appendContextText(message) + "\x00" + string(calls) + "\x00" + message.ToolCallID
	}
	headEnd := 1
	for headEnd < len(requests[0].Messages) && requests[0].Messages[headEnd].Role == llm.RoleSystem {
		headEnd++
	}
	type share struct{ total, dynamic int64 }
	shares := map[string]*share{}
	var total, dynamic, newChat int64
	toolTokens := llm.EstimateToolDefinitionsTokens(requests[0].Tools)
	for turn, request := range requests {
		for _, message := range request.Messages {
			if message.TurnOnly {
				t.Fatalf("turn %d: a first-step message is turn-only and would be dropped next turn: %.200q", turn+1, appendContextText(message))
			}
		}
		prefix := 0
		if turn > 0 {
			previous := requests[turn-1]
			before, _ := json.Marshal(previous.Tools)
			after, _ := json.Marshal(request.Tools)
			if string(before) != string(after) {
				t.Fatalf("turn %d: tool definitions changed", turn+1)
			}
			for prefix < len(previous.Messages) && prefix < len(request.Messages) && key(previous.Messages[prefix]) == key(request.Messages[prefix]) {
				prefix++
			}
			if prefix != len(previous.Messages) {
				t.Fatalf("turn %d: not append-only, the request departs from the previous one at message %d of %d:\n was %s: %.200q\n now %s: %.200q",
					turn+1, prefix, len(previous.Messages), previous.Messages[prefix].Role, appendContextText(previous.Messages[prefix]),
					request.Messages[prefix].Role, appendContextText(request.Messages[prefix]))
			}
		}
		block := shares[replyContextToolsName]
		if block == nil {
			block = &share{}
			shares[replyContextToolsName] = block
		}
		block.total += toolTokens
		total += toolTokens
		for index, message := range request.Messages {
			name := replyContextBlockName(index, message, headEnd)
			block := shares[name]
			if block == nil {
				block = &share{}
				shares[name] = block
			}
			tokens := llm.EstimateMessageTokens(message)
			block.total += tokens
			total += tokens
			if turn > 0 && index >= prefix {
				block.dynamic += tokens
				dynamic += tokens
				if name == replyContextHistoryName {
					newChat += tokens
				}
			}
		}
	}
	requestCount := int64(len(requests))
	continued := requestCount - 1
	var names []string
	for name := range shares {
		names = append(names, name)
	}
	sort.Slice(names, func(a, b int) bool { return shares[names[a]].total > shares[names[b]].total })
	var table strings.Builder
	fmt.Fprintf(&table, "%d 轮，平均每次请求 %d token；续接的 %d 轮平均每轮动态 %d token（占请求 %.1f%%），其中新消息 %d（占动态 %.0f%%）\n",
		requestCount, total/requestCount, continued, dynamic/continued, 100*float64(dynamic)/float64(total), newChat/continued, 100*float64(newChat)/float64(max(dynamic, 1)))
	fmt.Fprintf(&table, "%-34s %8s %7s %10s %8s\n", "块", "每次token", "占请求", "每轮动态", "占动态")
	for _, name := range names {
		block := shares[name]
		fmt.Fprintf(&table, "%-34s %8d %6.1f%% %10d %7.1f%%\n", name, block.total/requestCount, 100*float64(block.total)/float64(total), block.dynamic/continued, 100*float64(block.dynamic)/float64(max(dynamic, 1)))
	}
	t.Log("\n" + table.String())
	// 设置页的请求构成按最后一轮第一步实际发出的请求分块：合计和模型收到的那份逐 token
	// 一致，头部在前，只用于本轮的段标成 turn_only。
	breakdown, ok, err := runtime.ReplyContextBreakdownFor("", "group:20001")
	if err != nil || !ok || len(breakdown.Segments) < 4 || breakdown.Segments[0].Name != replyContextToolsName {
		t.Fatalf("breakdown = %+v, ok = %v, err = %v", breakdown, ok, err)
	}
	seenTail := false
	for _, segment := range breakdown.Segments {
		if segment.Kind != "prefix" {
			seenTail = true
		} else if seenTail {
			t.Fatalf("head segment %q after the tail: %+v", segment.Name, breakdown.Segments)
		}
	}
	last := requests[len(requests)-1]
	lastTokens := llm.EstimateToolDefinitionsTokens(last.Tools)
	for _, message := range last.Messages {
		lastTokens += llm.EstimateMessageTokens(message)
	}
	if breakdown.TotalTokens != lastTokens || breakdown.CompactionThreshold <= 0 {
		t.Fatalf("breakdown total %d, last request %d, threshold %d", breakdown.TotalTokens, lastTokens, breakdown.CompactionThreshold)
	}
	// 演示模式的请求构成（frontend/src/demo/demo.ts）照抄这一行，换了组装要重新抄。
	segmentsJSON, _ := json.Marshal(breakdown.Segments)
	t.Logf("请求构成：%s", segmentsJSON)
	// 续接的轮次，沿用的部分就是上一轮实际请求：这一轮新发的只是一小截。
	if breakdown.NewTokens <= 0 || breakdown.NewTokens*5 > breakdown.TotalTokens {
		t.Fatalf("new tokens %d of %d", breakdown.NewTokens, breakdown.TotalTokens)
	}
	// 对账：下一轮实际请求要以这一轮的原样开头，改了一条就记下不一致，设置页报错。
	log := runtime.appendContexts.get("\x00group:20001")
	next := log.request
	next.Messages = append(slices.Clone(log.request.Messages), llm.Message{Role: llm.RoleUser, Content: "新消息"})
	probe := &appendContextLog{}
	if probe.noteReplyRequest(next, llm.Usage{}, true, log); probe.requestMismatch != "" || probe.requestReused == 0 {
		t.Fatalf("clean continuation flagged: %q reused %d", probe.requestMismatch, probe.requestReused)
	}
	next.Messages[2].Content += "（被改过）"
	if probe.noteReplyRequest(next, llm.Usage{}, true, log); probe.requestMismatch == "" {
		t.Fatal("a changed message in a continued request should be flagged")
	}
	log.requestMismatch = probe.requestMismatch
	if _, _, err := runtime.ReplyContextOverview(""); err == nil {
		t.Fatal("overview should surface the mismatch")
	}
	log.requestMismatch = ""
	if sessions := runtime.ReplyContextSessions(""); len(sessions) != 1 || sessions[0].GroupID != "20001" {
		t.Fatalf("sessions = %+v", sessions)
	}
	// 动态部分里不是新消息的（状态块、附注、日期……）每轮平均不超过这个数。
	const nonChatDynamicBudget = 100
	if extra := (dynamic - newChat) / continued; extra > nonChatDynamicBudget {
		t.Fatalf("each continued turn sends %d tokens beyond the new chat, budget %d:\n%s", extra, nonChatDynamicBudget, table.String())
	}
}

// 关掉「只追加续接」后每轮重新组装：上一轮的当前消息改写成历史行，第二轮请求不再以
// 第一轮请求原样开头。
func TestReplyAppendOnlySwitchOff(t *testing.T) {
	provider := &appendOnlyProvider{reply: "嗯"}
	off := false
	runtime := NewRuntime(BotConfig{AgentEnabled: true, BotAccount: "42", ReplyAppendOnly: &off}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	base := interimTestEvent().Time
	for index, text := range []string{"Diana 早", "Diana 吃了吗"} {
		event := MessageEvent{Kind: EventKindGroup, SelfID: "42", GroupID: "20001", UserID: "10101", SenderName: "Alice",
			MessageID: fmt.Sprintf("switch-%d", index), Time: base + int64(index)*600, RawMessage: text,
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}, proactiveReply: true}
		if _, err := runtime.replyTo(t.Context(), event, text); err != nil {
			t.Fatal(err)
		}
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d", len(provider.requests))
	}
	first, second := provider.requests[0].Messages, provider.requests[1].Messages
	current := appendContextText(first[len(first)-1])
	for _, message := range second {
		if appendContextText(message) == current {
			t.Fatalf("second request still carries the first turn's current message verbatim: %.120q", current)
		}
	}
	// 记录照样记下，占比条还看得到。
	if _, ok, err := runtime.ReplyContextBreakdownFor("", "group:20001"); !ok || err != nil {
		t.Fatal("no breakdown with append-only off")
	}
}

// 关掉「固定说明进头部」后系统提示照旧按发言者分头部和尾部。
func TestReplyStableHeadSwitchOff(t *testing.T) {
	head := agent.NewToolRegistry()
	if headRegistryForPrompt(BotConfig{}, true, head) != head {
		t.Fatal("stable head should default on")
	}
	off := false
	if headRegistryForPrompt(BotConfig{ReplyStableHead: &off}, true, head) != nil {
		t.Fatal("stable head switch ignored")
	}
}

func TestReplyContextSessionEvent(t *testing.T) {
	for session, want := range map[string]MessageEvent{
		"group:20001":    {Kind: EventKindGroup, GroupID: "20001"},
		"tg:group:-1004": {Kind: EventKindGroup, GroupID: "-1004", ContextNamespace: "tg"},
		"private:10101":  {Kind: EventKindPrivate, UserID: "10101"},
	} {
		got := replyContextSessionEvent("p", session)
		if got.Kind != want.Kind || got.GroupID != want.GroupID || got.UserID != want.UserID || got.ContextNamespace != want.ContextNamespace || got.ProfileID != "p" {
			t.Fatalf("%s: %+v", session, got)
		}
	}
}
