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

// TestHumanStyleDiffersFromAssistant 两档的提示词不能一样，也不能只差几个字。
func TestHumanStyleDiffersFromAssistant(t *testing.T) {
	human := ReplyStyleHuman.stylePrompt()
	assistant := ReplyStyleAssistant.stylePrompt()
	if human == assistant {
		t.Fatal("真人感和助手拿到了同一份提示词")
	}
	if !strings.Contains(human, "情绪是外放的") {
		t.Fatal("真人感档没有写明情绪外放——那正是它区别于助手的地方")
	}
	if ReplyStyleHuman.closingAnchor() == ReplyStyleAssistant.closingAnchor() {
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

// TestHumanStyleTeachesSplitting 示例要同时示范两件事：确实另起一次发言时写标记，
// 同一件事的回应加追问留在一条里。示例比规则管用，只留连发示例会把节奏规则顶回去。
func TestHumanStyleTeachesSplitting(t *testing.T) {
	human := ReplyStyleHuman.stylePrompt()
	if !strings.Contains(human, notificationSplitMarker) {
		t.Fatal("真人感档没有教分条标记")
	}
	for _, merged := range []string{"你：在的，怎么啦", "你：这么快？厉害啊你"} {
		if !strings.Contains(human, merged) {
			t.Fatalf("缺少把回应和追问放在同一条里的示例：%s", merged)
		}
	}
	for _, fragment := range []string{"在的" + notificationSplitMarker + "怎么啦", "这么快？" + notificationSplitMarker + "厉害", "哪一轮啊" + notificationSplitMarker} {
		if strings.Contains(human, fragment) {
			t.Fatalf("示例仍把同一件事拆成碎片连发：%s", fragment)
		}
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

func TestHumanStyleDoesNotForceShortBubbles(t *testing.T) {
	human := ReplyStyleHuman.stylePrompt()
	if !strings.Contains(human, "不按字数强行拆消息") {
		t.Fatal("真人感档仍可能按字数强行拆成碎片")
	}
	if !strings.Contains(human, "尽量少发几条") {
		t.Fatal("缺少减少闲聊分条的要求")
	}
	if strings.Contains(human, "二十字往上就该拆开") {
		t.Fatal("旧版强制连发规则回归")
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
