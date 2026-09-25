// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 意图识别这一档的承诺是「整档可以绑只做判断的模型」。承诺要能验证：这一档底下
// 的每个用途，发请求时都必须带着判断题表，而且题表渲染出来的答案要能被各自的
// 解析器读回去。少一条，绑上 Jev 就会在那条路径上先失败一次再降级。
func TestEveryIntentPurposeCanRunOnDecisionModel(t *testing.T) {
	specs := map[string]*llm.DecisionSpec{
		PurposeProactiveReplyRouter:  proactiveReplyDecisionSpec(nil, nil),
		PurposeProactiveReplyQuality: replyAuditDecisionSpec(replyAuditNeed{Quality: true}),
		PurposeReplySendAudit:        replyAuditDecisionSpec(replyAuditNeed{Quality: true, AccountSafety: true}),
	}
	for purpose, group := range llmPurposeGroup {
		if group != llm.GroupIntent {
			continue
		}
		spec, ok := specs[purpose]
		if !ok {
			t.Fatalf("用途 %s 归在意图识别，却没有判断题表——要么补题表，要么把它挪到后台那一档", purpose)
		}
		if spec == nil || len(spec.Questions) == 0 {
			t.Fatalf("用途 %s 的判断题表是空的", purpose)
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("用途 %s 的判断题表不自洽：%v", purpose, err)
		}
	}
}

// 参与判定（是不是在跟机器人说话）同样要能回填成路由解析器读得懂的形状。
func TestParticipationDecisionRoundTrips(t *testing.T) {
	spec := participationDecisionSpec(nil)
	answers := map[string]llm.DecisionAnswer{}
	for _, question := range spec.Questions {
		switch question.Kind {
		case llm.DecisionNoul:
			answers[question.Key] = llm.DecisionAnswer{Kind: llm.DecisionNoul, Noul: 0.9}
		case llm.DecisionScore:
			answers[question.Key] = llm.DecisionAnswer{Kind: llm.DecisionScore, Score: 1, Confidence: 0.8}
		case llm.DecisionChoice:
			answers[question.Key] = llm.DecisionAnswer{Kind: llm.DecisionChoice, Choice: question.Options[0].Value, Confidence: 0.8}
		}
	}
	rendered, err := spec.RenderDecisionAnswers(answers)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(rendered, "directed") {
		t.Fatalf("参与判定回填缺了 directed：%s", rendered)
	}
}

// 写成段文字的用途绝不能被误挂上判断题表——挂了只会让判断模型答一堆是非题，
// 正文却是空的。异步的归后台生成，回复前同步跑的归回复辅助。
func TestTextPurposesStayOffIntent(t *testing.T) {
	writers := map[string]string{
		PurposeRSSWatchJudge:        llm.GroupBackground,
		PurposeMemoryExtract:        llm.GroupBackground,
		PurposeMemorySummary:        llm.GroupBackground,
		PurposeRelationshipEvaluate: llm.GroupBackground,
		PurposeContextSummary:       llm.GroupReplyAssist,
		PurposeDirectReplyTopic:     llm.GroupReplyAssist,
		PurposeReplySemanticDedup:   llm.GroupReplyAssist,
		PurposeSemanticTextRef:      llm.GroupReplyAssist,
		PurposeErrorNotice:          llm.GroupReplyAssist,
		PurposeReplySuppression:     llm.GroupReplyAssist,
		PurposePokeReply:            llm.GroupReplyAssist,
		PurposeWelcomeGenerator:     llm.GroupBackground,
		PurposeRomanceGreeting:      llm.GroupBackground,
	}
	for purpose, want := range writers {
		if got := ModelBindingGroupOf(purpose); got != want {
			t.Fatalf("%s 应归 %s，实际 %q", purpose, want, got)
		}
	}
}
