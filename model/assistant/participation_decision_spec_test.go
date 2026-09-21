// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestParticipationDecisionSpecRendersParsableRatings(t *testing.T) {
	spec := participationDecisionSpec()
	if err := spec.Validate(); err != nil {
		t.Fatalf("spec is invalid: %v", err)
	}
	raw, err := spec.RenderDecisionAnswers(map[string]llm.DecisionAnswer{
		"relevance": {Kind: llm.DecisionNoul, Noul: 0.88},
		"chat_in":   {Kind: llm.DecisionScore, Score: 3, Confidence: 0.64},
	})
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	ratings, err := parseParticipationRatings(raw)
	if err != nil {
		t.Fatalf("the rendered ratings did not parse: %v (%s)", err, raw)
	}
	if ratings.Relevance.Directed == nil || !*ratings.Relevance.Directed {
		t.Fatalf("expected directed=true, got %s", raw)
	}
	// 第四档就是 0.50 那个锚点。
	if ratings.ChatIn.Score == nil || *ratings.ChatIn.Score != 0.5 {
		t.Fatalf("expected the 0.50 anchor, got %s", raw)
	}
}

func TestParticipationChatInAnchorsStayInSyncWithTheLevels(t *testing.T) {
	if len(participationChatInLevels) != len(participationChatInLevelValues) {
		t.Fatalf("levels and their values drifted apart: %d vs %d", len(participationChatInLevels), len(participationChatInLevelValues))
	}
	for _, level := range participationChatInLevels[1:] {
		if !strings.Contains(participationChatInAnchors, level) {
			t.Fatalf("anchor line lost %q", level)
		}
	}
	if !strings.Contains(participationScorePrompt, participationChatInAnchors) {
		t.Fatal("the score prompt no longer carries the anchors")
	}
	if !strings.Contains(participationScorePrompt, participationRelevanceTrue) {
		t.Fatal("the score prompt no longer carries the relevance criteria")
	}
}

func TestProactiveReplyDecisionSpecRendersParsableDecision(t *testing.T) {
	candidates := []proactiveReplyCandidate{
		{Event: MessageEvent{MessageID: "101"}, Text: "有人知道这个报错吗"},
		{Event: MessageEvent{MessageID: "102"}, Text: "补一张截图"},
	}
	spec := proactiveReplyDecisionSpec(candidates)
	if err := spec.Validate(); err != nil {
		t.Fatalf("spec is invalid: %v", err)
	}
	raw, err := spec.RenderDecisionAnswers(map[string]llm.DecisionAnswer{
		"should_reply":      {Kind: llm.DecisionNoul, Noul: 0.93},
		"category":          {Kind: llm.DecisionChoice, Choice: "needs_response", Confidence: 0.8},
		"directed_at_bot":   {Kind: llm.DecisionNoul, Noul: 0.2},
		"answerable":        {Kind: llm.DecisionNoul, Noul: 0.9},
		"substantive":       {Kind: llm.DecisionNoul, Noul: 0.85},
		"requests_response": {Kind: llm.DecisionNoul, Noul: 0.95},
		"blocker":           {Kind: llm.DecisionChoice, Choice: proactiveBlockerNone, Confidence: 0.9},
		"target_message_id": {Kind: llm.DecisionChoice, Choice: "102", Confidence: 0.7},
		"turn_101":          {Kind: llm.DecisionNoul, Noul: 0.8},
		"turn_102":          {Kind: llm.DecisionNoul, Noul: 0.9},
	})
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	decision, parsed := parseProactiveReplyDecision(raw)
	if !parsed {
		t.Fatalf("the rendered decision did not parse: %s", raw)
	}
	if !decision.ShouldReply || decision.Category != "needs_response" {
		t.Fatalf("unexpected decision: %+v (%s)", decision, raw)
	}
	if decision.TargetMessageID != "102" {
		t.Fatalf("expected the chosen target, got %s", raw)
	}
	if len(decision.TurnMessageIDs) != 2 {
		t.Fatalf("expected both turn messages, got %s", raw)
	}
	if strings.TrimSpace(decision.Reason) == "" {
		t.Fatalf("expected a synthesized reason, got %s", raw)
	}
	if decision.Confidence < 0.9 {
		t.Fatalf("expected the noul probability as confidence, got %s", raw)
	}
}

func TestProactiveReplyDecisionSpecLetsTheModelPickNoTarget(t *testing.T) {
	spec := proactiveReplyDecisionSpec([]proactiveReplyCandidate{{Event: MessageEvent{MessageID: "101"}, Text: "草"}})
	raw, err := spec.RenderDecisionAnswers(map[string]llm.DecisionAnswer{
		"should_reply":      {Kind: llm.DecisionNoul, Noul: 0.04},
		"category":          {Kind: llm.DecisionChoice, Choice: "none", Confidence: 0.9},
		"directed_at_bot":   {Kind: llm.DecisionNoul, Noul: 0.02},
		"answerable":        {Kind: llm.DecisionNoul, Noul: 0.3},
		"substantive":       {Kind: llm.DecisionNoul, Noul: 0.1},
		"requests_response": {Kind: llm.DecisionNoul, Noul: 0.05},
		"blocker":           {Kind: llm.DecisionChoice, Choice: proactiveBlockerLowValue, Confidence: 0.88},
		"target_message_id": {Kind: llm.DecisionChoice, Choice: proactiveReplyNoTarget, Confidence: 0.9},
		"turn_101":          {Kind: llm.DecisionNoul, Noul: 0.1},
	})
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	decision, parsed := parseProactiveReplyDecision(raw)
	if !parsed {
		t.Fatalf("the rendered decision did not parse: %s", raw)
	}
	if decision.ShouldReply || decision.TargetMessageID != "" || len(decision.TurnMessageIDs) != 0 {
		t.Fatalf("expected a silent decision without a target: %s", raw)
	}
	if decision.Blocker != proactiveBlockerLowValue {
		t.Fatalf("expected the blocker to survive, got %s", raw)
	}
}

// 路由请求必须带着题目表：绑对话模型时它是死重量，绑判断模型时它是唯一的契约来源。
func TestProactiveReplyRouteCarriesTheDecisionSpec(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"should_reply":false,"confidence":0.9,"category":"none","blocker":"low_value","reason":"只是附和"}`,
	}}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	candidates := []proactiveReplyCandidate{
		{Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1", SenderName: "Alice"}, Text: "有人知道这个报错吗"},
		{Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-2", MessageID: "message-2", SenderName: "Bob"}, Text: "我先去吃饭了"},
	}
	runtime.routeProactiveReplyBatch(context.Background(), candidates)
	if len(provider.requests) != 1 {
		t.Fatalf("router calls = %d, want 1", len(provider.requests))
	}
	spec := provider.requests[0].Decision
	if spec == nil {
		t.Fatal("the route request carried no decision spec")
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("spec is invalid: %v", err)
	}
	keys := map[string]bool{}
	for _, question := range spec.Questions {
		keys[question.Key] = true
	}
	// 当前配置走的是评分契约，请求里带的就该是评分那两道题。
	for _, want := range []string{"relevance", "chat_in"} {
		if !keys[want] {
			t.Fatalf("decision spec is missing %q: %v", want, keys)
		}
	}
}
