// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestResponseModePresetsAndLegacyCustomSettings(t *testing.T) {
	tests := []struct {
		mode    ResponseMode
		enabled bool
		level   ChatInLevel
	}{
		{ResponseModeQuiet, false, ChatInLevelOff},
		{ResponseModeStandard, true, ChatInLevelLow},
		{ResponseModeActive, true, ChatInLevelHigh},
	}
	for _, test := range tests {
		cfg := BotConfig{ResponseMode: test.mode}.WithDefaults()
		if boolValue(cfg.ChatInEnabled, true) != test.enabled || cfg.ChatInLevel != test.level {
			t.Fatalf("mode %q produced enabled=%v level=%q", test.mode, boolValue(cfg.ChatInEnabled, true), cfg.ChatInLevel)
		}
	}

	legacy := BotConfig{ChatInEnabled: boolPointer(true), ChatInLevel: ChatInLevelMax, NaturalInterjectionEnabled: boolPointer(true)}.WithDefaults()
	if legacy.ResponseMode != ResponseModeCustom || legacy.ChatInLevel != ChatInLevelMax || !boolValue(legacy.NaturalInterjectionEnabled, false) {
		t.Fatalf("legacy settings were not preserved: %#v", legacy)
	}
}

func TestReplyStylePromptIsSpecificAndBounded(t *testing.T) {
	for _, style := range []ReplyStyle{ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleGroupmate, ReplyStyleCatgirl} {
		prompt := style.prompt()
		if prompt == "" || !strings.Contains(prompt, "默认表达风格") {
			t.Fatalf("style %q prompt = %q", style, prompt)
		}
	}
}

func TestGroupBehaviorPresetOverridesAndInherits(t *testing.T) {
	base := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleAssistant}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"active":  {GroupID: "active", ResponseMode: ResponseModeActive, ReplyStyle: ReplyStyleGentle},
		"inherit": {GroupID: "inherit"},
	}})

	active := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "active"})
	if active.ResponseMode != ResponseModeActive || active.ChatInLevel != ChatInLevelHigh || active.ReplyStyle != ReplyStyleGentle {
		t.Fatalf("active group config = %#v", active)
	}
	inherit := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "inherit"})
	if inherit.ResponseMode != ResponseModeStandard || inherit.ChatInLevel != ChatInLevelLow || inherit.ReplyStyle != ReplyStyleAssistant {
		t.Fatalf("inherited group config = %#v", inherit)
	}
	if prompt := runtime.systemPrompt(MessageEvent{Kind: EventKindGroup, GroupID: "active", UserID: "1"}, nil); !strings.Contains(prompt, "默认表达风格为温柔") {
		t.Fatalf("system prompt missing group style: %q", prompt)
	}
}

func TestBehaviorPresetsSurviveConfigPayloadRoundTrip(t *testing.T) {
	original := DefaultBotConfig()
	original.ResponseMode = ResponseModeActive
	original.ReplyStyle = ReplyStyleLively
	payload := PayloadFromConfig(original)
	if payload.ResponseMode != ResponseModeActive || payload.ReplyStyle != ReplyStyleLively {
		t.Fatalf("payload lost presets: %#v", payload)
	}
	restored := ConfigFromPayload(payload, BotConfig{})
	if restored.ResponseMode != ResponseModeActive || restored.ChatInLevel != ChatInLevelHigh || restored.ReplyStyle != ReplyStyleLively {
		t.Fatalf("round trip config = %#v", restored)
	}
}

func TestReplyStyleNormalizedHandlesGroupmateAndUnknown(t *testing.T) {
	for _, raw := range []string{"groupmate", "Groupmate", " groupmate "} {
		if got := ReplyStyle(raw).Normalized(); got != ReplyStyleGroupmate {
			t.Fatalf("Normalized(%q) = %q", raw, got)
		}
	}
	for _, raw := range []string{"", "  ", "unknown-style"} {
		if got := ReplyStyle(raw).Normalized(); got != ReplyStyleAssistant {
			t.Fatalf("Normalized(%q) = %q", raw, got)
		}
	}
}

func TestReplyStyleClosingAnchorIsAlwaysPresent(t *testing.T) {
	for _, style := range []ReplyStyle{ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleGroupmate} {
		if anchor := strings.TrimSpace(style.closingAnchor()); anchor == "" {
			t.Fatalf("style %q has an empty closing anchor", style)
		}
	}
}

