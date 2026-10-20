package assistant

import (
	"strings"
	"testing"
)

func TestEveryStylePrefersCompactChatWithoutHardLimit(t *testing.T) {
	for _, style := range []ReplyStyle{ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleCatgirl, ReplyStyleHuman, ""} {
		for _, natural := range []bool{false, true} {
			prompt := style.prompt(natural, personaVoice{})
			combined := prompt + style.closingAnchor()
			for _, obsolete := range []string{"默认一条", "默认写成一段", "同一条末尾", "追问也留在同一条", "同一件事先用一条"} {
				if strings.Contains(combined, obsolete) {
					t.Errorf("style=%q natural=%v still forces one message: %s", style, natural, obsolete)
				}
			}
			// 节奏照真人发消息来：一条说一件事、话多就多发几条，不是一条写长。
			if !strings.Contains(prompt, "一条说一件事") || !strings.Contains(prompt, "话多就分几条发") || strings.Contains(prompt, "尽量少发几条") {
				t.Fatal("pacing must prefer short one-thing messages over packing everything into one")
			}
			if !strings.Contains(prompt, replyCompactPacingRule) {
				t.Errorf("style=%q natural=%v missing pacing", style, natural)
			}
			if !strings.Contains(prompt, replyConversationalIntentRule) {
				t.Errorf("style=%q missing conversational intent guidance", style)
			}
			for _, obsolete := range []string{"二十字往上就该拆开", "句与句之间写 " + notificationSplitMarker, "先给结论是一段、再讲理由是一段"} {
				if strings.Contains(prompt, obsolete) {
					t.Errorf("style=%q still encourages excessive splitting: %s", style, obsolete)
				}
			}
			if !strings.Contains(prompt, "按需答全") || !strings.Contains(prompt, notificationLineMarker) {
				t.Fatal("pacing must preserve complete answers and structured formatting")
			}
			if strings.Contains(prompt, replySegmentationRule) != natural {
				t.Fatal("changed natural split toggle")
			}
		}
	}
}

func TestConversationalIntentPreservesExplicitHelp(t *testing.T) {
	for _, want := range []string{"闲聊", "不主动给建议", "明确求助和技术问题直接解答", "缺少决定性信息才问"} {
		if !strings.Contains(replyConversationalIntentRule, want) {
			t.Errorf("missing intent boundary %q", want)
		}
	}
}

func TestAnswerDepthTracksTheRequestNotJustItsTopic(t *testing.T) {
	for _, want := range []string{"本轮问题", "最小但完整", "必要条件、步骤和风险", "明确要求详细时展开", "不加重复总结或收尾邀请"} {
		if !strings.Contains(replyProportionRule, want) {
			t.Errorf("missing answer-depth boundary %q", want)
		}
	}
}
