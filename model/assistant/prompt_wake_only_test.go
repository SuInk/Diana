// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 只 @ 不说话时，回一句「我在」是接线员不是熟人。提示词得把这个失败模式点名，
// 光说「请自然回应」太空，模型会退回报到。
func TestWakeOnlyPromptForbidsPresenceAnnouncements(t *testing.T) {
	cfg := BotConfig{}.WithDefaults()
	prompt := cfg.PromptWakeOnlyText
	if strings.TrimSpace(prompt) == "" {
		t.Fatal("唤醒提示词不该为空")
	}
	// 点名要避免的说法。
	for _, phrase := range []string{"我在", "在呢", "怎么了"} {
		if !strings.Contains(prompt, phrase) {
			t.Fatalf("提示词没有点名要避免的说法 %q：%s", phrase, prompt)
		}
	}
	// 也要给出替代做法，否则只是禁止而没有方向。
	for _, guidance := range []string{"前面几条", "接着说"} {
		if !strings.Contains(prompt, guidance) {
			t.Fatalf("提示词没有给出替代做法 %q：%s", guidance, prompt)
		}
	}
	// 这段文字会作为「用户说了什么」交给模型，不能让它把规则复述出去。
	if !strings.Contains(prompt, "不要复述") {
		t.Fatalf("提示词没有禁止复述自身：%s", prompt)
	}
}

// 纯 @ 的消息才走这条提示词；带了字的照常把原文交给模型。
func TestCleanInputUsesWakeOnlyPromptOnlyForBareMention(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	bare := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "g", UserID: "10001", ToMe: true,
		Segments: []MessageSegment{{Type: "at", Data: map[string]string{"qq": "42"}}},
	}
	if got := runtime.cleanInput(bare, ""); got != runtime.Config().PromptWakeOnlyText {
		t.Fatalf("纯 @ 没有走唤醒提示词：%q", got)
	}
	spoken := bare
	spoken.Segments = append(spoken.Segments, MessageSegment{Type: "text", Data: map[string]string{"text": "在干嘛"}})
	if got := runtime.cleanInput(spoken, ""); !strings.Contains(got, "在干嘛") {
		t.Fatalf("带内容的消息被换成了唤醒提示词：%q", got)
	}
}

// 机器人自己的 @ 是噪声，别人的 @ 是回复对象的线索，不能一起剥掉。
func TestCleanInputKeepsOtherPeopleMentions(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "g", UserID: "10001", ToMe: true,
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "42"}},
			{Type: "text", Data: map[string]string{"text": "帮我看看 "}},
			{Type: "at", Data: map[string]string{"qq": "10002"}},
			{Type: "text", Data: map[string]string{"text": " 说的那件事"}},
		},
	}
	got := runtime.cleanInput(event, "")
	if strings.Contains(got, "42") {
		t.Fatalf("机器人自己的 @ 没有剥掉：%q", got)
	}
	if !strings.Contains(got, "10002") {
		t.Fatalf("别人的 @ 被误删了：%q", got)
	}
	if !strings.Contains(got, "帮我看看") || !strings.Contains(got, "说的那件事") {
		t.Fatalf("正文被破坏：%q", got)
	}
}
