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

func TestReplyBrevityPreservesFormattingAndEvidence(t *testing.T) {
	if !strings.Contains(replyDepthClosingAnchor, "用户明确指定的格式逐项满足") {
		t.Fatal("closing anchor must preserve explicit formatting requests")
	}
	for name, prompt := range map[string]string{
		"closing": replyDepthClosingAnchor,
		"tools":   replyToolResultBrevityRule,
	} {
		for _, want := range []string{"并列要点可用列表", "按当前平台规则排版", "不为排版增加内容"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s missing formatting boundary %q", name, want)
			}
		}
		for _, obsolete := range []string{"不分条列点", "先用一小段", "几十个字就够"} {
			if strings.Contains(prompt, obsolete) {
				t.Errorf("%s still suppresses structure: %q", name, obsolete)
			}
		}
	}
	for _, want := range []string{"覆盖用户问到的各项", "一两句话能说清就不凑列表", "只写材料支持的事实", "不加开场套话、重复总结"} {
		if !strings.Contains(replyToolResultBrevityRule, want) {
			t.Errorf("tool reminder missing scope/evidence boundary %q", want)
		}
	}
}
