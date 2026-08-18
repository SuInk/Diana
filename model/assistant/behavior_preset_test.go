// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
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
	for _, style := range []ReplyStyle{ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleMember} {
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

func TestReplyStyleMemberNormalizesAndReachesSystemPrompt(t *testing.T) {
	if got := ReplyStyle("Member").Normalized(); got != ReplyStyleMember {
		t.Fatalf("Normalized() = %q, want %q", got, ReplyStyleMember)
	}
	// 群友风格是「像真人一样说话」，不是「假装自己不是机器人」：被直接问起要如实回答。
	prompt := ReplyStyleMember.prompt()
	for _, want := range []string{"默认表达风格为群友", "被直接问起是不是机器人时如实回答"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("member prompt = %q, missing %q", prompt, want)
		}
	}

	base := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleAssistant}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"casual": {GroupID: "casual", ReplyStyle: ReplyStyleMember},
	}})
	if got := runtime.systemPrompt(MessageEvent{Kind: EventKindGroup, GroupID: "casual", UserID: "1"}, nil); !strings.Contains(got, "默认表达风格为群友") {
		t.Fatalf("system prompt missing group style: %q", got)
	}
}

func TestReplyStyleMemberDropsReplyReferenceAndMention(t *testing.T) {
	// 每条群回复都带引用和 @ 是最硬的机器人痕迹，prompt 管不到，得由风格关掉。
	cfg := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleMember}.WithDefaults()
	if boolValue(cfg.ReplyReferenceEnabled, true) || boolValue(cfg.MentionUserEnabled, true) {
		t.Fatalf("member style kept the bot-looking delivery flags: %#v", cfg)
	}

	// 其他风格不受影响，仍然默认带引用和 @。
	assistant := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleAssistant}.WithDefaults()
	if !boolValue(assistant.ReplyReferenceEnabled, true) || !boolValue(assistant.MentionUserEnabled, true) {
		t.Fatalf("assistant style should keep default delivery: %#v", assistant)
	}
}

func TestReplyStyleMemberRespectsExplicitDeliverySettings(t *testing.T) {
	// 用户手动打开过就尊重用户，preset 只负责填未设置的项。
	cfg := BotConfig{
		ResponseMode:          ResponseModeStandard,
		ReplyStyle:            ReplyStyleMember,
		ReplyReferenceEnabled: boolPointer(true),
		MentionUserEnabled:    boolPointer(true),
	}.WithDefaults()
	if !boolValue(cfg.ReplyReferenceEnabled, false) || !boolValue(cfg.MentionUserEnabled, false) {
		t.Fatalf("explicit delivery settings were overwritten: %#v", cfg)
	}
}

func TestReplyStyleMemberAppliesPerGroup(t *testing.T) {
	base := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleAssistant}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"casual": {GroupID: "casual", ReplyStyle: ReplyStyleMember},
	}})
	casual := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "casual"})
	if boolValue(casual.ReplyReferenceEnabled, true) || boolValue(casual.MentionUserEnabled, true) {
		t.Fatalf("group-level member style did not drop delivery flags: %#v", casual)
	}
}

func TestReplyStyleMemberUsesChatSizedDelivery(t *testing.T) {
	// 900 字一条、300ms 连发是机器人特征，群友风格要压到聊天体量和打字节奏。
	cfg := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleMember}.WithDefaults()
	if cfg.DirectReplyChunkSize != memberReplyChunkSize {
		t.Fatalf("DirectReplyChunkSize = %d, want %d", cfg.DirectReplyChunkSize, memberReplyChunkSize)
	}
	if cfg.SendChunkIntervalMS != memberSendChunkIntervalM {
		t.Fatalf("SendChunkIntervalMS = %d, want %d", cfg.SendChunkIntervalMS, memberSendChunkIntervalM)
	}

	// 比策略更克制的设置保留，更铺张的被压回来。
	tighter := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleMember, DirectReplyChunkSize: 80, SendChunkIntervalMS: 2000}.WithDefaults()
	if tighter.DirectReplyChunkSize != 80 || tighter.SendChunkIntervalMS != 2000 {
		t.Fatalf("tighter settings were overwritten: %#v", tighter)
	}

	// 其他风格不受影响。
	assistant := BotConfig{ResponseMode: ResponseModeStandard, ReplyStyle: ReplyStyleAssistant}.WithDefaults()
	if assistant.DirectReplyChunkSize != 900 || assistant.SendChunkIntervalMS != 300 {
		t.Fatalf("assistant delivery changed: %#v", assistant)
	}
}

func TestReplyStyleMemberNeverUsesForwardCard(t *testing.T) {
	// 合并转发是机器人专属控件，真人不会这么发言。
	if ReplyStyleMember.allowsForwardReply() {
		t.Fatal("member style must not fold replies into a forward card")
	}
	for _, style := range []ReplyStyle{ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise} {
		if !style.allowsForwardReply() {
			t.Fatalf("style %q unexpectedly lost forward replies", style)
		}
	}
}

func TestReplyStyleTypingDelayOnlyForMember(t *testing.T) {
	if got := ReplyStyleAssistant.typingDelay("随便一句话"); got != 0 {
		t.Fatalf("assistant typingDelay = %v, want 0", got)
	}
	if got := ReplyStyleMember.typingDelay("   "); got != 0 {
		t.Fatalf("blank text typingDelay = %v, want 0", got)
	}
	short := ReplyStyleMember.typingDelay("在的")
	long := ReplyStyleMember.typingDelay(strings.Repeat("字", 40))
	if short <= 0 || long <= short {
		t.Fatalf("typing delay should grow with length: short=%v long=%v", short, long)
	}
	if capped := ReplyStyleMember.typingDelay(strings.Repeat("字", 10000)); capped != memberTypingMaxDelay {
		t.Fatalf("typing delay = %v, want capped at %v", capped, memberTypingMaxDelay)
	}
}
