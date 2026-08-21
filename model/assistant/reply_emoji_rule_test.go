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
		ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleGroupmate, "",
	} {
		if !strings.Contains(style.prompt(), replyEmojiRule) {
			t.Errorf("风格 %q 的提示词没有带上 emoji 规则", style)
		}
	}
}

func TestGroupmateStyleDropsFillerWordQuota(t *testing.T) {
	// 语气词和颜文字的计数约束已经去掉：那是「最多一个」的上限，反而暗示可以带，
	// 而且「颜文字」说的是字符拼的表情，管不到 emoji。
	prompt := ReplyStyleGroupmate.prompt()
	for _, dropped := range []string{"语气词", "颜文字"} {
		if strings.Contains(prompt, dropped) {
			t.Errorf("群友风格提示词里仍然保留了 %q 的计数约束", dropped)
		}
	}
}

func TestReplyStylePromptKeepsStyleGuidance(t *testing.T) {
	// 加 emoji 规则不能把原来的风格描述挤掉。
	if !strings.Contains(ReplyStyleConcise.prompt(), "默认表达风格为简洁") {
		t.Fatal("风格本身的提示词丢了")
	}
	if !strings.Contains(ReplyStyleGroupmate.prompt(), "像群里一个熟悉的普通朋友那样说话") {
		t.Fatal("群友风格的提示词丢了")
	}
}
