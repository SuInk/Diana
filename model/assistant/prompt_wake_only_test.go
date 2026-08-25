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

// at 段带昵称时渲染成「@Diana（3129583166）」。按账号做字符串替换只会挖掉号码，
// 留下「@Diana（）」的残渣，文本照样不空、唤醒提示词照样不触发——所以要摘段。
func TestCleanInputDropsNamedBotMention(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "3129583166"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "3129583166", GroupID: "g", UserID: "10001", ToMe: true,
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "3129583166", "name": "Diana"}},
		},
	}
	got := runtime.cleanInput(event, "")
	if got != runtime.Config().PromptWakeOnlyText {
		t.Fatalf("带昵称的纯 @ 没有走唤醒提示词：%q", got)
	}
	for _, residue := range []string{"Diana", "（", "@"} {
		if strings.Contains(got, residue) && !strings.Contains(runtime.Config().PromptWakeOnlyText, residue) {
			t.Fatalf("留下了残渣 %q：%q", residue, got)
		}
	}
}

// 别人的 @ 带昵称时要完整保留：那是「在说谁」的线索。
func TestCleanInputKeepsNamedMentionsOfOthers(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "3129583166"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "3129583166", GroupID: "g", UserID: "10001", ToMe: true,
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "3129583166", "name": "Diana"}},
			{Type: "text", Data: map[string]string{"text": " 看看 "}},
			{Type: "at", Data: map[string]string{"qq": "10002", "name": "老王"}},
			{Type: "text", Data: map[string]string{"text": " 那条"}},
		},
	}
	got := runtime.cleanInput(event, "")
	if !strings.Contains(got, "老王") || !strings.Contains(got, "10002") {
		t.Fatalf("别人的 @ 被删了：%q", got)
	}
	if strings.Contains(got, "3129583166") {
		t.Fatalf("机器人自己的 @ 没删干净：%q", got)
	}
	if !strings.Contains(got, "看看") || !strings.Contains(got, "那条") {
		t.Fatalf("正文被破坏：%q", got)
	}
}

// @ 的注解要和正文对得上：@ 自己的已经从正文摘掉，就不能再说「不要忽略正文里的 @」；
// @ 了别人的还在正文里，那句提醒要留着。
func TestMentionAnnotationMatchesWhatIsInTheText(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "3129583166"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	self := MessageEvent{
		Kind: EventKindGroup, SelfID: "3129583166", GroupID: "g", UserID: "10001", ToMe: true,
		Segments: []MessageSegment{{Type: "at", Data: map[string]string{"qq": "3129583166", "name": "Diana"}}},
	}
	got := currentPromptText(self, runtime.cleanInput(self, ""))
	if !strings.Contains(got, "这条消息 @ 了你") {
		t.Fatalf("没有告诉模型它被 @ 了：%q", got)
	}
	if strings.Contains(got, "@ 是当前消息的一部分") {
		t.Fatalf("正文里已经没有 @ 了，不该再说「不要忽略」：%q", got)
	}

	withOther := self
	withOther.Segments = append(append([]MessageSegment{}, self.Segments...),
		MessageSegment{Type: "text", Data: map[string]string{"text": " 看看 "}},
		MessageSegment{Type: "at", Data: map[string]string{"qq": "10002", "name": "老王"}})
	got = currentPromptText(withOther, runtime.cleanInput(withOther, ""))
	if !strings.Contains(got, "@ 是当前消息的一部分") {
		t.Fatalf("@ 了别人时应当保留原来的提醒：%q", got)
	}
	if !strings.Contains(got, "老王") {
		t.Fatalf("别人的 @ 应当留在正文里：%q", got)
	}
}

func TestMentionsSomeoneElse(t *testing.T) {
	bot := "42"
	onlyBot := MessageEvent{SelfID: bot, Segments: []MessageSegment{{Type: "at", Data: map[string]string{"qq": bot}}}}
	if mentionsSomeoneElse(onlyBot) {
		t.Fatal("只 @ 了自己不该算 @ 了别人")
	}
	withOther := MessageEvent{SelfID: bot, Segments: []MessageSegment{
		{Type: "at", Data: map[string]string{"qq": bot}},
		{Type: "at", Data: map[string]string{"qq": "10002"}},
	}}
	if !mentionsSomeoneElse(withOther) {
		t.Fatal("@ 了别人没有被认出来")
	}
	// 取不到自己的账号时按「@ 了别人」处理：多提醒一次无害，漏掉会让模型忽略回复对象。
	unknownSelf := MessageEvent{Segments: []MessageSegment{{Type: "at", Data: map[string]string{"qq": bot}}}}
	if !mentionsSomeoneElse(unknownSelf) {
		t.Fatal("不知道自己是谁时应当保守处理")
	}
}
