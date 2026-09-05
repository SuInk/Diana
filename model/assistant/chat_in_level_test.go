// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestChatInSettingsResolveLevelsAndOverrides(t *testing.T) {
	on := true
	off := false

	medium := chatInSettingsFrom(&on, ChatInLevelMedium, 0, 0, 0)
	high := chatInSettingsFrom(&on, ChatInLevelHigh, 0, 0, 0)
	if !medium.Enabled || !high.Enabled {
		t.Fatalf("levels should stay enabled: %#v %#v", medium, high)
	}
	// 档位越高越爱说话：阈值更低、采样率更高、冷却更短。
	if !(high.Threshold < medium.Threshold && high.Chance > medium.Chance && high.Cooldown < medium.Cooldown) {
		t.Fatalf("higher level is not chattier: medium=%#v high=%#v", medium, high)
	}

	// off 档位必须压过开关，避免“开着但档位是关”这种自相矛盾的状态。
	if settings := chatInSettingsFrom(&on, ChatInLevelOff, 0, 0, 0); settings.Enabled {
		t.Fatalf("off level must win over the switch: %#v", settings)
	}
	// 总开关关闭时，任何档位都不放行。
	if settings := chatInSettingsFrom(&off, ChatInLevelMax, 0, 0, 0); settings.Enabled {
		t.Fatalf("switch off must disable every level: %#v", settings)
	}

	// 显式自定义覆盖档位预设。
	custom := chatInSettingsFrom(&on, ChatInLevelLow, 0.72, 0.9, 45)
	if custom.Threshold != 0.72 || custom.Chance != 0.9 || custom.Cooldown != 45*time.Second {
		t.Fatalf("explicit overrides ignored: %#v", custom)
	}
	// 未设置的项仍然回落到档位预设。
	partial := chatInSettingsFrom(&on, ChatInLevelHigh, 0, 0, 45)
	if partial.Threshold != chatInLevelPresets[ChatInLevelHigh].Threshold || partial.Cooldown != 45*time.Second {
		t.Fatalf("partial override should keep the preset: %#v", partial)
	}

	// 无法识别的档位回落到默认档，而不是变成关闭或零值。
	fallback := chatInSettingsFrom(nil, ChatInLevel("不存在的档位"), 0, 0, 0)
	if fallback.Level != defaultChatInLevel || !fallback.Enabled {
		t.Fatalf("unknown level should fall back to the default: %#v", fallback)
	}
}

func TestChatInDecisionRequiresSubstantiveContent(t *testing.T) {
	enabled := chatInSettingsFrom(boolPointer(true), ChatInLevelMedium, 0, 0, 0)
	disabled := chatInSettingsFrom(boolPointer(false), ChatInLevelMedium, 0, 0, 0)

	substantive := `{"should_reply":true,"confidence":0.93,"category":"chat_in","directed_at_bot":false,"answerable":true,"substantive":true}`
	filler := `{"should_reply":true,"confidence":0.99,"category":"chat_in","directed_at_bot":false,"answerable":true,"substantive":false}`

	decision, ok := parseProactiveReplyDecision(substantive)
	if !ok || !decision.chatIn() {
		t.Fatalf("chat_in decision did not parse: %#v ok=%v", decision, ok)
	}
	if !decision.allows(0.9, enabled) {
		t.Fatalf("substantive chat-in should be allowed: %#v", decision)
	}
	// 关掉开关后，同一条判定必须被拒。
	if decision.allows(0.9, disabled) {
		t.Fatalf("chat-in must be blocked while disabled: %#v", decision)
	}

	// 没有实质内容时，置信度再高也不放行——这正是“不要无意义插话”的闸门。
	empty, ok := parseProactiveReplyDecision(filler)
	if !ok {
		t.Fatalf("filler decision did not parse")
	}
	if empty.allows(0.9, enabled) {
		t.Fatalf("filler chat-in must be blocked: %#v", empty)
	}

	// 闲聊插话走自己的阈值，不受主动回复阈值影响。
	lowConfidence, _ := parseProactiveReplyDecision(`{"should_reply":true,"confidence":0.89,"category":"chat_in","directed_at_bot":false,"answerable":true,"substantive":true}`)
	if lowConfidence.allows(0.99, enabled) != (0.89 >= enabled.Threshold) {
		t.Fatalf("chat-in should use its own threshold %.2f: %#v", enabled.Threshold, lowConfidence)
	}
}

