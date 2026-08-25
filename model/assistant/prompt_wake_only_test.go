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

// 纯 @ 的消息也要把原话交给模型：唤醒指引是注解，不是正文的替身。
func TestBareMentionKeepsOriginalTextAndAppendsWakeGuidance(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	bare := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "g", UserID: "10001", ToMe: true,
		Segments: []MessageSegment{{Type: "at", Data: map[string]string{"qq": "42", "name": "Diana"}}},
	}
	clean := runtime.cleanInput(bare, "")
	if !strings.Contains(clean, "Diana") || !strings.Contains(clean, "@") {
		t.Fatalf("原话没有留下来：%q", clean)
	}
	if clean == runtime.Config().PromptWakeOnlyText {
		t.Fatalf("正文被唤醒提示词顶替了：%q", clean)
	}
	prompt := currentPromptTextWithSemanticContext(bare, clean, semanticReferenceContext{}, promptAnnotation{BotID: "42", WakeGuidance: runtime.Config().PromptWakeOnlyText})
	if !strings.Contains(prompt, runtime.Config().PromptWakeOnlyText) {
		t.Fatalf("唤醒指引没有作为注解附上：%q", prompt)
	}
	if !strings.Contains(prompt, "Diana") {
		t.Fatalf("注解之后原话丢了：%q", prompt)
	}

	spoken := bare
	spoken.Segments = append(append([]MessageSegment{}, bare.Segments...), MessageSegment{Type: "text", Data: map[string]string{"text": "在干嘛"}})
	got := currentPromptTextWithSemanticContext(spoken, runtime.cleanInput(spoken, ""), semanticReferenceContext{}, promptAnnotation{BotID: "42", WakeGuidance: runtime.Config().PromptWakeOnlyText})
	if !strings.Contains(got, "在干嘛") {
		t.Fatalf("带内容的消息正文丢了：%q", got)
	}
	if strings.Contains(got, runtime.Config().PromptWakeOnlyText) {
		t.Fatalf("说了话的消息不该附唤醒指引：%q", got)
	}
}

// 正文原样保留：机器人自己的 @ 和别人的 @ 都在。
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
	if !strings.Contains(got, "42") {
		t.Fatalf("机器人自己的 @ 被剥掉了：%q", got)
	}
	if !strings.Contains(got, "10002") {
		t.Fatalf("别人的 @ 被误删了：%q", got)
	}
	if !strings.Contains(got, "帮我看看") || !strings.Contains(got, "说的那件事") {
		t.Fatalf("正文被破坏：%q", got)
	}
}

// 判定用的那份副本要真的剥干净：at 段带昵称时渲染成「@Diana（3129583166）」，
// 按账号做字符串替换只会挖掉号码，留下「@Diana（）」，纯 @ 就判不出来了。
func TestBotMentionStrippedTextDropsNamedBotMention(t *testing.T) {
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "3129583166", GroupID: "g", UserID: "10001", ToMe: true,
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "3129583166", "name": "Diana"}},
		},
	}
	if got := botMentionStrippedText(event, "", "3129583166"); got != "" {
		t.Fatalf("带昵称的 @ 没有摘干净：%q", got)
	}
	if !bareWakeMention(event, "@Diana（3129583166）", "3129583166", nil) {
		t.Fatal("带昵称的纯 @ 没有被认成一次唤醒")
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
	if !strings.Contains(got, "Diana") || !strings.Contains(got, "3129583166") {
		t.Fatalf("机器人自己的 @ 也该留在原文里：%q", got)
	}
	if !strings.Contains(got, "看看") || !strings.Contains(got, "那条") {
		t.Fatalf("正文被破坏：%q", got)
	}
}

// @ 的注解要和正文对得上：正文里的 @ 指向自己时说「这是在叫你」，
// 还 @ 了别人时保留「别忽略 @」的提醒。
func TestMentionAnnotationMatchesWhatIsInTheText(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "3129583166"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	self := MessageEvent{
		Kind: EventKindGroup, SelfID: "3129583166", GroupID: "g", UserID: "10001", ToMe: true,
		Segments: []MessageSegment{{Type: "at", Data: map[string]string{"qq": "3129583166", "name": "Diana"}}},
	}
	got := currentPromptText(self, runtime.cleanInput(self, ""))
	if !strings.Contains(got, "那个 @ 指的就是你") {
		t.Fatalf("没有告诉模型它被 @ 了：%q", got)
	}
	if strings.Contains(got, "@ 是当前消息的一部分") {
		t.Fatalf("只 @ 了自己时不该用「别忽略别人」的那句：%q", got)
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

// 只喊一声名字和只 @ 一下是同一件事，都会招来「在呢」，所以都要附唤醒指引。
func TestBareTriggerWordAlsoGetsWakeGuidance(t *testing.T) {
	cfg := BotConfig{BotAccount: "42", GroupTriggers: []string{"Diana"}}.WithDefaults()
	annotation := promptAnnotation{BotID: "42", WakeGuidance: cfg.PromptWakeOnlyText, TriggerWords: cfg.GroupTriggers}
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "g", UserID: "10001", ToMe: true,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana"}}},
	}
	got := currentPromptTextWithSemanticContext(event, "Diana", semanticReferenceContext{}, annotation)
	if !strings.Contains(got, "Diana") {
		t.Fatalf("原话丢了：%q", got)
	}
	if !strings.Contains(got, cfg.PromptWakeOnlyText) {
		t.Fatalf("只喊名字没有附唤醒指引：%q", got)
	}

	// 名字后面接了话就不是「只叫了一声」了。
	spoken := event
	spoken.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana 帮我看看这个"}}}
	got = currentPromptTextWithSemanticContext(spoken, "Diana 帮我看看这个", semanticReferenceContext{}, annotation)
	if strings.Contains(got, cfg.PromptWakeOnlyText) {
		t.Fatalf("说了话的消息不该附唤醒指引：%q", got)
	}
}

// 唤醒指引已经把「这是一次有效唤醒」说清楚了，不要再叠一句泛泛的重复。
func TestWakeGuidanceReplacesGenericMentionOnlyNotice(t *testing.T) {
	cfg := BotConfig{BotAccount: "42"}.WithDefaults()
	annotation := promptAnnotation{BotID: "42", WakeGuidance: cfg.PromptWakeOnlyText}
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "g", UserID: "10001", ToMe: true,
		Segments: []MessageSegment{{Type: "at", Data: map[string]string{"qq": "42"}}},
	}
	got := currentPromptTextWithSemanticContext(event, "@42", semanticReferenceContext{}, annotation)
	if strings.Contains(got, "主要由 @ 或引用组成") {
		t.Fatalf("唤醒指引之外还叠了泛泛的那句：%q", got)
	}

	// 只有引用、没有 @ 的消息走不到唤醒指引，那句仍然要留着。
	quoted := event
	quoted.Segments = []MessageSegment{{Type: "reply", Data: map[string]string{"id": "abc"}}}
	got = currentPromptTextWithSemanticContext(quoted, "[diana-reply:abc]", semanticReferenceContext{}, annotation)
	if !strings.Contains(got, "主要由 @ 或引用组成") {
		t.Fatalf("纯引用的消息丢了提示：%q", got)
	}
}
