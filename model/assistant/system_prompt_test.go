// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 默认人设只写「它是谁」，排版规则由独立的输出规范段落负责，不在人设里重复。
func TestDefaultSystemPromptCarriesNoFormattingRules(t *testing.T) {
	for _, unwanted := range []string{"Markdown", notificationSplitMarker, legacyNotificationSplitMarker, "OneBot v11 消息里", "DIANA_SYSTEM_PROMPT"} {
		if strings.Contains(defaultSystemPrompt, unwanted) {
			t.Fatalf("default persona should not mention %q: %q", unwanted, defaultSystemPrompt)
		}
	}
	// 输出规范仍然会被注入，只是不再由人设承担。
	if !strings.Contains(defaultPromptPlaintextRules, notificationSplitMarker) {
		t.Fatalf("plaintext rules should teach the split marker: %q", defaultPromptPlaintextRules)
	}
}

// 老配置里从旧默认人设继承来的排版规则升级时剥掉，用户自己写的部分保留。
func TestLegacySystemPromptDropsInheritedFormattingRules(t *testing.T) {
	legacy := "你是 Diana，运行在群聊里的机器人。" + legacyDefaultSystemPromptFormatRules
	if got := (BotConfig{SystemPrompt: legacy}).WithDefaults().SystemPrompt; got != "你是 Diana，运行在群聊里的机器人。" {
		t.Fatalf("migrated persona = %q", got)
	}

	custom := "你是一只爱吐槽的猫娘。" + legacyDefaultSystemPromptFormatRules + "群里只聊技术。"
	got := (BotConfig{SystemPrompt: custom}).WithDefaults().SystemPrompt
	if !strings.Contains(got, "爱吐槽的猫娘") || !strings.Contains(got, "群里只聊技术") {
		t.Fatalf("custom persona was damaged: %q", got)
	}
	if strings.Contains(got, "严禁在每个列表项") {
		t.Fatalf("inherited formatting rules survived: %q", got)
	}

	// 没有继承那段规则的人设一个字都不动。
	untouched := "你是一个只会说「喵」的机器人。"
	if got := (BotConfig{SystemPrompt: untouched}).WithDefaults().SystemPrompt; got != untouched {
		t.Fatalf("unrelated persona changed: %q", got)
	}
}

// 分条标记改名后，模型仍可能输出旧标记，两种写法都要能分条。
func TestSplitMarkersAcceptLegacyName(t *testing.T) {
	want := []string{"第一条", "第二条"}
	for _, marker := range []string{notificationSplitMarker, legacyNotificationSplitMarker} {
		got := splitReply("第一条"+marker+"第二条", 100)
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("splitReply with %s = %#v", marker, got)
		}
		if got := splitReply("第一条"+marker+"第二条", 100); len(got) != 2 {
			t.Fatalf("notification split with %s = %#v", marker, got)
		}
	}
}
