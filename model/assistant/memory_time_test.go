// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
)

// 线上报的「你早上才吃了布洛芬」：三天前的情景记忆一被检索，last_verified_at 就刷成
// 现在，提示词里标的日期跟着变成今天。日期必须取来源消息时间，还要带上相对天数。
func TestStructuredMemoryLineShowsWhenItWasRecorded(t *testing.T) {
	now := time.Now()
	recorded := now.AddDate(0, 0, -3)
	line := formatStructuredMemoryLine(StructuredMemoryItem{
		Kind: MemoryKindEpisode, Topic: "用药", SubjectName: "小明",
		Content:         "小明早上吃了布洛芬",
		SourceEventTime: recorded,
		LastVerifiedAt:  now,
	})
	want := "记于 " + recorded.Format("2006-01-02") + "，3天前"
	if !strings.Contains(line, want) {
		t.Fatalf("line = %q, want it to contain %q", line, want)
	}
	if strings.Contains(line, now.Format("2006-01-02")) {
		t.Fatalf("retrieval time leaked into the label: %q", line)
	}
}

func TestRelativeDayLabelCountsCalendarDays(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 9, 25, 7, 0, 0, 0, loc)
	for _, tc := range []struct {
		then time.Time
		want string
	}{
		{time.Date(2026, 9, 25, 0, 30, 0, 0, loc), "今天"},
		// 不满 24 小时，但已经是前一个日历日。
		{time.Date(2026, 9, 24, 23, 0, 0, 0, loc), "昨天"},
		{time.Date(2026, 9, 22, 8, 0, 0, 0, loc), "3天前"},
	} {
		if got := relativeDayLabel(tc.then, now); got != tc.want {
			t.Fatalf("relativeDayLabel(%v) = %q, want %q", tc.then, got, tc.want)
		}
	}
}

func TestStructuredMemoryBlockExplainsRelativeTime(t *testing.T) {
	text, _, _ := formatStructuredMemoryContextWithTokenBudgetDetailed(UserMemoryProfile{UserID: "1"}, RelationshipPolicy{}, []StructuredMemoryItem{{
		Kind: MemoryKindEpisode, Topic: "用药", Content: "2026-09-22 上午吃了布洛芬", Confidence: 1, Importance: 1,
		SourceEventTime: time.Now().AddDate(0, 0, -3),
	}}, 4000)
	if !strings.Contains(text, structuredMemoryTimeRule) {
		t.Fatalf("memory block lacks the time rule: %q", text)
	}
}

// 私聊出站消息的 UserID 是对方。摘要和门控都得认出这是机器人自己的话，否则机器人
// 一句「你早上吃了布洛芬」会被整理成用户的自述，越记越真。
func TestBotOwnMessagesAreNotAttributedToTheUser(t *testing.T) {
	outbound := MessageEvent{
		Kind: EventKindPrivate, UserID: "10001", SenderName: "Diana", Outbound: true,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "你早上吃了布洛芬，别喝酒"}}},
	}
	line := compactContextEvent(outbound)
	if strings.Contains(line, "10001") || !strings.Contains(line, "机器人自己") {
		t.Fatalf("summary line = %q", line)
	}
	gate := memoryGateEventFromMessage(outbound, "你早上吃了布洛芬，别喝酒")
	if !gate.FromBot {
		t.Fatalf("gate event not marked as the bot's own: %#v", gate)
	}
	if !strings.Contains(memoryGateRulesPrompt, "from_bot=true") || !strings.Contains(memorySummaryRulesPrompt, "机器人自己") {
		t.Fatal("prompts must explain how bot lines are marked")
	}
}

// 门控和摘要看到的时间要和运行时钟同一时区，规则里要求把相对时间换算成日期。
func TestMemoryPromptsResolveRelativeTime(t *testing.T) {
	event := MessageEvent{Time: time.Date(2026, 9, 24, 23, 30, 0, 0, time.UTC).Unix()}
	if got := memoryEventTime(event).Location(); got != time.Local {
		t.Fatalf("memory event time zone = %v, want local", got)
	}
	for name, prompt := range map[string]string{"gate": memoryGateRulesPrompt, "summary": memorySummaryRulesPrompt} {
		if !strings.Contains(prompt, "相对") || !strings.Contains(prompt, "具体日期") {
			t.Fatalf("%s prompt does not ask to resolve relative time", name)
		}
	}
}

func TestSessionThreadNoteCarriesWhenItWasWritten(t *testing.T) {
	now := time.Now()
	written := now.AddDate(0, 0, -2)
	prefix := sessionThreadAsOf(StructuredMemoryItem{UpdatedAt: written}, now)
	if !strings.Contains(prefix, written.In(now.Location()).Format("2006-01-02 15:04")) || !strings.Contains(prefix, "2天前") {
		t.Fatalf("thread prefix = %q", prefix)
	}
	if got := sessionThreadAsOf(StructuredMemoryItem{}, now); got != "" {
		t.Fatalf("undated thread got prefix %q", got)
	}
}

func TestNotebookEventLinesAreDated(t *testing.T) {
	created := time.Date(2026, 9, 22, 8, 0, 0, 0, time.Local)
	event := formatNotebookLine(NotebookEntry{Kind: NotebookKindEvent, Term: "小明吃药", Meaning: "早上吃了布洛芬", CreatedAt: created})
	if !strings.Contains(event, "记于 2026-09-22") {
		t.Fatalf("event line = %q", event)
	}
	term := formatNotebookLine(NotebookEntry{Kind: NotebookKindTerm, Term: "yyds", Meaning: "永远的神", CreatedAt: created})
	if strings.Contains(term, "记于") {
		t.Fatalf("terms are timeless and should stay undated: %q", term)
	}
}
