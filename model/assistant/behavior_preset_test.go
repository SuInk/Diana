// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

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
		prompt := style.prompt(true, personaVoice{})
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
	prompt := ReplyStyleGroupmate.prompt(true, personaVoice{})
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
	for _, want := range []string{ReplyStyleGroupmate.prompt(true, personaVoice{}), ReplyStyleGroupmate.closingAnchor()} {
		if !strings.Contains(persona, want) {
			t.Fatalf("persona missing %q: %q", want, persona)
		}
	}
	// 已经带过人设的消息列表不应该再插一遍。
	if again := runtime.withUserFacingPersona(event, messages); len(again) != len(messages) {
		t.Fatalf("persona was injected twice: %#v", again)
	}
}

// 引用和 @ 只有配置一个来源，风格不插手。
//
// 风格曾经把这两项按成「从不」。错的不是值而是位置：它们在 WebUI 里有对应的
// 下拉框，风格再填一遍就有了两个来源；而 WithDefaults 会把填进去的值一起存库，
// 保存过一次配置之后，风格填的「从不」和用户亲手选的「从不」再也分不开——
// 「用户选过就尊重用户」名存实亡，之后改风格的默认值也到不了这些人手上。
func TestReplyDecorationModesAreNotStyleDriven(t *testing.T) {
	for _, style := range []ReplyStyle{ReplyStyleGroupmate, ReplyStyleAssistant, ReplyStyleCatgirl} {
		cfg := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: style}.WithDefaults()
		if replyReferenceMode(cfg) != ReplyDecorationAuto || mentionUserMode(cfg) != ReplyDecorationAuto {
			t.Fatalf("风格 %s 改动了装饰件默认值：引用=%s @=%s", style, replyReferenceMode(cfg), mentionUserMode(cfg))
		}
	}
	// 显式选过的值任何风格都不许动。
	explicit := BotConfig{
		ResponseMode:       ResponseModeStandard,
		ReplyStyle:         ReplyStyleGroupmate,
		ReplyReferenceMode: ReplyDecorationOn,
		MentionUserMode:    ReplyDecorationOff,
	}.WithDefaults()
	if replyReferenceMode(explicit) != ReplyDecorationOn || mentionUserMode(explicit) != ReplyDecorationOff {
		t.Fatalf("显式设置被风格改掉了：%#v", explicit)
	}
}

// 投递方式只由配置决定：填了就照填的来，没填才用默认值。风格不参与。
func TestDeliverySettingsAreConfigOnly(t *testing.T) {
	// 没填：所有风格拿到同一份聊天体量的默认值。
	for _, style := range []ReplyStyle{ReplyStyleGroupmate, ReplyStyleAssistant, ReplyStyleCatgirl} {
		cfg := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: style}.WithDefaults()
		if cfg.DirectReplyChunkSize != chatReplyChunkSize || cfg.SendChunkIntervalMS != chatSendChunkIntervalMS {
			t.Fatalf("风格 %s 的默认投递 = %d/%d，want %d/%d",
				style, cfg.DirectReplyChunkSize, cfg.SendChunkIntervalMS, chatReplyChunkSize, chatSendChunkIntervalMS)
		}
	}

	// 填了：两个方向都照填的来。以前更铺张的值会被群友风格压回去——那正是
	// 「WebUI 里改了不生效」的来源。
	for _, want := range []struct{ chunk, interval int }{{80, 2000}, {900, 300}} {
		cfg := BotConfig{
			ResponseMode:         ResponseModeStandard,
			ReplyStyle:           ReplyStyleGroupmate,
			DirectReplyChunkSize: want.chunk,
			SendChunkIntervalMS:  want.interval,
		}.WithDefaults()
		if cfg.DirectReplyChunkSize != want.chunk || cfg.SendChunkIntervalMS != want.interval {
			t.Fatalf("显式投递设置被改掉了：%d/%d，want %d/%d",
				cfg.DirectReplyChunkSize, cfg.SendChunkIntervalMS, want.chunk, want.interval)
		}
	}
}

