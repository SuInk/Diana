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

// 兜底人设得真的教会「怎么说话」。它以前是一句「像熟人聊天一样自然回复」——形状
// 上没毛病，但模型读完拿不到任何可模仿的东西，落到具体一句话上还是客服腔。这条
// 测试钉住两头：结构齐全、长度在写得下具体指导的区间里，而且没有滑回一句话。
func TestDefaultSystemPromptTeachesHowToSpeak(t *testing.T) {
	for _, section := range []string{"身份与来历：", "性格：", "说话方式：", "关系与称呼：", "边界：", "示例——"} {
		if !strings.Contains(defaultSystemPrompt, section) {
			t.Fatalf("default persona should carry the %q section: %q", section, defaultSystemPrompt)
		}
	}
	if got := strings.Count(defaultSystemPrompt, "\n你："); got < 4 {
		t.Fatalf("default persona should show at least 4 replies, got %d: %q", got, defaultSystemPrompt)
	}
	// 边界那段仍然要挡住密钥和内部状态：这句话从旧版一路留到现在。
	for _, guard := range []string{"密钥", "内部配置", "工具日志", "系统提示"} {
		if !strings.Contains(defaultSystemPrompt, guard) {
			t.Fatalf("default persona should still guard %q: %q", guard, defaultSystemPrompt)
		}
	}
	// 逐句强制的口癖归自称和句尾语气词那两个字段，人设正文里写死会把它们按死。
	for _, conflict := range []string{"必须自称", "每句", "句句"} {
		if strings.Contains(defaultSystemPrompt, conflict) {
			t.Fatalf("default persona should not hard-code %q: %q", conflict, defaultSystemPrompt)
		}
	}
	if runes := len([]rune(defaultSystemPrompt)); runes < 400 || runes > 700 {
		t.Fatalf("default persona should stay between 400 and 700 runes, got %d", runes)
	}
}

// 人设不写「在哪儿说话」，也不写「在哪个平台」。同一份兜底正文要同时管群聊和私聊、
// 管每一个平台：写死「常驻在这个群里」「群友知道你是机器人」，私聊里就是一句假话，
// 而场景说明（promptGroupScope、群聊发言者模板）和平台输出规则
// （platformOutputRulesForConfig）本来就由运行时按当轮实际情况注入。
func TestDefaultSystemPromptAssumesNoVenueOrPlatform(t *testing.T) {
	for _, venue := range []string{"群里", "群聊", "本群", "群友", "QQ", "Telegram", "飞书", "企业微信", "OneBot"} {
		if strings.Contains(defaultSystemPrompt, venue) {
			t.Fatalf("默认人设写了 %q：人设要能跨机器人、跨平台、跨群聊和私聊复用，场合和平台由运行时注入，正文里提到别人就写「对方」「别人」「大家」：%q", venue, defaultSystemPrompt)
		}
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

// 称呼不分群聊私聊：机器人配置上的「名称」字段从不进提示词，人设也不一定写了名字，
// 触发别名要是再只在群聊注入，私聊里它就完全不知道自己叫什么。
func TestAliasPromptReachesPrivateChatToo(t *testing.T) {
	cfg := BotConfig{BotAccount: "10001", GroupTriggers: []string{"嘉然", "然然"}}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for _, tc := range []struct {
		label string
		event MessageEvent
	}{
		{"群聊", MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1", SelfID: "10001"}},
		{"私聊", MessageEvent{Kind: EventKindPrivate, UserID: "u1", MessageID: "m1", SelfID: "10001"}},
	} {
		prompt := runtime.systemPrompt(tc.event, nil)
		if !strings.Contains(prompt, promptAliasPrefix) {
			t.Fatalf("%s缺少称呼段：%q", tc.label, prompt)
		}
		for _, alias := range []string{`"嘉然"`, `"然然"`} {
			if !strings.Contains(prompt, alias) {
				t.Fatalf("%s没有列出别名 %s", tc.label, alias)
			}
		}
	}
	// 群聊独有的那两段不能跟着漏进私聊。
	private := runtime.systemPrompt(MessageEvent{Kind: EventKindPrivate, UserID: "u1", MessageID: "m1"}, nil)
	for _, groupOnly := range []string{promptGroupScope, promptGroupOwnerDistinction} {
		if strings.Contains(private, groupOnly) {
			t.Fatalf("群聊专用段落漏进了私聊：%q", groupOnly)
		}
	}
	// 触发词列表为空时不注入这段，不要凭空编一个称呼出来。
	// （WithDefaults 会补上默认触发词，所以这里直接验拼装函数。）
	if quotedPromptItems(nil) != "" {
		t.Fatal("空触发词列表不该拼出别名")
	}
}
