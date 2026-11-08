// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func TestTextSettingEqualToFallbackIsStoredEmpty(t *testing.T) {
	spec := followUpPromptSetting()
	if spec.Fallback != promptFollowUp {
		t.Fatalf("跟评提示词应该把内置跟评要求作为 fallback 下发，得到 %q", spec.Fallback)
	}
	value, err := normalizeSettingValue(spec, "  "+strings.ReplaceAll(promptFollowUp, "\n", "\r\n")+"\n")
	if err != nil || value != "" {
		t.Fatalf("原样提交默认内容应存空串以跟随升级，得到 %q, %v", value, err)
	}
	value, err = normalizeSettingValue(spec, "只说一句。")
	if err != nil || value != "只说一句。" {
		t.Fatalf("自定义内容应原样保存，得到 %q, %v", value, err)
	}
}

func TestFollowUpPromptFallbackUsesBotOverride(t *testing.T) {
	state := PluginState{Manifest: PluginManifest{Settings: []PluginSettingSpec{followUpPromptSetting()}}}
	cfg := BotConfig{PromptOverrides: PromptOverrides{promptFollowUpSpec.Key: "机器人自己的跟评要求"}}
	got := WithFollowUpPromptFallback(state, cfg)
	if got.Manifest.Settings[0].Fallback != "机器人自己的跟评要求" {
		t.Fatalf("选了机器人时应显示它改过的跟评要求，得到 %q", got.Manifest.Settings[0].Fallback)
	}
	if state.Manifest.Settings[0].Fallback != promptFollowUp {
		t.Fatal("不能改写共享的设置切片")
	}
	if same := WithFollowUpPromptFallback(state, BotConfig{}); same.Manifest.Settings[0].Fallback != promptFollowUp {
		t.Fatal("机器人没改过时仍显示内置默认")
	}
}

func TestHiddenTextDefaultsAreExposedAsFallback(t *testing.T) {
	coding := NewCodingAgentPlugin().Manifest().Settings
	for _, spec := range coding {
		switch spec.Key {
		case codingAgentSettingApprovalPatterns:
			if !strings.Contains(spec.Fallback, "git push") {
				t.Fatalf("危险操作清单应下发内置默认，得到 %q", spec.Fallback)
			}
		case codingAgentSettingTemplate, codingAgentSettingProfiles:
			if spec.FallbackBy == nil || spec.FallbackBy.Values[codingBackendClaude] == "" || spec.FallbackBy.Values[codingBackendCodex] == "" {
				t.Fatalf("%s 应按后端下发预置命令模板", spec.Key)
			}
		}
	}
}
