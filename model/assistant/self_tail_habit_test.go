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
	return MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "bot", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}}
}

func peerText(text string) MessageEvent {
	event := botText(text)
	event.UserID = "100"
	return event
}

func tailHistory(own, peers []string) []MessageEvent {
	var history []MessageEvent
	for _, text := range peers {
		history = append(history, peerText(text))
	}
	for _, text := range own {
		history = append(history, botText(text))
	}
	return history
}

func repeatText(text string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = text
	}
	return out
}

var ownParenHeavy = []string{"一（逃", "二", "三（", "四（逃", "五", "六（目移", "七"}

// 自己的尾巴比真人多得多才提醒，提示里摆出两边的数字。
func TestSelfTailHabitPromptComparesWithPeers(t *testing.T) {
	cfg := BotConfig{BotAccount: "bot"}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "100", SelfID: "bot"}
	peers := append(repeatText("今天吃什么好呢", 19), "笑死（")
	for i, item := range tailHistory(ownParenHeavy, peers) {
		item.MessageID = fmt.Sprint(i)
		runtime.remember(item)
	}
	got := runtime.selfTailHabitPrompt(event, cfg)
	if !strings.Contains(got, "大家最近 20 条消息里只有 1 条") || !strings.Contains(got, "你最近 7 条里却有 4 条用 「（逃」「（」「（目移」") {
		t.Fatalf("prompt = %q", got)
	}
}

// 真人自己也爱这么收尾，那是群风格，不算口癖。
func TestSelfTailHabitFollowsPeerStyle(t *testing.T) {
	peers := append(repeatText("笑死（逃", 8), repeatText("今天吃什么好呢", 12)...)
	if _, ok := selfTailHabit(tailHistory(ownParenHeavy, peers), "bot", nil); ok {
		t.Fatal("tails the humans also use must not be flagged")
	}
}

// 偶尔一两次是调味；真人样本不够也不下结论。
func TestSelfTailHabitNeedsHabitAndPeers(t *testing.T) {
	peers := repeatText("今天吃什么好呢", 20)
	if _, ok := selfTailHabit(tailHistory([]string{"一（逃", "二", "三", "四", "五", "六", "七（"}, peers), "bot", nil); ok {
		t.Fatal("occasional tails must not be flagged")
	}
	if _, ok := selfTailHabit(tailHistory(ownParenHeavy, peers[:5]), "bot", nil); ok {
		t.Fatal("too few human messages must not produce a verdict")
	}
}

// 群里别的机器人不算真人参照：它满屏「（逃」不能把 Diana 的口癖洗成群风格。
func TestSelfTailHabitSkipsOtherBots(t *testing.T) {
	history := tailHistory(ownParenHeavy, repeatText("今天吃什么好呢", 20))
	for i := 0; i < 10; i++ {
		other := peerText("笑死（逃")
		other.UserID = "200"
		history = append([]MessageEvent{other}, history...)
	}
	if _, ok := selfTailHabit(history, "bot", nil); ok {
		t.Fatal("unmarked: the other bot counts as a peer and its tails cover Diana's habit")
	}
	if _, ok := selfTailHabit(history, "bot", []string{"200"}); !ok {
		t.Fatal("a marked bot must not count as a human reference")
	}
}
