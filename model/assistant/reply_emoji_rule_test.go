// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func TestEveryReplyStyleCarriesTheEmojiRule(t *testing.T) {
	// emoji 规则必须对所有风格生效：用户切了风格不等于想看 😂。
	for _, style := range []ReplyStyle{
		ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleHuman, "",
	} {
		if !strings.Contains(style.prompt(true, personaVoice{}), replyEmojiRule) {
			t.Errorf("风格 %q 的提示词没有带上 emoji 规则", style)
		}
	}
}

func TestEveryReplyStyleCarriesTheBlankLineRule(t *testing.T) {
	// 空行规则必须对所有风格生效：运行时把空行当分条信号，模型却当段落间距，
	// 从源头上不让它输出空行，两边就不会再对不上。
	for _, style := range []ReplyStyle{
		ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleHuman, "",
	} {
		if !strings.Contains(style.prompt(true, personaVoice{}), replyBlankLineRule) {
			t.Errorf("风格 %q 的提示词没有带上空行规则", style)
		}
	}
}

func TestReplyStylePromptKeepsStyleGuidance(t *testing.T) {
	// 加 emoji 规则不能把原来的风格描述挤掉。
	if !strings.Contains(ReplyStyleConcise.prompt(true, personaVoice{}), "默认表达风格为简洁") {
		t.Fatal("风格本身的提示词丢了")
	}
	if !strings.Contains(ReplyStyleHuman.prompt(true, personaVoice{}), "情绪是外放的") {
		t.Fatal("真人感风格的提示词丢了")
	}
}

// 篇幅规则对所有风格生效:闲聊一句话问的,即使联网查证过也不要写成小评测,
// 更不要在回复里罗列参考链接。
func TestEveryReplyStyleForbidsEssayAndLinkDump(t *testing.T) {
	for _, style := range []ReplyStyle{ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleHuman, ReplyStyle("")} {
		prompt := style.prompt(true, personaVoice{})
		if !strings.Contains(prompt, replyProportionRule) {
			t.Fatalf("风格 %q 缺少篇幅与链接规则", style)
		}
	}
}

// 回复风格不再禁止句号；标点应按语义自然使用。
func TestReplyStylesDoNotForbidTrailingPunctuation(t *testing.T) {
	for _, style := range []ReplyStyle{
		ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleHuman, "",
	} {
		prompt := style.prompt(true, personaVoice{})
		if strings.Contains(prompt, "句末不打句号") || strings.Contains(prompt, "结尾不要用句号") {
			t.Errorf("风格 %q 仍禁止自然句号：%s", style, prompt)
		}
	}
}
