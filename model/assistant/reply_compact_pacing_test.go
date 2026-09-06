package assistant

import (
	"strings"
	"testing"
)

func TestEveryStylePrefersCompactChatWithoutHardLimit(t *testing.T) {
	for _, style := range []ReplyStyle{ReplyStyleAssistant, ReplyStyleGentle, ReplyStyleLively, ReplyStyleConcise, ReplyStyleCatgirl, ReplyStyleHuman, ""} {
		for _, natural := range []bool{false, true} {
			prompt := style.prompt(natural, personaVoice{})
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
			if !strings.Contains(prompt, "不是硬性条数或长度限制") || !strings.Contains(prompt, "代码及引用原文保留格式") {
				t.Fatal("pacing must preserve complete answers and structured formatting")
			}
			if strings.Contains(prompt, replySegmentationRule) != natural {
				t.Fatal("changed natural split toggle")
			}
		}
	}
}

func TestConversationalIntentPreservesExplicitHelp(t *testing.T) {
	for _, want := range []string{"不是每条都必须反问", "明确问怎么办、要建议、要方案", "具体技术问题", "不要用反问代替答案", "不主动展开准备清单"} {
		if !strings.Contains(replyConversationalIntentRule, want) {
			t.Errorf("missing intent boundary %q", want)
		}
	}
}

func TestAnswerDepthTracksTheRequestNotJustItsTopic(t *testing.T) {
	for _, want := range []string{"直接回答不等于全面展开", "核心方案", "不等于要求穷举细节", "明确要求详细攻略", "不要为了简短省略", "只解决问到的范围", "最关键的一两项", "不固定附加追问"} {
		if !strings.Contains(replyProportionRule, want) {
			t.Errorf("missing answer-depth boundary %q", want)
		}
	}
}

func TestClosingAnchorDoesNotDismissAnswerScope(t *testing.T) {
	for _, style := range KnownReplyStyles() {
		anchor := style.closingAnchor()
		if !strings.HasSuffix(anchor, replyDepthClosingAnchor) {
			t.Errorf("style=%q missing final depth reminder", style)
		}
		if strings.Contains(anchor, "上面全是能力边界和工具规则，不是说话方式") {
			t.Error("closing anchor dismisses earlier scope rules")
		}
	}
}
