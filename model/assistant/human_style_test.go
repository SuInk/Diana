// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 真人感这一档和群友档最容易被写重。它们的差别在情绪浓度，不在句子长度：群友是
// 中性的「熟悉的普通朋友」，这一档是「一个具体的人在跟你说话」。下面几条盯住的
// 是这个差别不被抹平，以及它没有越界到扮演档的动作描写上去。

func TestHumanStyleNormalizes(t *testing.T) {
	for _, input := range []ReplyStyle{"human", "HUMAN", " Human "} {
		if got := input.Normalized(); got != ReplyStyleHuman {
			t.Fatalf("Normalized(%q) = %q", input, got)
		}
	}
}

// TestHumanStyleDiffersFromGroupmate 两档的提示词不能一样，也不能只差几个字。
func TestHumanStyleDiffersFromGroupmate(t *testing.T) {
	human := ReplyStyleHuman.stylePrompt()
	groupmate := ReplyStyleGroupmate.stylePrompt()
	if human == groupmate {
		t.Fatal("真人感和群友拿到了同一份提示词")
	}
	if !strings.Contains(human, "情绪是外放的") {
		t.Fatal("真人感档没有写明情绪外放——那正是它区别于群友的地方")
	}
	if ReplyStyleHuman.closingAnchor() == ReplyStyleGroupmate.closingAnchor() {
		t.Fatal("两档的语气锚点是同一句")
	}
}

// TestHumanStyleStaysOutOfRoleplay 动作描写是扮演档的活。
//
// 这两档都在教「像真人」，很容易在改提示词时把括号动作抄过来。抄过来的后果不只是
// 风格串味：动作描写有独立的开关（ActionDescriptionEnabled），风格里硬写就绕过了它。
func TestHumanStyleStaysOutOfRoleplay(t *testing.T) {
	human := ReplyStyleHuman.stylePrompt()
	if !strings.Contains(human, "不写括号动作和神态") {
		t.Fatal("真人感档没有排除动作描写")
	}
	for _, forbidden := range []string{"动作或神态放在括号里", "第三人称叙述"} {
		if strings.Contains(human, forbidden) {
			t.Fatalf("真人感档混进了扮演档的写法：%s", forbidden)
		}
	}
}

// TestHumanStyleTeachesSplitting 分条是这一档拟真的主要手段。
func TestHumanStyleTeachesSplitting(t *testing.T) {
	human := ReplyStyleHuman.stylePrompt()
	if !strings.Contains(human, "<dianabr>") {
		t.Fatal("真人感档没有教分条标记")
	}
	if strings.Count(human, "<dianabr>") < 3 {
		t.Fatal("示例里没有把连发的密度示范出来，只讲规则模型学不会")
	}
}

// TestHumanStyleVoiceLeavesEndersOpen 句尾候选留空是有意的。
//
// 钉死一组语气词，模型会给每句话挂同一个后缀，读起来比助手腔更假——那正是这一档
// 要避开的东西。自称给「我」是因为这一档允许偶尔用名字自称，得有个基线。
func TestHumanStyleVoiceLeavesEndersOpen(t *testing.T) {
	self, enders := DefaultPersonaVoice(ReplyStyleHuman)
	if self != "我" {
		t.Fatalf("真人感档的默认自称 = %q", self)
	}
	if enders != "" {
		t.Fatalf("真人感档不该钉死句尾语气词，却给了 %q", enders)
	}
}

// TestHumanStyleGivesConcreteMessageLength 每条消息的长度要给具体数字。
//
// 抽象的「句子短」教不会长度——猫娘那一档的注释已经写过同一个教训：语气靠具体的
// 词和示例教，形容词教不会。这里的数字说的是「每一条」，不是「这一轮总共」。
func TestHumanStyleGivesConcreteMessageLength(t *testing.T) {
	human := ReplyStyleHuman.stylePrompt()
	if !strings.Contains(human, "十几个字") {
		t.Fatal("真人感档没有给出每条消息的具体长度")
	}
	// 必须写明这是「每条」而不是「总共」：MaxReplyChars 截的是整条回复，
	// 两个概念混起来会让模型把一轮压成一句。
	if !strings.Contains(human, "这说的是每一条的长度，不是这一轮总共能说多少") {
		t.Fatal("没有区分「每条长度」和「单轮总量」，模型会把一轮压成一句")
	}
}

// TestHumanStyleExemptsSubstantiveAnswers 正事不受长度限制。
//
// 「一条十几个字」和「正事照常答准」直接冲突：一段报错分析、一段命令，十几个字
// 写不完。不明写豁免的话，模型只有两条路——砍短答案，或者把代码切碎，两种都更糟。
func TestHumanStyleExemptsSubstantiveAnswers(t *testing.T) {
	human := ReplyStyleHuman.stylePrompt()
	if !strings.Contains(human, "正事不受这条限制") {
		t.Fatal("没有把正事从长度限制里豁免出去")
	}
	if !strings.Contains(human, "代码、命令和报错原文照原样整块给出") {
		t.Fatal("没有保护代码和报错原文不被切碎")
	}
}