func TestGroupmateReplyStylePromptCarriesExamples(t *testing.T) {
	prompt := ReplyStyleGroupmate.prompt()
	if !strings.Contains(prompt, "示例") || !strings.Contains(prompt, "用户：") {
		t.Fatalf("groupmate prompt is missing examples: %q", prompt)
	}
}

func TestSystemPromptEndsWithReplyStyleClosingAnchor(t *testing.T) {
	base := BotConfig{ReplyStyle: ReplyStyleGroupmate}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1"}
	relationship := RelationshipPolicyFor(UserMemoryProfile{}, base.OwnerID, event.UserID)
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(event, nil, true, relationship, true, nil)
	anchor := ReplyStyleGroupmate.closingAnchor()
	if !strings.HasSuffix(prompt, anchor) {
		t.Fatalf("system prompt does not end with the closing anchor: %q", prompt)
	}
}

func TestUserFacingPersonaCarriesStylePromptAndClosingAnchor(t *testing.T) {
	base := BotConfig{BotAccount: "42", ReplyStyle: ReplyStyleGroupmate}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "9"}

	messages := runtime.withUserFacingPersona(event, []llm.Message{{Role: llm.RoleUser, Content: "在吗"}})
	if len(messages) != 2 || messages[0].Role != llm.RoleSystem {
		t.Fatalf("persona was not prepended: %#v", messages)
	}
	persona := messages[0].Content
	for _, want := range []string{ReplyStyleGroupmate.prompt(), ReplyStyleGroupmate.closingAnchor()} {
		if !strings.Contains(persona, want) {
			t.Fatalf("persona missing %q: %q", want, persona)
		}
	}
	// 已经带过人设的消息列表不应该再插一遍。
	if again := runtime.withUserFacingPersona(event, messages); len(again) != len(messages) {
		t.Fatalf("persona was injected twice: %#v", again)
	}
}

func TestReplyStyleGroupmateDropsReplyReferenceAndMention(t *testing.T) {
	// 每条群回复都带引用和 @ 是最硬的机器人痕迹，prompt 管不到，得由风格关掉。
	cfg := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleGroupmate}.WithDefaults()
	if replyReferenceMode(cfg) != ReplyDecorationOff || mentionUserMode(cfg) != ReplyDecorationOff {
		t.Fatalf("groupmate style kept the bot-looking delivery flags: %#v", cfg)
	}

	assistant := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleAssistant}.WithDefaults()
	if replyReferenceMode(assistant) != ReplyDecorationOn || mentionUserMode(assistant) != ReplyDecorationOn {
		t.Fatalf("assistant style should keep default delivery: %#v", assistant)
	}
}

func TestReplyStyleGroupmateRespectsExplicitDeliverySettings(t *testing.T) {
	// 用户手动打开过就尊重用户，preset 只负责填未设置的项。
	cfg := BotConfig{
		ResponseMode:          ResponseModeStandard,
		ReplyStyle:            ReplyStyleGroupmate,
		ReplyReferenceEnabled: boolPointer(true),
		MentionUserEnabled:    boolPointer(true),
	}.WithDefaults()
	if replyReferenceMode(cfg) != ReplyDecorationOn || mentionUserMode(cfg) != ReplyDecorationOn {
		t.Fatalf("explicit delivery settings were overwritten: %#v", cfg)
	}
}

func TestReplyStyleGroupmateUsesChatSizedDelivery(t *testing.T) {
	// 900 字一条、300ms 连发是机器人特征，群友风格要压到聊天体量和打字节奏。
	cfg := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleGroupmate}.WithDefaults()
	if cfg.DirectReplyChunkSize != groupmateReplyChunkSize || cfg.SendChunkIntervalMS != groupmateSendChunkIntervalM {
		t.Fatalf("delivery = %d/%d", cfg.DirectReplyChunkSize, cfg.SendChunkIntervalMS)
	}

	// 比策略更克制的设置保留，更铺张的被压回来。
	tighter := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleGroupmate, DirectReplyChunkSize: 80, SendChunkIntervalMS: 2000}.WithDefaults()
	if tighter.DirectReplyChunkSize != 80 || tighter.SendChunkIntervalMS != 2000 {
		t.Fatalf("tighter settings were overwritten: %#v", tighter)
	}

	assistant := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleAssistant}.WithDefaults()
	if assistant.DirectReplyChunkSize != 900 || assistant.SendChunkIntervalMS != 300 {
		t.Fatalf("assistant delivery changed: %#v", assistant)
	}
}

