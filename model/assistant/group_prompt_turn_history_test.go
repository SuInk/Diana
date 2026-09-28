// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func turnHistoryTestEvent(id, user, text string, at int64) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "30001", UserID: user, SenderName: "u" + user,
		MessageID: id, Time: at, RawMessage: text,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

func historyLineContents(messages []llm.Message) []string {
	var out []string
	for _, message := range messages {
		if strings.HasPrefix(message.Content, "[历史 ") {
			out = append(out, message.Content)
		}
	}
	return out
}

func assertHistoryPrefix(t *testing.T, before, after []string) {
	t.Helper()
	if len(after) < len(before) {
		t.Fatalf("后一轮历史比前一轮短：\n前 %q\n后 %q", before, after)
	}
	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("第 %d 行被改写，前缀断了：\n前 %q\n后 %q", index, before, after)
		}
	}
}

// 两条几乎同时到的消息各起一轮：前一轮把后到的那条当历史写进了日志，轮到后到
// 那条自己时不能再把它从日志中间抽掉——以前抽掉之后同一位置换成了前一条，两轮的
// 前缀从这一行起全部作废，下一轮它又被追加回末尾。
func TestStableGroupHistoryKeepsJournaledCurrentMessageInPlace(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	x := turnHistoryTestEvent("m-x", "10001", "早先的一句", 100)
	a := turnHistoryTestEvent("m-a", "10002", "哦这里的梗是奶油蛋糕", 110)
	b := turnHistoryTestEvent("m-b", "10002", "我懂了", 112)
	project := func(current MessageEvent, history ...MessageEvent) ([]string, []MessageEvent) {
		stable, _, kept := r.stableGroupHistoryKeepingTurn(context.Background(), current, r.effectiveConfigForEvent(current), history, true, nil)
		return historyLineContents(stable), kept
	}

	// a 这一轮：a 是当前消息，b 已经到了，作为历史进日志。
	first, kept := project(a, x, b)
	if len(first) != 2 || !strings.Contains(first[1], "我懂了") || len(kept) != 0 {
		t.Fatalf("a 这一轮的历史 = %q，留下 = %v", first, kept)
	}
	// b 这一轮：b 已经在日志里，原样留下；a 不再是当前消息，按晚到追加到末尾。
	second, kept := project(b, x, a)
	assertHistoryPrefix(t, first, second)
	if len(second) != 3 || !strings.Contains(second[2], "奶油蛋糕") {
		t.Fatalf("b 这一轮的历史 = %q", second)
	}
	if len(kept) != 1 || kept[0].MessageID != "m-b" {
		t.Fatalf("留下的应当只有当前消息 b：%v", kept)
	}
	// 再下一轮：两条都是普通历史，顺序不再变。
	c := turnHistoryTestEvent("m-c", "10003", "那嘉然还记得烤肉吗", 120)
	third, kept := project(c, x, a, b)
	assertHistoryPrefix(t, second, third)
	if len(third) != 3 || len(kept) != 0 {
		t.Fatalf("c 这一轮的历史 = %q，留下 = %v", third, kept)
	}
}

// 还没进过日志的当前消息照旧不进：它在尾部单独成块，下一轮再按晚到追加，
// 行首带的还是它自己的原始时间。
func TestStableGroupHistoryStillOmitsFreshCurrentMessage(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	x := turnHistoryTestEvent("m-x", "10001", "早先的一句", 100)
	a := turnHistoryTestEvent("m-a", "10002", "刚发的这句", 110)
	stable, _, kept := r.stableGroupHistoryKeepingTurn(context.Background(), a, r.effectiveConfigForEvent(a), []MessageEvent{x}, true, nil)
	lines := historyLineContents(stable)
	if len(lines) != 1 || strings.Contains(strings.Join(lines, "\n"), "刚发的这句") || len(kept) != 0 {
		t.Fatalf("没进过日志的当前消息被写进了历史：%q，留下 = %v", lines, kept)
	}
	b := turnHistoryTestEvent("m-b", "10003", "下一句", 120)
	stable, _ = r.stableGroupHistory(context.Background(), b, r.effectiveConfigForEvent(b), []MessageEvent{x, a}, true, nil)
	next := historyLineContents(stable)
	assertHistoryPrefix(t, lines, next)
	wantPrefix := historyLinePrefix(a)
	if len(next) != 2 || !strings.HasPrefix(next[1], wantPrefix) {
		t.Fatalf("晚到的一条应追加在末尾并带原始时间 %q：%q", wantPrefix, next)
	}
}