func TestChatInGenerationDeclineStaysSilent(t *testing.T) {
	provider := &refusalLLMProvider{replies: []string{"这句没有实质内容。" + replyRefusalMarker}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "20002", RawMessage: "普通群聊", chatInReply: true}

	reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
	if !errors.Is(err, errChatInReplyDeclined) || reply != "" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("declined chat-in sent messages: %#v", channel.sent)
	}
	if _, active := runtime.activeReplySuppression(event, time.Now()); active {
		t.Fatal("a silent chat-in decline must not activate user suppression")
	}
}

func TestChatInRouterPromptReflectsSwitch(t *testing.T) {
	enabled := chatInSettingsFrom(boolPointer(true), ChatInLevelHigh, 0, 0, 0)
	disabled := chatInSettingsFrom(boolPointer(false), ChatInLevelHigh, 0, 0, 0)

	onPrompt := proactiveReplyRouterPromptForChatIn("路由器提示词", enabled, false)
	if !strings.Contains(onPrompt, "当前闲聊插话档位") || !strings.Contains(onPrompt, string(ChatInLevelHigh)) {
		t.Fatalf("enabled prompt missing level: %q", onPrompt)
	}
	offPrompt := proactiveReplyRouterPromptForChatIn("路由器提示词", disabled, false)
	if !strings.Contains(offPrompt, "禁止使用 category=chat_in") {
		t.Fatalf("disabled prompt should ban the category: %q", offPrompt)
	}
}

func TestProactiveRouterPromptKeepsShortQuestionAndTopicGuidance(t *testing.T) {
	enabled := chatInSettingsFrom(boolPointer(true), ChatInLevelLow, 0, 0, 0)
	disabled := chatInSettingsFrom(boolPointer(false), ChatInLevelLow, 0, 0, 0)

	for _, prompt := range []string{
		proactiveReplyRouterPromptForChatIn("旧版自定义路由提示词", enabled, false),
		proactiveReplyRouterPromptForChatIn("旧版自定义路由提示词", disabled, false),
	} {
		for _, want := range []string{
			"没有点名机器人不等于不需要回复",
			"定义、解释、辨析或求助问题",
			"不得仅因句子短",
			"应视为该问题仍在等待回答并使用 needs_response",
		} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("router prompt missing %q: %q", want, prompt)
			}
		}
	}

	onPrompt := proactiveReplyRouterPromptForChatIn("旧版自定义路由提示词", enabled, false)
	for _, want := range []string{
		"围绕上下文中可识别的话题",
		"按 chat_in 判断 substantive",
		"轻松调侃、反问或接梗",
		"风格化表达也可以构成 substantive",
		"新的观察、画面、情绪或笑点",
		"不要求这句话必须包含可核实事实",
		"形容词堆砌",
		"你不是最喜欢看小说吗",
		"directed_at_bot=false",
	} {
		if !strings.Contains(onPrompt, want) {
			t.Fatalf("enabled router prompt missing topic guidance %q: %q", want, onPrompt)
		}
	}

	for _, want := range []string{
		"面向全群提出的定义、解释、辨析或求助问题",
		"不属于 needs_response",
		"满足第 6.1 至 6.4 条时才可使用 chat_in",
		"短语省略问号或谓语本身不能作为 substantive=false 的理由",
		"轻松调侃、反问或接梗",
		"风格化表达不要求包含可核实事实",
		"套话换皮、无关抒情、同义复述和形容词堆砌",
		"你不是最喜欢看小说吗",
		"应视为该问题仍在等待回答并使用 needs_response",
	} {
		if !strings.Contains(defaultProactiveReplyRouterPrompt, want) {
			t.Fatalf("default router prompt missing %q", want)
		}
	}
}

func TestChatInSurvivesConfigPayloadRoundTrip(t *testing.T) {
	// WebUI 存一次配置就会走这条来回转换；漏字段会静默把开关和档位重置成默认值。
	cfg := BotConfig{
		ChatInEnabled:         boolPointer(false),
		ChatInLevel:           ChatInLevelHigh,
		ChatInThreshold:       0.77,
		ChatInChance:          0.42,
		ChatInCooldownSeconds: 90,
	}
	got := ConfigFromPayload(PayloadFromConfig(cfg), BotConfig{})
	if got.ChatInEnabled == nil || *got.ChatInEnabled {
		t.Fatalf("chat-in switch lost in round trip: %#v", got.ChatInEnabled)
	}
	if got.ChatInLevel != ChatInLevelHigh {
		t.Fatalf("chat-in level = %q, want %q", got.ChatInLevel, ChatInLevelHigh)
	}
	if got.ChatInThreshold != 0.77 || got.ChatInChance != 0.42 || got.ChatInCooldownSeconds != 90 {
		t.Fatalf("chat-in overrides lost in round trip: %#v", got)
	}
}

