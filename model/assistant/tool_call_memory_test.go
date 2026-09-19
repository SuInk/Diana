// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 线上 09-15：机器人 20:06 调过 web_search，20:21 被问「搜索了吗」却回答「没搜」。
func TestToolCallMemoryTellsNextTurnWhatWasSearched(t *testing.T) {
	now := time.Date(2026, 9, 15, 20, 6, 0, 0, time.Local)
	runtime := &Runtime{}
	runtime.now = func() time.Time { return now }
	event := MessageEvent{Kind: EventKindGroup, GroupID: "1103673848"}
	runtime.rememberToolCalls(event, []agent.Step{
		{Tool: "web_search.search", Input: map[string]any{"query": "iCloud 由云上贵州运营 条款与条件 更新"}},
		{Tool: "browser_render", Input: map[string]any{"url": "https://www.apple.com/legal/"}, Error: "timeout"},
		{Tool: "diana.chat_history", Skipped: true},
		{Tool: "tools.load", Input: map[string]any{"names": []string{"unexecuted"}}},
		{Tool: "tools.execute", Input: map[string]any{"name": "internal"}},
		{Tool: "agent.finalize", Input: map[string]any{"content": "回复正文"}},
	})

	now = now.Add(15 * time.Minute)
	context := runtime.toolCallContext(event)
	for _, want := range []string{"20:06 web_search.search「iCloud 由云上贵州运营 条款与条件 更新」", "browser_render「https://www.apple.com/legal/」（调用失败）", "不要因为对方质疑就改口认错"} {
		if !strings.Contains(context, want) {
			t.Fatalf("context missing %q:\n%s", want, context)
		}
	}
	for _, unwanted := range []string{"diana.chat_history", "agent.finalize", "tools.load", "tools.execute", "unexecuted", "回复正文"} {
		if strings.Contains(context, unwanted) {
			t.Fatalf("context should not include %q:\n%s", unwanted, context)
		}
	}
	// 最近的调用排在前面。
	if strings.Index(context, "browser_render") > strings.Index(context, "web_search.search") {
		t.Fatalf("newest call should come first:\n%s", context)
	}
	if other := runtime.toolCallContext(MessageEvent{Kind: EventKindGroup, GroupID: "42"}); other != "" {
		t.Fatalf("tool calls leaked into another session: %q", other)
	}
}

func TestToolCallMemoryExpiresAndCaps(t *testing.T) {
	now := time.Date(2026, 9, 15, 20, 0, 0, 0, time.Local)
	runtime := &Runtime{}
	runtime.now = func() time.Time { return now }
	event := MessageEvent{Kind: EventKindPrivate, UserID: "u1"}
	for index := 0; index < recentToolCallLimit+3; index++ {
		runtime.rememberToolCalls(event, []agent.Step{{Tool: "web_search.search", Input: map[string]any{"query": strings.Repeat("长", 120)}}})
	}
	context := runtime.toolCallContext(event)
	if got := strings.Count(context, "\n- "); got != recentToolCallLimit {
		t.Fatalf("kept %d records, want %d:\n%s", got, recentToolCallLimit, context)
	}
	if strings.Contains(context, strings.Repeat("长", 81)) {
		t.Fatal("query summary was not truncated")
	}
	now = now.Add(recentToolCallTTL + time.Minute)
	if context := runtime.toolCallContext(event); context != "" {
		t.Fatalf("expired records still injected: %q", context)
	}
}
