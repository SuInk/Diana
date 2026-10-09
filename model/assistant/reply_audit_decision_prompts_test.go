// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"reflect"
	"testing"
)

func TestReplyAuditDecisionSpecDefaultsUnchanged(t *testing.T) {
	need := replyAuditNeed{Quality: true, AccountSafety: true, Loop: true, Density: &replyDensity{}, Closing: true, Fatigue: true}
	cfg := BotConfig{PromptOverrides: PromptOverrides{"audit.quality": "x"}}
	if got, want := replyAuditDecisionSpecForConfig(need, cfg), replyAuditDecisionSpec(need); !reflect.DeepEqual(got, want) {
		t.Fatalf("判断表默认文字被改写了")
	}
}

func TestReplyAuditDecisionSpecAppliesOverrides(t *testing.T) {
	cfg := BotConfig{PromptOverrides: PromptOverrides{
		"audit.decision.count_refusal":        "新说明",
		"audit.decision.count_refusal.true":   "新判是",
		"audit.decision.account_risk.options": "politics：只看台海\nbogus：忽略\nexplicit 没有冒号",
	}}
	spec := replyAuditDecisionSpecForConfig(replyAuditNeed{AccountSafety: true}, cfg)
	for _, q := range spec.Questions {
		switch q.Key {
		case "count_refusal":
			if q.Instructions != "新说明" || q.TrueCriteria != "新判是" || q.FalseCriteria == "" {
				t.Fatalf("count_refusal = %+v", q)
			}
		case "account_risk":
			if q.Options[1].Description != "只看台海" || q.Options[2].Description != "露骨性内容" || len(q.Options) != 4 {
				t.Fatalf("options = %+v", q.Options)
			}
		}
	}
}
