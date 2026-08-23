// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 旧版排版规则要求连续论述必须挤在同一条消息里,叠加空行折叠后长回答必然
// 糊成一个气泡。新版默认改为按意群分条;老配置里存的旧文案按完全匹配升级,
// 用户自己改过的一个字不动。
func TestPlaintextRulesMigrationAndNewDefault(t *testing.T) {
	if !strings.Contains(defaultPromptPlaintextRules, "意群边界写 "+notificationSplitMarker) {
		t.Fatalf("新默认应鼓励按意群分条:%s", defaultPromptPlaintextRules)
	}
	if !strings.Contains(defaultPromptPlaintextRules, "严禁在每个列表项前") {
		t.Fatalf("列表整体性约束不能丢:%s", defaultPromptPlaintextRules)
	}

	cfg := BotConfig{PromptPlaintextRulesText: legacyPromptPlaintextRules}.WithDefaults()
	if cfg.PromptPlaintextRulesText != defaultPromptPlaintextRules {
		t.Fatal("旧默认文案(dianabr 版)应升级到新版")
	}

	legacyBotbr := strings.ReplaceAll(legacyPromptPlaintextRules, notificationSplitMarker, legacyNotificationSplitMarker)
	cfg = BotConfig{PromptPlaintextRulesText: legacyBotbr}.WithDefaults()
	if cfg.PromptPlaintextRulesText != defaultPromptPlaintextRules {
		t.Fatal("旧默认文案(botbr 版)应升级到新版")
	}

	custom := "我自己写的排版规则"
	cfg = BotConfig{PromptPlaintextRulesText: custom}.WithDefaults()
	if cfg.PromptPlaintextRulesText != custom {
		t.Fatalf("用户自定义文案不得被改写:%q", cfg.PromptPlaintextRulesText)
	}

	cfg = BotConfig{}.WithDefaults()
	if cfg.PromptPlaintextRulesText != defaultPromptPlaintextRules {
		t.Fatal("留空应回落到新默认")
	}
}