func TestChatInCooldownBlocksRepeatedInterjections(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001"}

	if !runtime.chatInCooldownAllows(event, time.Minute) {
		t.Fatal("first interjection should be allowed")
	}
	runtime.markChatInReplied(event)
	if runtime.chatInCooldownAllows(event, time.Minute) {
		t.Fatal("second interjection should be blocked by the cooldown")
	}
	// 冷却为 0 表示不限频。
	if !runtime.chatInCooldownAllows(event, 0) {
		t.Fatal("zero cooldown should never block")
	}
	// 冷却按群隔离，别的群不受影响。
	other := MessageEvent{Kind: EventKindGroup, GroupID: "456", UserID: "10001"}
	if !runtime.chatInCooldownAllows(other, time.Minute) {
		t.Fatal("cooldown must not leak across groups")
	}
}

func TestChatInReplyPromptOnlyAppearsForInterjections(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", SenderName: "小明"}

	if prompt := runtime.systemPrompt(event, nil); strings.Contains(prompt, "本次回复是主动插话") {
		t.Fatalf("normal reply must not carry the chat-in prompt: %q", prompt)
	}
	event.chatInReply = true
	prompt := runtime.systemPrompt(event, nil)
	for _, want := range []string{
		"本次回复是主动插话",
		"风格化表达本身也可以是内容",
		"比喻、拟人、意象、节奏感或角色口吻",
		"一次集中使用一两个最贴切的手法",
		"不要堆形容词、套网感模板",
		"事实、技术和操作内容仍以清楚准确为先",
		"不与事实和用户明确要求冲突",
		"优先遵循已配置的人设与口吻",
		"不要复述别人刚说过的内容",
		"控制在一到两句",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("chat-in prompt missing %q: %q", want, prompt)
		}
	}
}

func TestResponseModePresetClearsChatInFineTuning(t *testing.T) {
	preset := BotConfig{
		ResponseMode:          ResponseModeStandard,
		ChatInThreshold:       0.6,
		ChatInChance:          0.9,
		ChatInCooldownSeconds: 5,
	}.WithDefaults()
	if preset.ChatInLevel != ChatInLevelLow {
		t.Fatalf("preset level = %q", preset.ChatInLevel)
	}
	if preset.ChatInThreshold != 0 || preset.ChatInChance != 0 || preset.ChatInCooldownSeconds != 0 {
		t.Fatalf("preset kept stale fine-tuning: %#v", preset)
	}
	if settings := preset.chatInSettings(); settings.Threshold != chatInLevelPresets[ChatInLevelLow].Threshold {
		t.Fatalf("effective threshold = %v, want the level preset", settings.Threshold)
	}
	// 自定义模式下这三项仍然是用户说了算。
	custom := BotConfig{
		ResponseMode:          ResponseModeCustom,
		ChatInLevel:           ChatInLevelLow,
		ChatInThreshold:       0.6,
		ChatInChance:          0.9,
		ChatInCooldownSeconds: 5,
	}.WithDefaults()
	if custom.ChatInThreshold != 0.6 || custom.ChatInChance != 0.9 || custom.ChatInCooldownSeconds != 5 {
		t.Fatalf("custom mode lost fine-tuning: %#v", custom)
	}
}

func TestChatInCooldownIsNotConsumedByRoutingAlone(t *testing.T) {
	provider := &refusalLLMProvider{replies: []string{"端口被占了。"}}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "9", MessageID: "m-1", RawMessage: "这个报错什么意思", chatInReply: true}
	// 路由放行之后回复仍可能被质量审核或发送失败挡下来，那时冷却不该被吃掉。
	if !runtime.chatInCooldownAllows(event, time.Minute) {
		t.Fatal("cooldown was already consumed before any reply was sent")
	}
	if _, err := runtime.replyAndRecord(context.Background(), event, event.RawMessage, "replied_proactive"); err != nil {
		t.Fatalf("replyAndRecord: %v", err)
	}
	if runtime.chatInCooldownAllows(event, time.Minute) {
		t.Fatal("cooldown was not started after the interjection was delivered")
	}
}

