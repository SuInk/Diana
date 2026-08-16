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
	for _, style := range []ReplyStyle{ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise} {
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
