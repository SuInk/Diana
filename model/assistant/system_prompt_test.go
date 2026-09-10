// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 默认人设只写「它是谁」，排版规则由独立的输出规范段落负责，不在人设里重复。
func TestDefaultSystemPromptCarriesNoFormattingRules(t *testing.T) {
	for _, unwanted := range []string{"Markdown", notificationSplitMarker, "OneBot v11 消息里"} {
		if strings.Contains(defaultSystemPrompt, unwanted) {
			t.Fatalf("default persona should not mention %q: %q", unwanted, defaultSystemPrompt)
		}
	}
	// 输出规范仍然会被注入，只是不再由人设承担。
	if !strings.Contains(defaultPromptPlaintextRules, "不要使用 Markdown 语法") {
		t.Fatalf("plaintext rules should still cover Markdown: %q", defaultPromptPlaintextRules)
	}
	// 分条归内置规则，不放在可编辑的文本框里：用户改一次、关一次，或者存着旧版
	// 默认值，分条就再也不会发生。
	if strings.Contains(defaultPromptPlaintextRules, notificationSplitMarker) {
		t.Fatalf("plaintext rules should not own the split marker: %q", defaultPromptPlaintextRules)
	}
	if !strings.Contains(replySegmentationRule, notificationSplitMarker) {
		t.Fatalf("built-in segmentation rule should teach the split marker: %q", replySegmentationRule)
	}
}

// 分条是投递机制，不能只在某一种表达风格里教：splitReply 只认 [diana-msg]，模型
// 不写标记就一定发成一整条。每种风格的提示词都必须带上这条规则。
func TestEveryReplyStyleTeachesTheSplitMarker(t *testing.T) {
	for _, style := range []ReplyStyle{
		ReplyStyleAssistant, ReplyStyleHuman, ReplyStyleGentle,
		ReplyStyleLively, ReplyStyleConcise, ReplyStyleCatgirl, ReplyStyle(""),
	} {
		prompt := style.prompt(true, personaVoice{})
		if !promptTeachesSegmentation(prompt) {
			t.Fatalf("style %q does not teach the split marker: %q", style, prompt)
		}
	}
}

func TestCustomPlaintextRulesAreKept(t *testing.T) {
	const custom = "只用短句，不要列点。"
	if got := (BotConfig{PromptPlaintextRulesText: custom}).WithDefaults().PromptPlaintextRulesText; got != custom {
		t.Fatalf("custom plaintext rules were overwritten: %q", got)
	}
}

// 用户发图（尤其是表情包）时，模型很容易把画面解说当成回复发出去。带图这轮
// 必须注入自然回应的规则，纯文字那轮不注入，免得白占提示词。
func TestSystemPromptTeachesNaturalImageReply(t *testing.T) {
	base := BotConfig{}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	relationship := RelationshipPolicyFor(UserMemoryProfile{}, base.OwnerID, "1")

	withImage := MessageEvent{Kind: EventKindPrivate, UserID: "1", Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.com/a.png"}}}}
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(withImage, nil, false, relationship, true, nil)
	if !strings.Contains(prompt, promptImageReply) {
		t.Fatalf("image turn is missing the natural-reply rule: %q", prompt)
	}

	textOnly := MessageEvent{Kind: EventKindPrivate, UserID: "1", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "在吗"}}}}
	if prompt := runtime.systemPromptWithRelationshipAndAgentTools(textOnly, nil, false, relationship, true, nil); strings.Contains(prompt, promptImageReply) {
		t.Fatalf("text-only turn should not carry the image rule: %q", prompt)
	}

	// 引用里的图片同样要触发：用户回一张图时也不该收到图解。
	quoted := MessageEvent{Kind: EventKindPrivate, UserID: "1", Quoted: &QuotedMessage{Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.com/b.png"}}}}}
	if prompt := runtime.systemPromptWithRelationshipAndAgentTools(quoted, nil, false, relationship, true, nil); !strings.Contains(prompt, promptImageReply) {
		t.Fatalf("quoted-image turn is missing the natural-reply rule: %q", prompt)
	}
}

// 主动接话那一轮不能再说「只有被提到才回复」：同一段提示词后面就写着「本次回复
// 是主动插话」，两句话正面打架，线上抓到的表现是模型一边接话一边解释自己不该说话。
func TestGroupScopeFollowsWhoStartedTheTurn(t *testing.T) {
	base := BotConfig{}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	relationship := RelationshipPolicyFor(UserMemoryProfile{}, base.OwnerID, "1")
	prompt := func(event MessageEvent) string {
		return runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false, relationship, true, nil)
	}

	triggered := prompt(MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "1", RawMessage: "Diana 在吗"})
	if !strings.Contains(triggered, promptGroupScope) || strings.Contains(triggered, promptGroupScopeProactive) {
		t.Fatalf("被点名那轮的场景说明不对：%q", triggered)
	}

	for name, event := range map[string]MessageEvent{
		"chatIn":    {Kind: EventKindGroup, GroupID: "g", UserID: "1", chatInReply: true},
		"proactive": {Kind: EventKindGroup, GroupID: "g", UserID: "1", proactiveReply: true},
		"both":      {Kind: EventKindGroup, GroupID: "g", UserID: "1", proactiveReply: true, chatInReply: true},
	} {
		got := prompt(event)
		if !strings.Contains(got, promptGroupScopeProactive) || strings.Contains(got, promptGroupScope) {
			t.Fatalf("%s 那轮仍在说「只有被提到才回复」：%q", name, got)
		}
	}

	// 私聊两串都不该出现：没有群，说了是白付 token。
	private := prompt(MessageEvent{Kind: EventKindPrivate, UserID: "1", chatInReply: true})
	if strings.Contains(private, promptGroupScope) || strings.Contains(private, promptGroupScopeProactive) {
		t.Fatalf("私聊注入了群聊场景说明：%q", private)
	}
}

// 头部要留得住前缀缓存：同一个模式下这行字必须逐字节固定，不随发言者或消息内容变。
func TestGroupScopeIsOneStableStringPerMode(t *testing.T) {
	for _, event := range []MessageEvent{
		{Kind: EventKindGroup, GroupID: "g", UserID: "1", RawMessage: "甲"},
		{Kind: EventKindGroup, GroupID: "g", UserID: "2", RawMessage: "乙"},
	} {
		if got := groupScopePrompt(event); got != promptGroupScope {
			t.Fatalf("触发回复的场景说明不稳定：%q", got)
		}
		event.chatInReply = true
		if got := groupScopePrompt(event); got != promptGroupScopeProactive {
			t.Fatalf("主动接话的场景说明不稳定：%q", got)
		}
	}
}
