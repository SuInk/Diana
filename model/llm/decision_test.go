// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"encoding/json"
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
