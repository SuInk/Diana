// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderDecisionAnswersFillsNestedContract(t *testing.T) {
	spec := DecisionSpec{Questions: []DecisionQuestion{
		{
			Key:        "relevance",
			Kind:       DecisionNoul,
			Label:      "在跟机器人说话",
			Path:       "relevance.directed",
			ReasonPath: "relevance.reason",
		},
		{
			Key:         "chat_in",
			Kind:        DecisionScore,
			Label:       "闲聊适合度",
			Levels:      []string{"a", "b", "c", "d", "e", "f"},
			LevelValues: []float64{0, 0.10, 0.30, 0.50, 0.70, 0.90},
			Max:         0.9,
			Path:        "chat_in.score",
			ReasonPath:  "chat_in.reason",
		},
	}}
	raw, err := spec.RenderDecisionAnswers(map[string]DecisionAnswer{
		"relevance": {Kind: DecisionNoul, Noul: 0.82},
		"chat_in":   {Kind: DecisionScore, Score: 2.5, Confidence: 0.6},
	})
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	var decoded struct {
		Relevance struct {
			Directed *bool  `json:"directed"`
			Reason   string `json:"reason"`
		} `json:"relevance"`
		ChatIn struct {
			Score  *float64 `json:"score"`
			Reason string   `json:"reason"`
		} `json:"chat_in"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("rendered output is not valid JSON: %v (%s)", err, raw)
	}
	if decoded.Relevance.Directed == nil || !*decoded.Relevance.Directed {
		t.Fatalf("expected directed=true, got %s", raw)
	}
	if decoded.Relevance.Reason == "" || decoded.ChatIn.Reason == "" {
		t.Fatalf("expected synthesized reasons, got %s", raw)
	}
	// 2.5 落在 0.30 和 0.50 两档正中间。
	if decoded.ChatIn.Score == nil || *decoded.ChatIn.Score != 0.4 {
		t.Fatalf("expected chat_in score 0.4, got %s", raw)
	}
}

func TestRenderDecisionAnswersMultiSelectAndEmptyChoice(t *testing.T) {
	spec := DecisionSpec{Questions: []DecisionQuestion{
		{
			Key:  "target_message_id",
			Kind: DecisionChoice,
			Options: []DecisionOption{
				{Value: "101"},
				{Value: "none"},
			},
			EmptyOption: "none",
			Path:        "target_message_id",
		},
		{Key: "turn_101", Kind: DecisionNoul, Path: "turn_message_ids", AppendValue: "101"},
		{Key: "turn_102", Kind: DecisionNoul, Path: "turn_message_ids", AppendValue: "102"},
	}}
	raw, err := spec.RenderDecisionAnswers(map[string]DecisionAnswer{
		"target_message_id": {Kind: DecisionChoice, Choice: "none"},
		"turn_101":          {Kind: DecisionNoul, Noul: 0.9},
		"turn_102":          {Kind: DecisionNoul, Noul: 0.2},
	})
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	var decoded struct {
		TargetMessageID string   `json:"target_message_id"`
		TurnMessageIDs  []string `json:"turn_message_ids"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("rendered output is not valid JSON: %v (%s)", err, raw)
	}
	if decoded.TargetMessageID != "" {
		t.Fatalf("expected the empty option to render an empty target, got %s", raw)
	}
	if len(decoded.TurnMessageIDs) != 1 || decoded.TurnMessageIDs[0] != "101" {
		t.Fatalf("expected only the accepted candidate, got %s", raw)
	}
}

func TestRenderDecisionAnswersNoulConfidenceFollowsTheVerdict(t *testing.T) {
	spec := DecisionSpec{Questions: []DecisionQuestion{
		{Key: "should_reply", Kind: DecisionNoul, Path: "should_reply", ConfidencePath: "confidence", ReasonPath: "reason"},
	}}
	raw, err := spec.RenderDecisionAnswers(map[string]DecisionAnswer{
		"should_reply": {Kind: DecisionNoul, Noul: 0.12},
	})
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	var decoded struct {
		ShouldReply bool    `json:"should_reply"`
		Confidence  float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("rendered output is not valid JSON: %v (%s)", err, raw)
	}
	if decoded.ShouldReply {
		t.Fatalf("expected should_reply=false, got %s", raw)
	}
	if decoded.Confidence != 0.88 {
		t.Fatalf("expected the confidence of the negative verdict, got %s", raw)
	}
}

func TestRenderDecisionAnswersRejectsMissingAnswer(t *testing.T) {
	spec := DecisionSpec{Questions: []DecisionQuestion{{Key: "a", Kind: DecisionNoul, Path: "a"}}}
	if _, err := spec.RenderDecisionAnswers(map[string]DecisionAnswer{}); err == nil {
		t.Fatal("expected a missing answer to fail")
	}
}

func TestDecisionSpecValidate(t *testing.T) {
	cases := map[string]DecisionSpec{
		"empty":       {},
		"no path":     {Questions: []DecisionQuestion{{Key: "a", Kind: DecisionNoul}}},
		"few options": {Questions: []DecisionQuestion{{Key: "a", Kind: DecisionChoice, Path: "a", Options: []DecisionOption{{Value: "x"}}}}},
		"few levels":  {Questions: []DecisionQuestion{{Key: "a", Kind: DecisionScore, Path: "a", Levels: []string{"x"}}}},
		"duplicate":   {Questions: []DecisionQuestion{{Key: "a", Kind: DecisionNoul, Path: "a"}, {Key: "a", Kind: DecisionNoul, Path: "b"}}},
	}
	for name, spec := range cases {
		if err := spec.Validate(); err == nil {
			t.Fatalf("%s: expected validation to fail", name)
		}
	}
}

// TestRenderDecisionAnswersNoulHonoursThreshold 阈值抬到 0.7 后，0.6 这种「拿不准」
// 要落到否那边；零值仍按 0.5 切，别的题目不受影响。
func TestRenderDecisionAnswersNoulHonoursThreshold(t *testing.T) {
	spec := DecisionSpec{Questions: []DecisionQuestion{
		{Key: "strict", Kind: DecisionNoul, Path: "strict", Threshold: 0.7, ReasonPath: "strict_reason"},
		{Key: "plain", Kind: DecisionNoul, Path: "plain"},
	}}
	if err := spec.Validate(); err != nil {
		t.Fatalf("spec is invalid: %v", err)
	}
	raw, err := spec.RenderDecisionAnswers(map[string]DecisionAnswer{
		"strict": {Kind: DecisionNoul, Noul: 0.6},
		"plain":  {Kind: DecisionNoul, Noul: 0.6},
	})
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	var decoded struct {
		Strict       bool   `json:"strict"`
		StrictReason string `json:"strict_reason"`
		Plain        bool   `json:"plain"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("rendered output is not valid JSON: %v (%s)", err, raw)
	}
	if decoded.Strict || !decoded.Plain {
		t.Fatalf("expected strict=false and plain=true at 0.6, got %s", raw)
	}
	// 理由里仍记原始概率，复盘时看得出这一条是差一点还是差很多。
	if !strings.Contains(decoded.StrictReason, "0.60") {
		t.Fatalf("reason should keep the raw probability, got %q", decoded.StrictReason)
	}
	bad := DecisionSpec{Questions: []DecisionQuestion{{Key: "a", Kind: DecisionNoul, Path: "a", Threshold: 1}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("a threshold of 1 can never be reached and must be rejected")
	}
}