// 卡片由阈值决定，跟风格无关。以前群友风格整条分支都被短路，「合并转发字数」
// 填了不生效，长回复照样一口气刷十几条。
func TestForwardCardAppliesToEveryStyle(t *testing.T) {
	long := strings.Repeat("字", 1200)
	chunks := []string{long}
	for _, style := range []ReplyStyle{
		ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively,
		ReplyStyleConcise, ReplyStyleGroupmate, ReplyStyleCatgirl,
	} {
		cfg := BotConfig{ReplyStyle: style}.WithDefaults()
		if !shouldUseForwardReply(long, chunks, cfg.ForwardReplyThreshold, cfg.ForwardReplyChunkThreshold) {
			t.Fatalf("风格 %q 下 1200 字没有触发合并转发", style)
		}
		short := strings.Repeat("字", 100)
		if shouldUseForwardReply(short, []string{short}, cfg.ForwardReplyThreshold, cfg.ForwardReplyChunkThreshold) {
			t.Fatalf("风格 %q 下 100 字不该触发合并转发", style)
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
	if casual.ReplyStyle.Normalized() != ReplyStyleGroupmate {
		t.Fatalf("group-level style did not take effect: %#v", casual)
	}
	// 风格按群生效的是措辞和打字节奏，投递配置不归它管——卡片阈值也一样，
	// 群级换成群友风格不会把它关掉。
	long := strings.Repeat("字", 1200)
	if !shouldUseForwardReply(long, []string{long}, casual.ForwardReplyThreshold, casual.ForwardReplyChunkThreshold) {
		t.Fatalf("群级群友风格把合并转发关掉了：%#v", casual)
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
	prompt := ReplyStyleCatgirl.prompt(true, personaVoice{})
	for _, want := range []string{"动作描写", "只对主人称", "可爱只体现在语气上"} {
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
	// 曾经教过「句尾一个孤零零的『（』」当语气词，现在不教了：发送前的审核器把
	// 「括号没闭合」当成截断特征，整条回复会被判成半截话拦下来。提示词和审核规则
	// 对着干，最后是用户少收到一条完整回复。
	if strings.Contains(prompt, "括号里不写任何内容") || strings.Contains(prompt, "喵（") {
		t.Fatalf("catgirl prompt 又教回了句末空括号：%q", prompt)
	}
	if !strings.Contains(prompt, "不要自己发明别的收尾符号") {
		t.Fatalf("catgirl prompt 没有钉住结尾写法：%q", prompt)
	}
	// 表达风格换人不代表输出规范换人：emoji、空行、篇幅三条对所有风格生效。
	for _, want := range []string{replyEmojiRule, replyBlankLineRule, replyProportionRule} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("catgirl prompt dropped a global rule: %q", prompt)
		}
	}
}

// 任何风格都不该动投递方式的四项配置——它们在 WebUI 里各有一个输入框，
// 风格再改一遍就有了两个来源。
func TestReplyStyleDoesNotChangeDeliveryConfig(t *testing.T) {
	for _, style := range []ReplyStyle{ReplyStyleCatgirl, ReplyStyleGroupmate, ReplyStyleLively, ReplyStyleConcise} {
		filled := BotConfig{
			ReplyStyle:           style,
			DirectReplyChunkSize: 777,
			SendChunkIntervalMS:  333,
			ReplyReferenceMode:   ReplyDecorationOff,
			MentionUserMode:      ReplyDecorationOn,
		}.WithDefaults()
		if filled.DirectReplyChunkSize != 777 || filled.SendChunkIntervalMS != 333 ||
			filled.ReplyReferenceMode != ReplyDecorationOff || filled.MentionUserMode != ReplyDecorationOn {
			t.Fatalf("风格 %s 改动了投递配置：%d/%d %s/%s", style,
				filled.DirectReplyChunkSize, filled.SendChunkIntervalMS, filled.ReplyReferenceMode, filled.MentionUserMode)
		}
	}
}

// 逗号分隔的候选清单要能解析，中英文逗号都认，并且去重限量。
func TestParsePersonaEndersAcceptsBothCommas(t *testing.T) {
	enders := parsePersonaEnders(" 喵, 喵~ ，喵？，，喵…… ,喵~ ")
	want := []string{"喵", "喵~", "喵？", "喵……"}
	if len(enders) != len(want) {
		t.Fatalf("enders = %v, want %v", enders, want)
	}
	for index, ender := range want {
		if enders[index] != ender {
			t.Fatalf("enders = %v, want %v", enders, want)
		}
	}

	raw := make([]string, 0, personaVoiceMaxEnders*2)
	for index := 0; index < personaVoiceMaxEnders*2; index++ {
		raw = append(raw, strconv.Itoa(index))
	}
	if got := parsePersonaEnders(strings.Join(raw, ",")); len(got) != personaVoiceMaxEnders {
		t.Fatalf("len = %d, want %d", len(got), personaVoiceMaxEnders)
	}

	// 填一整段人设进来的不算候选，直接丢掉。
	if got := parsePersonaEnders(strings.Repeat("喵", personaVoiceMaxRunes+1)); len(got) != 0 {
		t.Fatalf("overlong ender was accepted: %v", got)
	}
}

// 多个候选是让模型按语气挑，不是运行时随机——提示词必须把「按当下语气挑」说出来。
func TestPersonaVoicePromptAsksModelToPickByTone(t *testing.T) {
	voice := personaVoiceFrom("本喵", "喵,喵~,喵？")
	prompt := voice.prompt()
	if !strings.Contains(prompt, "「本喵」") {
		t.Fatalf("自称没写进提示词：%s", prompt)
	}
	for _, ender := range []string{"「喵」", "「喵~」", "「喵？」"} {
		if !strings.Contains(prompt, ender) {
			t.Fatalf("候选 %s 没写进提示词：%s", ender, prompt)
		}
	}
	if !strings.Contains(prompt, "按当下语气挑") {
		t.Fatalf("没说清楚按语气挑：%s", prompt)
	}
	// 和风格描述冲突时要说清楚以谁为准，否则模型会在两套说法之间犹豫。
	if !strings.Contains(prompt, "以这里为准") {
		t.Fatalf("没声明覆盖关系：%s", prompt)
	}

	// 只有一个候选就是固定句尾，不该再说「挑」。
	if single := personaVoiceFrom("", "喵").prompt(); strings.Contains(single, "按当下语气挑") {
		t.Fatalf("单个候选不该说挑：%s", single)
	}
}

// 两项都留空时一个字都不该加：老配置不受影响，风格自带的说法照旧。
func TestPersonaVoiceEmptyLeavesStylePromptUntouched(t *testing.T) {
	if got := (personaVoice{}).prompt(); got != "" {
		t.Fatalf("empty voice produced %q", got)
	}
	if ReplyStyleCatgirl.prompt(true, personaVoice{}) != ReplyStyleCatgirl.prompt(true, personaVoiceFrom("  ", " ")) {
		t.Fatal("空白字段应当和完全没填一样")
	}
}

// 猫娘那档自带的候选要和 WebUI 填进框里的一致，否则用户看到的和实际生效的对不上。
func TestDefaultPersonaVoiceForCatgirl(t *testing.T) {
	selfReference, enders := DefaultPersonaVoice(ReplyStyleCatgirl)
	if selfReference != "我" {
		t.Fatalf("self reference = %q", selfReference)
	}
	if got := parsePersonaEnders(enders); len(got) < 2 {
		t.Fatalf("猫娘应当给出多个候选：%v", got)
	}
	for _, style := range []ReplyStyle{ReplyStyleAssistant, ReplyStyleGroupmate, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise} {
		if self, ends := DefaultPersonaVoice(style); self != "" || ends != "" {
			t.Fatalf("%s 不该对自称和句尾有主张：%q %q", style, self, ends)
		}
	}
	// 扮演只主张自称：句尾语气词属于具体角色，不属于这套说话方式。
	if self, ends := DefaultPersonaVoice(ReplyStyleRoleplay); self != "我" || ends != "" {
		t.Fatalf("扮演的自称和句尾 = %q %q", self, ends)
	}
}

// 端到端：群友风格下的长回复现在也折成合并转发卡片，而不是刷一屏。
//
// 以前这里发的是十几条普通消息——风格把整条卡片分支短路掉了，「合并转发字数」
// 填多少都没用。
func TestRuntimeGroupmateLongReplyUsesForwardCard(t *testing.T) {
	channel := &recordingChannel{}
	long := strings.Repeat("刘翔在雅典夺冠那年的事说来话长喵。", 80) // 远超 900 字
	botRuntime := NewRuntime(BotConfig{
		GroupTriggers: []string{"Diana"},
		BotAccount:    "42",
		ReplyStyle:    ReplyStyleGroupmate,
	}.WithDefaults(), channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return &capturingLLMProvider{reply: long}, nil
	})

	event := MessageEvent{
		Kind:        EventKindGroup,
		SelfID:      "42",
		GroupID:     "123456",
		UserID:      "10001",
		MessageID:   "long-1",
		SenderLevel: 40, SenderLevelLabel: "LV40",
		RawMessage: "Diana 讲讲刘翔",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana 讲讲刘翔"}}},
	}
	if err := botRuntime.HandleEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	// 等得比群友风格的打字停顿上限（5 秒）还宽：走卡片时会在停顿之前就返回，
	// 所以正常路径是秒过；等这么久是为了万一退回散装时能数出条数，而不是超时
	// 了事——「一条没发」和「刷了十几条」是两种完全不同的故障。
	waitForCondition(t, 10*time.Second, func() bool {
		return len(channel.callsSnapshot()) > 0 || len(channel.sentSnapshot()) > 0
	})
	calls := channel.callsSnapshot()
	if len(calls) == 0 {
		t.Fatalf("没有走合并转发，散装发了 %d 条", len(channel.sentSnapshot()))
	}
	if calls[0].action != "send_group_forward_msg" {
		t.Fatalf("动作 = %q，期望 send_group_forward_msg", calls[0].action)
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("走了卡片就不该再散装发：%#v", sent)
	}
}

// 扮演这一档和猫娘正好相反：那边禁止动作描写，这边动作描写就是主体。
// 要守的是两处刹车——别写成小说、别拿动作顶替正事——
// 以及它同样没有豁免全局输出规则。
func TestRoleplayReplyStyleTeachesActionsAndKeepsBrakes(t *testing.T) {
	for _, raw := range []string{"roleplay", "Roleplay", " roleplay "} {
		if got := ReplyStyle(raw).Normalized(); got != ReplyStyleRoleplay {
			t.Fatalf("Normalized(%q) = %q", raw, got)
		}
	}
	prompt := ReplyStyleRoleplay.prompt(true, personaVoice{})
	// 骨架：括号动作可在台词间自然穿插多次，每处都要短。
	for _, want := range []string{"台词前、中间或结尾", "不必只写一处", "每处一句话以内", "第三人称叙述"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("扮演提示词缺少写法要点 %q：%q", want, prompt)
		}
	}
	// 三处刹车。
	for _, want := range []string{"就成小说了", "正事照常办"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("扮演提示词缺少刹车 %q：%q", want, prompt)
		}
	}
	// 以人设为由要求越界是这一档最大的攻击面，次序必须写死。
	if !strings.Contains(prompt, "规则优先，人设让位") {
		t.Fatalf("扮演提示词没有写死规则优先：%q", prompt)
	}
	// 语气靠示例教，抽象形容词教不会——群友和猫娘两档都是这么写的。
	if !strings.Contains(prompt, "示例——") || !strings.Contains(prompt, "用户：") {
		t.Fatalf("扮演提示词没有示例：%q", prompt)
	}
	// 全局规则照旧生效：不因为在演就能刷 emoji 或空行。
	for _, want := range []string{replyEmojiRule, replyBlankLineRule} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("扮演提示词漏掉了全局输出规则：%q", prompt)
		}
	}
	if !strings.Contains(ReplyStyleRoleplay.closingAnchor(), "一处或多处") {
		t.Fatalf("锚点没有把写法拉回来：%q", ReplyStyleRoleplay.closingAnchor())
	}
}

