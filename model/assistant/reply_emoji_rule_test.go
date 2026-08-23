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

func TestEveryReplyStyleCarriesTheBlankLineRule(t *testing.T) {
	// 空行规则必须对所有风格生效：运行时把空行当分条信号，模型却当段落间距，
	// 从源头上不让它输出空行，两边就不会再对不上。
	for _, style := range []ReplyStyle{
		ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleGroupmate, "",
	} {
		if !strings.Contains(style.prompt(), replyBlankLineRule) {
			t.Errorf("风格 %q 的提示词没有带上空行规则", style)
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

// 篇幅规则对所有风格生效:闲聊一句话问的,即使联网查证过也不要写成小评测,
// 更不要在回复里罗列参考链接。
func TestEveryReplyStyleForbidsEssayAndLinkDump(t *testing.T) {
	for _, style := range []ReplyStyle{ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleGroupmate, ReplyStyle("")} {
		prompt := style.prompt()
		if !strings.Contains(prompt, replyProportionRule) {
			t.Fatalf("风格 %q 缺少篇幅与链接规则", style)
		}
	}
}

// 末尾标点规则对所有风格生效：聊天窗口里一条「知道了。」读起来是公事公办的冷淡，
// 真人不这么打字。
func TestEveryReplyStyleCarriesTheTrailingPunctuationRule(t *testing.T) {
	for _, style := range []ReplyStyle{
		ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleGroupmate, "",
	} {
		if !strings.Contains(style.prompt(), replyTrailingPunctuationRule) {
			t.Errorf("风格 %q 的提示词没有带上末尾标点规则", style)
		}
	}
}

// 规则只管末尾，不能把问号感叹号和句中标点一起禁掉——那会让稍长的回复连不成句，
// 也会把疑问和惊讶的语气抹平。
func TestTrailingPunctuationRuleKeepsQuestionAndMidSentenceMarks(t *testing.T) {
	for _, keep := range []string{"句子中间的标点照常使用", "问号和感叹号"} {
		if !strings.Contains(replyTrailingPunctuationRule, keep) {
			t.Errorf("末尾标点规则缺少例外说明 %q", keep)
		}
	}
}
