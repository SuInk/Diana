// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
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

// 判断模型逐题给置信度。「复读自己」不能借「没空转」那一题的数：线上一条回复
// 「没空转」判得很稳（0.94），「在复读」只是略过半，却被当成 0.94 的复读整条丢掉，
// 被点名追问的用户什么也没收到。
func TestReplyAuditSelfRepeatUsesItsOwnConfidence(t *testing.T) {
	spec := replyAuditDecisionSpec(replyAuditNeed{Loop: true, Density: &replyDensity{}})
	for _, tc := range []struct {
		name       string
		selfRepeat float64
		purpose    float64
		wantDrop   bool
		wantCounts bool
	}{
		{"borderline_self_repeat_is_kept", 0.60, 0.10, false, false},
		{"confident_self_repeat_is_dropped", 0.96, 0.10, true, true},
		{"borderline_purposeless_does_not_count", 0.10, 0.60, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := spec.RenderDecisionAnswers(map[string]llm.DecisionAnswer{
				"send_confidence":         {Kind: llm.DecisionScore, Score: 2, Confidence: 0.9},
				"reply_loop_meaningless":  {Kind: llm.DecisionNoul, Noul: 0.06},
				"reply_loop_automated_ai": {Kind: llm.DecisionNoul, Noul: 0.05},
				"reply_loop_self_repeat":  {Kind: llm.DecisionNoul, Noul: tc.selfRepeat},
				"reply_loop_purposeless":  {Kind: llm.DecisionNoul, Noul: tc.purpose},
			})
			if err != nil {
				t.Fatalf("渲染失败：%v", err)
			}
			decision, ok := parseProactiveReplyQualityDecision(rendered)
			if !ok {
				t.Fatalf("审核解析器读不回去：%s", rendered)
			}
			loop := decision.loopDecision()
			// 这里只看置信度门槛；复读要不要计数还取决于对方是不是机器人，按机器人算。
			loop.selfRepeatCounts = true
			if got := loop.selfRepeatDropsReply(); got != tc.wantDrop {
				t.Fatalf("selfRepeatDropsReply() = %v，want %v：%s", got, tc.wantDrop, rendered)
			}
			if got := loop.counts(); got != tc.wantCounts {
				t.Fatalf("counts() = %v，want %v：%s", got, tc.wantCounts, rendered)
			}
			if !strings.Contains(decision.ReplyLoopReason, "复读") {
				t.Fatalf("复读的判断要写进理由，复盘时才看得出是哪一题拦的：%q", decision.ReplyLoopReason)
			}
		})
	}
}

// 对话模型只写一个总置信度，复读沿用它，行为和以前一样。
func TestReplyAuditSelfRepeatFallsBackToSharedConfidence(t *testing.T) {
	decision, ok := parseProactiveReplyQualityDecision(selfRepeatVerdict(true, 0.95, "同一句晚安又说一遍"))
	if !ok || !decision.loopDecision().selfRepeatDropsReply() {
		t.Fatalf("没有单独置信度时应沿用 reply_loop_confidence：%#v", decision)
	}
	decision, _ = parseProactiveReplyQualityDecision(selfRepeatVerdict(true, 0.75, "拿不准"))
	if decision.loopDecision().selfRepeatDropsReply() {
		t.Fatalf("低置信的复读不该丢回复：%#v", decision)
	}
}