// 猫娘禁动作描写、扮演靠动作描写，两档的说法不能互相污染。
func TestRoleplayAndCatgirlDoNotContradictEachOther(t *testing.T) {
	catgirl := ReplyStyleCatgirl.prompt(true, personaVoice{})
	roleplay := ReplyStyleRoleplay.prompt(true, personaVoice{})
	if strings.Contains(roleplay, "不写动作描写") {
		t.Fatalf("扮演提示词里混进了猫娘的禁令：%q", roleplay)
	}
	if !strings.Contains(catgirl, "不写 *蹭蹭*") {
		t.Fatalf("猫娘那档的禁令被改掉了：%q", catgirl)
	}
}

func TestActionDescriptionIsAnIndependentPersonaPreservingLayer(t *testing.T) {
	combined := actionDescriptionPrompt(true) + "\n" + actionDescriptionClosingAnchor(true)
	for _, want := range []string{"原有人设和表达风格", "不必只写一处", "台词前、中间或结尾", "不额外变得黏人或亲密"} {
		if !strings.Contains(combined, want) {
			t.Fatalf("动作描写提示缺少 %q：%q", want, combined)
		}
	}
	if got := actionDescriptionPrompt(false); got != "" {
		t.Fatalf("关闭动作描写后仍注入了提示：%q", got)
	}
}

func TestLegacyRoleplayConfigMigratesToAssistantWithActions(t *testing.T) {
	cfg := (BotConfig{ReplyStyle: ReplyStyleRoleplay}).WithDefaults()
	if cfg.ReplyStyle != ReplyStyleAssistant {
		t.Fatalf("旧扮演风格迁移后的表达风格 = %q", cfg.ReplyStyle)
	}
	if !boolValue(cfg.ActionDescriptionEnabled, false) {
		t.Fatal("旧扮演风格没有迁移为动作描写开关")
	}
}
