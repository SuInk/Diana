// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 判断模型答完之后，渲染出来的 JSON 必须能被审核解析器原样读回去——两边对不上
// 的话，绑上判断模型就等于把审核悄悄变成了「全部默认值」。
func TestReplyAuditDecisionSpecRoundTrips(t *testing.T) {
	spec := replyAuditDecisionSpec(replyAuditNeed{Quality: true, AccountSafety: true, Loop: true, Closing: true, Density: &replyDensity{}})
	answers := map[string]llm.DecisionAnswer{
		// Score 是档位下标（0 起），不是最终分值：这里选最高档。
		"send_confidence":         {Kind: llm.DecisionScore, Score: 2, Confidence: 0.9},
		"accuracy_issue":          {Kind: llm.DecisionChoice, Choice: "wording", Confidence: 0.8},
		"account_safe":            {Kind: llm.DecisionScore, Score: 0, Confidence: 0.9},
		"account_risk":            {Kind: llm.DecisionChoice, Choice: "politics", Confidence: 0.91},
		"count_refusal":           {Kind: llm.DecisionNoul, Noul: 0.93},
		"reply_loop_meaningless":  {Kind: llm.DecisionNoul, Noul: 0.88},
		"reply_loop_automated_ai": {Kind: llm.DecisionNoul, Noul: 0.12},
		"reply_loop_self_repeat":  {Kind: llm.DecisionNoul, Noul: 0.20},
		"reply_loop_purposeless":  {Kind: llm.DecisionNoul, Noul: 0.77},
		"conversation_closing":    {Kind: llm.DecisionNoul, Noul: 0.95},
		"stop_requested":          {Kind: llm.DecisionNoul, Noul: 0.10},
	}
	rendered, err := spec.RenderDecisionAnswers(answers)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	decision, ok := parseProactiveReplyQualityDecision(rendered)
	if !ok {
		t.Fatalf("审核解析器读不回去：%s", rendered)
	}
	if decision.Confidence < 0.95 || decision.AccuracyIssue != "wording" {
		t.Fatalf("准确性没对上：%#v", decision)
	}
	if decision.AccountSafeScore > 0.1 || decision.AccountRisk != "politics" {
		t.Fatalf("账号安全没对上：%#v", decision)
	}
	if !decision.CountRefusal || decision.RefusalConfidence < 0.9 {
		t.Fatalf("拒答没对上：%#v", decision)
	}
	if !decision.ReplyLoopMeaningless || decision.ReplyLoopAutomatedAI || !decision.ReplyLoopPurposeless {
		t.Fatalf("空转没对上：%#v", decision)
	}
	if !decision.ConversationClosing || decision.StopRequested {
		t.Fatalf("收尾没对上：%#v", decision)
	}
	// 每道题都要带上理由，上层日志和事件详情里「为什么没发」不能是空的。
	if decision.Reason == "" || decision.AccountRiskReason == "" || decision.ReplyLoopReason == "" || decision.ClosingReason == "" {
		t.Fatalf("理由缺失：%#v", decision)
	}
}

// 这一轮不需要的判断不该出现在题目里：判断模型按题计费，也按题思考。
func TestReplyAuditDecisionSpecOnlyAsksWhatIsNeeded(t *testing.T) {
	spec := replyAuditDecisionSpec(replyAuditNeed{AccountSafety: true})
	keys := map[string]bool{}
	for _, question := range spec.Questions {
		keys[question.Key] = true
	}
	if !keys["send_confidence"] || !keys["account_safe"] {
		t.Fatalf("缺了必问项：%#v", keys)
	}
	for _, unexpected := range []string{"accuracy_issue", "reply_loop_meaningless", "conversation_closing", "reply_loop_purposeless"} {
		if keys[unexpected] {
			t.Fatalf("这一轮不该问 %s：%#v", unexpected, keys)
		}
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("题表自身不自洽：%v", err)
	}
}

// 没有密度证据就不问「有没有目的」：问了也不作数，运行时会把它清成 false。
func TestReplyAuditDecisionSpecSkipsPurposeWithoutDensity(t *testing.T) {
	spec := replyAuditDecisionSpec(replyAuditNeed{Loop: true})
	for _, question := range spec.Questions {
		if question.Key == "reply_loop_purposeless" {
			t.Fatal("没有密度证据时不该问目的")
		}
	}
}