// 同轮补充同理：已经在日志里的留在原位，不在的照旧只在尾部补充块里出现。
func TestStableGroupHistoryKeepsJournaledTurnSupplements(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	x := turnHistoryTestEvent("m-x", "10001", "早先的一句", 100)
	s1 := turnHistoryTestEvent("m-s1", "10002", "补一句", 110)
	s2 := turnHistoryTestEvent("m-s2", "10002", "再补一句", 111)
	current := turnHistoryTestEvent("m-cur", "10002", "问题在这", 112)
	other := turnHistoryTestEvent("m-o", "10004", "别人插话", 105)
	cfg := r.effectiveConfigForEvent(current)
	first, _ := r.stableGroupHistory(context.Background(), other, cfg, []MessageEvent{x, s1}, true, nil)
	before := historyLineContents(first)
	stable, _, kept := r.stableGroupHistoryKeepingTurn(context.Background(), current, cfg, []MessageEvent{x, other, s1, s2}, true, map[string]bool{"m-s1": true, "m-s2": true})
	after := historyLineContents(stable)
	assertHistoryPrefix(t, before, after)
	joined := strings.Join(after, "\n")
	if strings.Contains(joined, "再补一句") {
		t.Fatalf("没进过日志的同轮补充被写进了历史：%q", after)
	}
	if len(kept) != 1 || kept[0].MessageID != "m-s1" {
		t.Fatalf("留下的应当只有已在日志里的 s1：%v", kept)
	}
}

// 整轮走一遍：当前消息已经在前面的历史里时，尾部要说明「上面那条就是它」，
// 别让模型以为对方又发了一遍，也别把排在它后面的历史当成要回的话。
func TestReplyNotesCurrentMessageAlreadyInStableHistory(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":false}`,
		"好的。",
		`{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":false}`,
		"明白。",
	}}
	runtime := NewRuntime(BotConfig{AgentEnabled: false, RequestTimeout: time.Minute}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	x := turnHistoryTestEvent("m-x", "10001", "早先的一句", 100)
	a := turnHistoryTestEvent("m-a", "10002", "哦这里的梗是奶油蛋糕", 110)
	b := turnHistoryTestEvent("m-b", "10002", "我懂了", 112)
	for _, event := range []MessageEvent{x, a, b} {
		runtime.remember(event)
	}
	note := promptNoteTurnInHistorySpec.Default
	finalRequest := func(current string) llm.GenerateRequest {
		t.Helper()
		provider.mu.Lock()
		defer provider.mu.Unlock()
		for index := len(provider.requests) - 1; index >= 0; index-- {
			last := provider.requests[index].Messages[len(provider.requests[index].Messages)-1]
			if strings.Contains(last.Content, "【当前需要回复的消息】") && strings.Contains(last.Content, current) {
				return provider.requests[index]
			}
		}
		t.Fatalf("找不到回复 %q 的那次请求", current)
		return llm.GenerateRequest{}
	}

	if _, err := runtime.replyTo(context.Background(), a, a.RawMessage); err != nil {
		t.Fatal(err)
	}
	first := finalRequest("奶油蛋糕")
	if last := first.Messages[len(first.Messages)-1].Content; strings.Contains(last, note) {
		t.Fatalf("当前消息不在历史里时不该带注解：%s", last)
	}
	if _, err := runtime.replyTo(context.Background(), b, b.RawMessage); err != nil {
		t.Fatal(err)
	}
	second := finalRequest("我懂了")
	if last := second.Messages[len(second.Messages)-1].Content; !strings.Contains(last, note) {
		t.Fatalf("当前消息已在历史里却没有注解：%s", last)
	}
	assertHistoryPrefix(t, historyLineContents(first.Messages), historyLineContents(second.Messages))
}