func TestReplyStyleGroupmateNeverUsesForwardCard(t *testing.T) {
	// 合并转发是机器人专属控件，真人不会这么发言。
	if ReplyStyleGroupmate.allowsForwardReply() {
		t.Fatal("groupmate style must not fold replies into a forward card")
	}
	for _, style := range []ReplyStyle{ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise} {
		if !style.allowsForwardReply() {
			t.Fatalf("style %q unexpectedly lost forward replies", style)
		}
	}
}

func TestReplyStyleTypingDelayOnlyForGroupmate(t *testing.T) {
	if got := ReplyStyleAssistant.typingDelay("随便一句话"); got != 0 {
		t.Fatalf("assistant typingDelay = %v, want 0", got)
	}
	if got := ReplyStyleGroupmate.typingDelay("   "); got != 0 {
		t.Fatalf("blank text typingDelay = %v, want 0", got)
	}
	short := ReplyStyleGroupmate.typingDelay("在的")
	long := ReplyStyleGroupmate.typingDelay(strings.Repeat("字", 40))
	if short <= 0 || long <= short {
		t.Fatalf("typing delay should grow with length: short=%v long=%v", short, long)
	}
	if capped := ReplyStyleGroupmate.typingDelay(strings.Repeat("字", 10000)); capped != groupmateTypingMaxDelay {
		t.Fatalf("typing delay = %v, want capped at %v", capped, groupmateTypingMaxDelay)
	}
}

func TestReplyStyleGroupmateAppliesPerGroup(t *testing.T) {
	base := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleAssistant}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"casual": {GroupID: "casual", ReplyStyle: ReplyStyleGroupmate},
	}})
	casual := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "casual"})
	if replyReferenceMode(casual) != ReplyDecorationOff || mentionUserMode(casual) != ReplyDecorationOff {
		t.Fatalf("group-level groupmate style did not drop delivery flags: %#v", casual)
	}
}

func sharedPromptPrefixRatio(left, right string) float64 {
	runesLeft, runesRight := []rune(left), []rune(right)
	shared := 0
	for shared < len(runesLeft) && shared < len(runesRight) && runesLeft[shared] == runesRight[shared] {
		shared++
	}
	return float64(shared) / float64(len(runesLeft))
}

// promptLineDiff 统计两段提示词按行拆开后的多重集差异：left 独有的行计 +1，right
// 独有的行计 -1，两边都有的行相互抵消为 0。
func promptLineDiff(left, right string) map[string]int {
	diff := map[string]int{}
	for _, line := range strings.Split(left, "\n") {
		diff[line]++
	}
	for _, line := range strings.Split(right, "\n") {
		diff[line]--
	}
	return diff
}

func TestSystemPromptKeepsPerMessageContentOutOfTheCacheablePrefix(t *testing.T) {
	base := BotConfig{GroupTriggers: []string{"Diana", "diana"}, OwnerID: "9001"}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	prompt := func(userID, sender, text string) string {
		event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: userID, SenderName: sender, RawMessage: text}
		return runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false,
			RelationshipPolicyFor(UserMemoryProfile{}, base.OwnerID, userID), true, nil)
	}

	alice := prompt("1", "Alice", "diana 在吗")
	aliceAgain := prompt("1", "Alice", "随便说点什么")
	bob := prompt("2", "Bob", "随便说点什么")
	owner := prompt("9001", "SuInk", "随便说点什么")

	// 群里两个普通成员之间，只有尾部的发送者与命中别名不同；前面几千 token 的规则
	// 必须逐字相同，否则供应商的前缀缓存对这段最长的 system 提示词永远失效。
	if ratio := sharedPromptPrefixRatio(alice, bob); ratio < 0.85 {
		t.Fatalf("two ordinary members share only %.0f%% of the prompt prefix", ratio*100)
	}
	// 主人多出若干工具规则，但这些差异同样必须落在尾部而不是中段。
	if ratio := sharedPromptPrefixRatio(alice, owner); ratio < 0.75 {
		t.Fatalf("owner and member diverge after only %.0f%% of the prompt", ratio*100)
	}
	// 同一发言者连续发言时，权限档位段落和昵称也稳定，前缀应一路延伸到命中别名之前。
	if ratio := sharedPromptPrefixRatio(alice, aliceAgain); ratio < 0.95 {
		t.Fatalf("consecutive messages from the same member share only %.0f%% of the prompt prefix", ratio*100)
	}

	// 两个同档位成员的提示词按行拆开比较，差异只允许出现在发送者昵称和命中别名上，
	// 以此保证挪动位置没有丢段、也没有串档。
	for line, count := range promptLineDiff(alice, bob) {
		if count != 0 && !strings.Contains(line, "Alice") && !strings.Contains(line, "Bob") &&
			!strings.Contains(line, "当前消息命中的配置别名") {
			t.Fatalf("unexpected line-level difference between two same-tier members: %q", line)
		}
	}

	// 内容不能因为挪位置而丢失或串档。
	if !strings.Contains(alice, "Alice") || !strings.Contains(bob, "Bob") {
		t.Fatal("sender name missing from the prompt")
	}
	if !strings.Contains(alice, "当前消息命中的配置别名") {
		t.Fatal("matched alias notice missing from the prompt")
	}
	if strings.Contains(bob, "当前消息命中的配置别名") {
		t.Fatal("a message without an alias hit must not carry the notice")
	}
	if !strings.Contains(owner, "diana.llm_config") {
		t.Fatal("owner-only tool rules missing after the move")
	}
	if strings.Contains(alice, "diana.llm_config") {
		t.Fatal("owner-only tool rules leaked to an ordinary member")
	}
	for _, item := range []string{alice, bob, owner} {
		if !strings.Contains(item, "权限规则：") {
			t.Fatal("relationship permission context missing from the prompt")
		}
		if !strings.HasSuffix(item, base.ReplyStyle.closingAnchor()) {
			t.Fatal("closing anchor must stay last")
		}
	}
}

