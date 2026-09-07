// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func TestNormalizeChatBubbleNewlinesTurnsSoftWrapsIntoPunctuation(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		input string
		want  string
	}{
		"Chinese":              {"你前面说得对\n缓存命中率确实需要看真实数据\n不能只跑 smoke test", "你前面说得对，缓存命中率确实需要看真实数据，不能只跑 smoke test"},
		"existing punctuation": {"先检查日志。\n然后重启服务", "先检查日志。然后重启服务"},
		"English":              {"Check the logs\nRestart the service", "Check the logs. Restart the service"},
		"English punctuation":  {"Check the logs.\nRestart the service", "Check the logs. Restart the service"},
		"mid sentence":         {"问题主要出在，\n第二次请求没有复用缓存", "问题主要出在，第二次请求没有复用缓存"},
		"prose label":          {"你前面说得对：\n这里只靠提示词不稳定", "你前面说得对：这里只靠提示词不稳定"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := normalizeChatBubbleNewlines(test.input, false); got != test.want {
				t.Fatalf("normalizeChatBubbleNewlines() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeChatBubbleNewlinesPreservesRealLayout(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]string{
		"list":     "处理步骤：\n1. 查看日志\n2. 重启服务",
		"quote":    "原文：\n> 第一行\n> 第二行",
		"heading":  "## 原因\n请求超时",
		"document": "第一段说明\n第二段说明",
	} {
		t.Run(name, func(t *testing.T) {
			preserve := name == "document"
			if got := normalizeChatBubbleNewlines(input, preserve); got != input {
				t.Fatalf("layout changed: %q", got)
			}
		})
	}
}

func TestNaturalSplitSwitchControlsSoftNewlineNormalization(t *testing.T) {
	t.Parallel()

	text := "前面说得对\n这次补上代码约束"
	enabled := splitChatReply(text, chatSplitLimits{MarkerOnly: true})
	if len(enabled) != 1 || enabled[0] != "前面说得对，这次补上代码约束" {
		t.Fatalf("enabled splitChatReply() = %#v", enabled)
	}
	disabled := splitChatReply(text, chatSplitLimits{MarkerOnly: true, PreserveSoftNewlines: true})
	if len(disabled) != 1 || disabled[0] != text {
		t.Fatalf("disabled splitChatReply() = %#v", disabled)
	}
}

func TestPerTurnDeliveryChoiceControlsSoftNewlineNormalization(t *testing.T) {
	t.Parallel()

	text := "前面说得对\n这次补上代码约束"
	single := splitChatReply(replySingleMarker+text, chatSplitLimits{})
	if len(single) != 1 || single[0] != text {
		t.Fatalf("single splitChatReply() = %#v", single)
	}
	auto := splitChatReply(replyAutoMarker+text, chatSplitLimits{MarkerOnly: true, PreserveSoftNewlines: true})
	if len(auto) != 2 || auto[0] != "前面说得对" || auto[1] != "这次补上代码约束" {
		t.Fatalf("auto splitChatReply() = %#v", auto)
	}
}
