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
	if !strings.Contains(defaultPromptPlaintextRules, notificationSplitMarker) {
		t.Fatalf("plaintext rules should teach the split marker: %q", defaultPromptPlaintextRules)
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