// 猫娘风格的难点全在「别做过头」：模型一听见猫娘就往颜文字、动作描写和「本喵」
// 上冲，还会拿卖萌顶替正事。这条守住那几条刹车，以及它没有豁免全局输出规则。
func TestCatgirlReplyStyleKeepsBrakesAndGlobalRules(t *testing.T) {
	for _, raw := range []string{"catgirl", "Catgirl", " catgirl "} {
		if got := ReplyStyle(raw).Normalized(); got != ReplyStyleCatgirl {
			t.Fatalf("Normalized(%q) = %q", raw, got)
		}
	}
	prompt := ReplyStyleCatgirl.prompt()
	for _, want := range []string{"本喵", "动作描写", "只对主人称", "可爱只体现在语气上"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("catgirl prompt is missing the %q brake: %q", want, prompt)
		}
	}
	// 拒绝是模型最容易掉出人设的地方：一要拒绝就切回客服腔。而「以人设为由要求
	// 越界」正是这一档会招来的攻击面，次序必须写死：规则优先，人设让位。
	for _, want := range []string{"拒绝时也留在人设里", "规则优先，人设让位"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("catgirl prompt is missing %q: %q", want, prompt)
		}
	}
	// 语气靠具体的词和示例教，抽象形容词教不会——群友那档也是这么写的。
	if !strings.Contains(prompt, "示例——") || !strings.Contains(prompt, "用户：") {
		t.Fatalf("catgirl prompt has no worked examples: %q", prompt)
	}
	// 「每句加喵」和「句末不打句号」必须一起说：只说前者，模型会写成「……了喵。」，
	// 句号跟在后面，读起来还是助理腔。示例里也一个句号都不能有，否则规则和样例
	// 自相矛盾，模型跟样例走。
	if !strings.Contains(prompt, "每句话结尾都加「喵」") || !strings.Contains(prompt, "句末不要「。」") {
		t.Fatalf("catgirl prompt does not pin the sentence ending: %q", prompt)
	}
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, "你：") && strings.Contains(line, "。") {
			t.Fatalf("catgirl example still ends sentences with a full stop: %q", line)
		}
	}
	// 表达风格换人不代表输出规范换人：emoji、空行、篇幅三条对所有风格生效。
	for _, want := range []string{replyEmojiRule, replyBlankLineRule, replyProportionRule} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("catgirl prompt dropped a global rule: %q", prompt)
		}
	}
}

// 只有群友风格会改投递方式（分条长度、连发间隔、引用装饰）。猫娘只是换个语气，
// 不该顺手把这些也改了。
func TestCatgirlReplyStyleDoesNotChangeDelivery(t *testing.T) {
	cfg := BotConfig{ReplyStyle: ReplyStyleCatgirl}
	before := cfg
	ReplyStyleCatgirl.apply(&cfg)
	if cfg.DirectReplyChunkSize != before.DirectReplyChunkSize ||
		cfg.SendChunkIntervalMS != before.SendChunkIntervalMS ||
		cfg.ReplyReferenceMode != before.ReplyReferenceMode ||
		cfg.MentionUserMode != before.MentionUserMode {
		t.Fatalf("catgirl style changed delivery settings: %#v", cfg)
	}
}
