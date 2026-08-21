// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func TestMatchedAliasesInTextSmartKeepsDirectCalls(t *testing.T) {
	aliases := []string{"diana"}
	cases := []string{
		"diana",
		"diana 帮我看看这个报错",
		"diana，在吗",
		"diana你怎么看",
		"diana说说今天的安排",
		"在吗 diana",
		"这题怎么做啊 diana？",
		"diana 查一下明天天气",
	}
	for _, text := range cases {
		if got := matchedAliasesInText(text, aliases, AliasTriggerSmart); len(got) == 0 {
			t.Errorf("smart 模式漏掉了直接呼叫：%q", text)
		}
	}
}

func TestMatchedAliasesInTextSmartDropsDiscussion(t *testing.T) {
	aliases := []string{"diana"}
	cases := []string{
		"diana 刚才那句话好怪",
		"diana的回复越来越慢了",
		"diana说的那个方案我觉得不行",
		"跟diana聊天挺有意思的",
		"diana又开始复读了",
		"「diana」这个名字挺好听",
	}
	for _, text := range cases {
		if got := matchedAliasesInText(text, aliases, AliasTriggerSmart); len(got) != 0 {
			t.Errorf("smart 模式没能识别出这是在谈论它：%q（命中 %v）", text, got)
		}
	}
}

func TestMatchedAliasesInTextLooseKeepsLegacyBehaviour(t *testing.T) {
	aliases := []string{"diana"}
	// 谈论类消息在 loose 档下必须仍然触发，这是升级前的行为。
	for _, text := range []string{"diana 刚才那句话好怪", "diana的回复越来越慢了"} {
		if got := matchedAliasesInText(text, aliases, AliasTriggerLoose); len(got) == 0 {
			t.Errorf("loose 模式应保持原行为并触发：%q", text)
		}
	}
}

func TestMatchedAliasesInTextStrictRequiresVocativePosition(t *testing.T) {
	aliases := []string{"diana"}
	if got := matchedAliasesInText("diana 帮我看看", aliases, AliasTriggerStrict); len(got) == 0 {
		t.Fatal("strict 模式应接受句首呼语")
	}
	if got := matchedAliasesInText("这题 diana 应该会做吧我猜", aliases, AliasTriggerStrict); len(got) != 0 {
		t.Fatalf("strict 模式不应接受夹在句中的称呼，命中 %v", got)
	}
}

func TestMatchedAliasesInTextRespectsASCIIWordBoundary(t *testing.T) {
	aliases := []string{"diana"}
	for _, text := range []string{"dianabc 是什么", "看看 https://example.com/diana2 这个", "adiana"} {
		if got := matchedAliasesInText(text, aliases, AliasTriggerSmart); len(got) != 0 {
			t.Errorf("ASCII 称呼不应粘在更长的词里命中：%q（命中 %v）", text, got)
		}
	}
	if got := matchedAliasesInText("diana-酱 在吗", aliases, AliasTriggerSmart); len(got) == 0 {
		t.Error("连字符不是单词字符，应该正常命中")
	}
}

func TestMatchedAliasesInTextNonASCIIAliasIgnoresWordBoundary(t *testing.T) {
	if got := matchedAliasesInText("小满帮我看看", []string{"小满"}, AliasTriggerSmart); len(got) == 0 {
		t.Fatal("中文称呼没有词边界概念，应正常命中")
	}
	if got := matchedAliasesInText("小满刚才说的那个", []string{"小满"}, AliasTriggerSmart); len(got) != 0 {
		t.Fatalf("中文称呼同样要能识别谈论语境，命中 %v", got)
	}
}

func TestNormalizeAliasTriggerModeFallsBackToSmart(t *testing.T) {
	for _, mode := range []AliasTriggerMode{"", "  ", "nonsense"} {
		if got := normalizeAliasTriggerMode(mode); got != AliasTriggerSmart {
			t.Errorf("normalizeAliasTriggerMode(%q) = %q，期望 smart", mode, got)
		}
	}
	if got := normalizeAliasTriggerMode(" LOOSE "); got != AliasTriggerLoose {
		t.Errorf("normalizeAliasTriggerMode 未忽略大小写和空白，得到 %q", got)
	}
}

// groupAliasEvent 构造一条群内纯文本消息，用于走完整的触发判定链路。
func groupAliasEvent(text string) MessageEvent {
	return MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123",
		UserID:     "10001",
		RawMessage: text,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

func TestShouldHandleChatTriggerSkipsAliasDiscussionByDefault(t *testing.T) {
	runtime := NewRuntime(BotConfig{GroupTriggers: []string{"Diana"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)

	called := groupAliasEvent("Diana 帮我看看这个报错")
	if !runtime.shouldHandleChatTrigger(called, PlainText(called.Segments)) {
		t.Fatal("默认档位漏掉了直接呼叫")
	}

	discussed := groupAliasEvent("Diana 刚才那句话好怪")
	if runtime.shouldHandleChatTrigger(discussed, PlainText(discussed.Segments)) {
		t.Fatal("默认档位把谈论机器人的消息当成了显式呼叫")
	}
}

func TestShouldHandleChatTriggerLooseModeKeepsLegacyBehaviour(t *testing.T) {
	runtime := NewRuntime(
		BotConfig{GroupTriggers: []string{"Diana"}, GroupTriggerMode: AliasTriggerLoose},
		nilChannel{}, NewPluginManager(), nil, nil, nil, nil,
	)
	discussed := groupAliasEvent("Diana 刚才那句话好怪")
	if !runtime.shouldHandleChatTrigger(discussed, PlainText(discussed.Segments)) {
		t.Fatal("宽松档应保持升级前的行为")
	}
}

func TestReplyDecisionReasonMatchesTriggerGate(t *testing.T) {
	runtime := NewRuntime(BotConfig{GroupTriggers: []string{"Diana"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	// 判定理由和闸门必须用同一套匹配，否则后台会显示一条并不成立的触发原因。
	discussed := groupAliasEvent("Diana 刚才那句话好怪")
	reason := runtime.replyDecisionReason(discussed, PlainText(discussed.Segments), "replied")
	if reason == "群消息命中了触发称呼“Diana”" {
		t.Fatalf("判定理由仍然报告了已被闸门丢弃的称呼命中：%q", reason)
	}
}
