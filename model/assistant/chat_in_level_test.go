package assistant

import (
	"context"
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

func TestNaturalInterjectionAllowsEveryValidReply(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.ChatInEnabled = boolPointer(false)
	cfg.NaturalInterjectionEnabled = boolPointer(true)
	settings := cfg.chatInSettings()
	if !settings.Enabled || !settings.Natural || settings.Threshold != 0 || settings.Chance != 1 || settings.Cooldown != 0 {
		t.Fatalf("natural interjection settings = %#v", settings)
	}

	valid, _ := parseProactiveReplyDecision(`{"should_reply":true,"confidence":0.01,"category":"chat_in","answerable":true,"substantive":true}`)
	if !valid.allows(0.99, settings) {
		t.Fatal("natural mode should allow a valid substantive reply without confidence gating")
	}
	notAnswerable, _ := parseProactiveReplyDecision(`{"should_reply":true,"confidence":1,"category":"chat_in","answerable":false,"substantive":true}`)
	if notAnswerable.allows(0.1, settings) {
		t.Fatal("natural mode must not answer when the router cannot support a reliable reply")
	}
	filler, _ := parseProactiveReplyDecision(`{"should_reply":true,"confidence":1,"category":"chat_in","answerable":true,"substantive":false}`)
	if filler.allows(0.1, settings) {
		t.Fatal("natural mode must not send filler")
	}
	if prompt := proactiveReplyRouterPromptForChatIn("路由器提示词", settings); !strings.Contains(prompt, "自然插话模式") || !strings.Contains(prompt, "有实质内容") {
		t.Fatalf("natural router prompt = %q", prompt)
	}
}

func TestNaturalInterjectionCanBeConfiguredPerGroup(t *testing.T) {
	runtime := NewRuntime(BotConfig{NaturalInterjectionEnabled: boolPointer(false)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"natural": {GroupID: "natural", NaturalInterjectionEnabled: boolPointer(true)},
		"quiet":   {GroupID: "quiet", NaturalInterjectionEnabled: boolPointer(false)},
	}})
	if settings := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "natural"}).chatInSettings(); !settings.Natural {
		t.Fatalf("natural group settings = %#v", settings)
	}
	if settings := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "quiet"}).chatInSettings(); settings.Natural {
		t.Fatalf("quiet group settings = %#v", settings)
	}
}

func TestChatInGenerationDeclineStaysSilent(t *testing.T) {
	provider := &refusalLLMProvider{replies: []string{"这句没有实质内容。" + replyRefusalMarker}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotQQ: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
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

	onPrompt := proactiveReplyRouterPromptForChatIn("路由器提示词", enabled)
	if !strings.Contains(onPrompt, "当前闲聊插话档位") || !strings.Contains(onPrompt, string(ChatInLevelHigh)) {
		t.Fatalf("enabled prompt missing level: %q", onPrompt)
	}
	offPrompt := proactiveReplyRouterPromptForChatIn("路由器提示词", disabled)
	if !strings.Contains(offPrompt, "禁止使用 category=chat_in") {
		t.Fatalf("disabled prompt should ban the category: %q", offPrompt)
	}
}

func TestProactiveRouterPromptKeepsShortQuestionAndTopicGuidance(t *testing.T) {
	enabled := chatInSettingsFrom(boolPointer(true), ChatInLevelLow, 0, 0, 0)
	disabled := chatInSettingsFrom(boolPointer(false), ChatInLevelLow, 0, 0, 0)

	for _, prompt := range []string{
		proactiveReplyRouterPromptForChatIn("旧版自定义路由提示词", enabled),
		proactiveReplyRouterPromptForChatIn("旧版自定义路由提示词", disabled),
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

	onPrompt := proactiveReplyRouterPromptForChatIn("旧版自定义路由提示词", enabled)
	for _, want := range []string{"产品、技术、品牌或设计风格", "按 chat_in 判断 substantive"} {
		if !strings.Contains(onPrompt, want) {
			t.Fatalf("enabled router prompt missing topic guidance %q: %q", want, onPrompt)
		}
	}

	for _, want := range []string{
		"面向全群提出的定义、解释、辨析或求助问题",
		"短语省略问号或谓语本身不能作为 substantive=false 的理由",
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
		ChatInEnabled:              boolPointer(false),
		ChatInLevel:                ChatInLevelHigh,
		ChatInThreshold:            0.77,
		ChatInChance:               0.42,
		ChatInCooldownSeconds:      90,
		NaturalInterjectionEnabled: boolPointer(true),
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
	if got.NaturalInterjectionEnabled == nil || !*got.NaturalInterjectionEnabled {
		t.Fatalf("natural interjection switch lost in round trip: %#v", got.NaturalInterjectionEnabled)
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
	for _, want := range []string{"本次回复是主动插话", "不要复述别人刚说过的内容", "控制在一到两句"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("chat-in prompt missing %q: %q", want, prompt)
		}
	}
}