// TestSocialReplyGuardOnlyAppearsWhenEnabled 盯住社交性回应这条规则的注入。
//
// 线上现象：群友说一句「笨笨」，路由判成 directed_at_bot=true、answerable=true、
// substantive=false，于是 category=none 保持沉默。对助手型人设这是对的，对陪聊型
// 人设就是装死。开关打开时才补这条规则，关着不能污染默认提示词。
func TestSocialReplyGuardOnlyAppearsWhenEnabled(t *testing.T) {
	chatIn := chatInSettings{Enabled: true, Level: ChatInLevelMedium, Threshold: 0.9}

	off := proactiveReplyRouterPromptForChatIn("", chatIn, false)
	if strings.Contains(off, socialReplyGuard) {
		t.Fatalf("开关关着却注入了社交性回应规则：\n%s", off)
	}

	on := proactiveReplyRouterPromptForChatIn("", chatIn, true)
	if !strings.Contains(on, socialReplyGuard) {
		t.Fatalf("开关打开却没有注入社交性回应规则：\n%s", on)
	}
	// 打开之后原有的守则一条都不能少：这条是加法，不是换一套提示词。
	if !strings.Contains(on, "当前闲聊插话档位") {
		t.Fatalf("注入社交规则时丢掉了闲聊档位说明：\n%s", on)
	}

	// 规则本身必须写清楚放行边界，否则「被搭话就回」会退化成什么都回。
	for _, want := range []string{"directed_at_bot=true", "不是对机器人说的话", "别再说话", "已经回过"} {
		if !strings.Contains(socialReplyGuard, want) {
			t.Fatalf("社交性回应规则缺少边界约束 %q：%s", want, socialReplyGuard)
		}
	}
}

// TestSocialReplyEnabledFlowsFromGroupConfig 群级开关要真的生效。GroupConfig 里
// 存了却没拷回生效配置，就是又一个「填了不生效」的输入框。
func TestSocialReplyEnabledFlowsFromGroupConfig(t *testing.T) {
	base := BotConfig{ResponseMode: ResponseModeStandard}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"social": {GroupID: "social", SocialReplyEnabled: boolPointer(true)},
		"quiet":  {GroupID: "quiet", SocialReplyEnabled: boolPointer(false)},
	}})

	social := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "social"})
	if !boolValue(social.SocialReplyEnabled, false) {
		t.Fatal("群级打开的社交性回应没有生效")
	}
	quiet := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "quiet"})
	if boolValue(quiet.SocialReplyEnabled, false) {
		t.Fatal("群级关掉的社交性回应没有生效")
	}
}

// 自然插话模式已经删掉。它当年的作用是让 chat_in 绕开置信度、采样率和冷却，只看
// substantive；五个回复模式预设早就全部强制关掉它，界面上也只在自定义模式下才露脸，
// 留着只会让人以为还能开。
//
// 这里盯两件事：老配置里残留的那个键不能把行为带回来，以及任何档位下 chat_in 都要
// 过置信度门槛。
func TestNaturalInterjectionIsGone(t *testing.T) {
	// 存量配置是 JSON，删字段之后这个键会被直接忽略，不需要迁移。
	var cfg BotConfig
	raw := `{"chat_in_enabled":true,"chat_in_level":"low","natural_interjection_enabled":true}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	settings := cfg.WithDefaults().chatInSettings()
	if settings.Threshold <= 0 || settings.Chance <= 0 {
		t.Fatalf("旧键把档位参数清零了，等于自然插话又活了：%#v", settings)
	}

	// 有实质内容但置信度低于档位阈值：自然插话时代会放行，现在必须挡住。
	lowConfidence, _ := parseProactiveReplyDecision(`{"should_reply":true,"confidence":0.01,"category":"chat_in","answerable":true,"substantive":true}`)
	if lowConfidence.allows(0.99, settings) {
		t.Fatalf("chat_in 绕过了置信度门槛：%#v", settings)
	}

	// 路由器提示词也不该再提它。
	if prompt := proactiveReplyRouterPromptForChatIn("路由器提示词", settings, false); strings.Contains(prompt, "自然插话") {
		t.Fatalf("router prompt = %q", prompt)
	}
}
