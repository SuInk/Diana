// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func TestParticipationPreviewCarriesUnsavedOverrides(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.Name = "Diana"
	cfg.ProactiveReplyExtraCriteria = "群里叫「鸽子」是催更"
	cfg.PromptOverrides = PromptOverrides{
		promptParticipationRelevanceTrueSpec.Key:    "有人叫它小D",
		promptParticipationChatInLevelsSpec.Key:     "0.00 叫停\n0.90 有人聊猫",
		promptParticipationRouteInstructionSpec.Key: "先读上下文再评分。",
	}
	preview, err := PreviewParticipationPrompt(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if preview.System != proactiveReplyRouteSystemPrompt(cfg, cfg.chatInSettings()) {
		t.Fatal("preview system prompt differs from what the runtime sends")
	}
	for _, want := range []string{"有人叫它小D", "群里叫「鸽子」是催更", cfg.prompt(promptRouterCriteriaGuardSpec)} {
		if !strings.Contains(preview.System, want) {
			t.Fatalf("system prompt is missing %q", want)
		}
	}
	if !strings.HasPrefix(preview.User, "先读上下文再评分。") || !strings.Contains(preview.User, "【当前消息】[刚刚] 小林：有人知道改到哪天了吗") {
		t.Fatalf("user message is not instruction + payload: %s", preview.User)
	}
	if preview.Retry != cfg.prompt(promptParticipationRetrySpec) {
		t.Fatal("retry reminder missing")
	}
	if len(preview.Decision) != 2 || preview.Decision[0].TrueCriteria != "有人叫它小D" || len(preview.Decision[1].Levels) != 2 {
		t.Fatalf("decision questions did not pick up the overrides: %+v", preview.Decision)
	}
}

func TestParticipationIdentityFollowsCurrentProfile(t *testing.T) {
	for _, name := range []string{"星河", "月白"} {
		cfg := DefaultBotConfig()
		cfg.Name = name
		cfg.GroupTriggers = []string{"小助手"}
		cfg.SystemPrompt = "{{name}}是天文社成员，朋友叫小雨。"
		preview, err := PreviewParticipationPrompt(cfg)
		if err != nil {
			t.Fatal(err)
		}
		for _, chatIn := range []chatInSettings{cfg.chatInSettings(), {}} {
			prompt := proactiveReplyRouteSystemPrompt(cfg, chatIn)
			for _, want := range []string{`"name":"` + name + `"`, name + "是天文社成员", "朋友叫小雨", "其他机器人、其他群友和人设中的其他人物都不等于本人", "「本人」「我」以及判据中的「机器人」只指上述当前角色"} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("routing identity missing %q", want)
				}
			}
			if strings.Contains(prompt, "{{name}}") {
				t.Fatal("routing persona retains an unexpanded name")
			}
		}
		if preview.System != proactiveReplyRouteSystemPrompt(cfg, cfg.chatInSettings()) {
			t.Fatal("identity preview differs from runtime")
		}
	}
}
