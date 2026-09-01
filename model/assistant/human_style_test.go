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

// TestHumanStyleGetsTypingDelay 秒回是最容易暴露的一点，这一档必须有拟真停顿。
func TestHumanStyleGetsTypingDelay(t *testing.T) {
	if ReplyStyleHuman.typingDelay("测试一句话") <= 0 {
		t.Fatal("真人感档没有开口前的停顿")
	}
	// 别顺手把停顿加给所有风格：助手风格秒回是对的。
	if ReplyStyleAssistant.typingDelay("测试一句话") != 0 {
		t.Fatal("助手风格不该有拟真停顿")
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
