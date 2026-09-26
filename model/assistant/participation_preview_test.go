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
