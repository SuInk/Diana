// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
	"testing"
)

func TestSelfTailKind(t *testing.T) {
	cases := map[string][2]string{
		"等能玩了必须搬好小板凳去围观一手（":        {"paren", "（"},
		"省着点霍霍（逃":                  {"paren", "（逃"},
		"纯粹是拿来当护身符的（目移）":           {"paren", "（目移"},
		"属于是把开会的精髓完全摸透了w":          {"w", "w"},
		"那必须的，关键时刻可靠谱了喵～":          {"meow", "喵"},
		"走开发者流程正常申请审核要温和得多":        {"", ""},
		"最常走的湖中道（梨园到磨山段）约 6 公里":    {"", ""},
		"装 git 后跑 git log -n 3 -w": {"", ""},
	}
	for text, want := range cases {
		kind, tail := selfTailKind(text)
		if kind != want[0] || tail != want[1] {
			t.Errorf("selfTailKind(%q) = %q %q, want %q %q", text, kind, tail, want[0], want[1])
		}
	}
}

func botText(text string) MessageEvent {
	return MessageEvent{Kind: EventKindGroup, UserID: "bot", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}}
}

// 只数自己的消息：群友满屏「（逃」不算机器人的口癖。
func TestSelfTailHabitCountsOnlyOwnMessages(t *testing.T) {
	var history []MessageEvent
	for i := 0; i < 10; i++ {
		history = append(history, MessageEvent{Kind: EventKindGroup, UserID: "100", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "笑死（逃"}}}})
		history = append(history, botText(fmt.Sprintf("第 %d 条正常回复", i)))
	}
	if _, _, _, ok := selfTailHabit(history, "bot"); ok {
		t.Fatal("human tails must not count as the bot's habit")
	}
}

func TestSelfTailHabitPromptNamesTails(t *testing.T) {
	cfg := BotConfig{BotAccount: "bot"}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "100", SelfID: "bot"}
	texts := []string{"一（逃", "二", "三（", "四（逃", "五", "六（目移", "七"}
	for i, text := range texts {
		item := botText(text)
		item.GroupID, item.MessageID = "g1", fmt.Sprint(i)
		runtime.remember(item)
	}
	got := runtime.selfTailHabitPrompt(event, cfg)
	if !strings.Contains(got, "最近 7 条消息里有 4 条") || !strings.Contains(got, "「（逃」「（」「（目移」") {
		t.Fatalf("prompt = %q", got)
	}
	// 只有一两次是调味，不提醒。
	runtime2 := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for i, text := range []string{"一（逃", "二", "三", "四", "五", "六", "七（"} {
		item := botText(text)
		item.GroupID, item.MessageID = "g1", fmt.Sprint(i)
		runtime2.remember(item)
	}
	if got := runtime2.selfTailHabitPrompt(event, cfg); got != "" {
		t.Fatalf("occasional tails produced %q", got)
	}
}
