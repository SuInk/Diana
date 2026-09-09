package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestParticipationThresholdConfigurationRoundTrip(t *testing.T) {
	for _, raw := range []string{
		`{"desire":75}`,
		`{"desire":75,"relevance_threshold":0,"substance_threshold":90}`,
		`{"desire":75,"relevance_threshold":80,"substance_threshold":40}`,
	} {
		var original ParticipationPreferences
		if err := json.Unmarshal([]byte(raw), &original); err != nil {
			t.Fatal(err)
		}
		cfg := BotConfig{Participation: &original}.WithDefaults()
		data, err := json.Marshal(PayloadFromConfig(cfg))
		if err != nil {
			t.Fatal(err)
		}
		var payload ConfigPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		restored := ConfigFromPayload(payload, BotConfig{}).participationPreferences()
		if restored.relevanceThreshold() != original.relevanceThreshold() || restored.substanceThreshold() != original.substanceThreshold() {
			t.Fatal("score thresholds changed during config round trip")
		}
		if original.RelevanceThreshold != nil {
			*cfg.Participation.RelevanceThreshold = 50
			if *original.RelevanceThreshold == 50 {
				t.Fatal("threshold pointer aliases input")
			}
		}
	}
	if (ParticipationPreferences{}).substanceThreshold() != 60 {
		t.Fatal("old config lost default")
	}
	low, high := -10, 110
	normalized := copyParticipation(&ParticipationPreferences{RelevanceThreshold: &low, SubstanceThreshold: &high})
	if *normalized.RelevanceThreshold != 0 || *normalized.SubstanceThreshold != 100 {
		t.Fatal("thresholds not clamped")
	}
}

func TestParticipationThresholdGroupOverridesAndPrompt(t *testing.T) {
	r90, s80, r40, s60 := 90, 80, 40, 60
	base := BotConfig{Participation: &ParticipationPreferences{Desire: 75, RelevanceThreshold: &r90, SubstanceThreshold: &s80}}
	r := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"custom":  {GroupID: "custom", Participation: &ParticipationPreferences{Desire: 75, RelevanceThreshold: &r40, SubstanceThreshold: &s60}},
		"inherit": {GroupID: "inherit"},
	}})
	for _, group := range []string{"custom", "inherit"} {
		cfg := r.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: group})
		prefs := cfg.participationPreferences()
		wantR, wantS := 90, 80
		if group == "custom" {
			wantR, wantS = 40, 60
		}
		if prefs.relevanceThreshold() != wantR || prefs.substanceThreshold() != wantS {
			t.Fatal("group thresholds not applied")
		}
		for _, want := range []string{"本轮相关度档位", "闲聊档位"} {
			if !strings.Contains(prefs.prompt(), want) {
				t.Fatalf("prompt missing %s", want)
			}
		}
		d := proactiveReplyDecision{ShouldReply: true, Category: "chat_in", Scores: testParticipationScores(100, wantS-1)}
		if d.allows(0, cfg.chatInSettings()) {
			t.Fatal("configured substance threshold ignored")
		}
		d.Scores = testParticipationScores(100, wantS)
		if !d.allows(0, cfg.chatInSettings()) {
			t.Fatal("configured substance boundary rejected")
		}
		d.Category, d.DirectedAtBot = "bot_related", true
		d.Scores = testParticipationScores(wantR-1, 100)
		if d.allows(0, cfg.chatInSettings()) {
			t.Fatal("configured relevance threshold ignored")
		}
		d.Scores = testParticipationScores(wantR, 0)
		if !d.allows(0, cfg.chatInSettings()) {
			t.Fatal("request incorrectly used substance threshold")
		}
	}
}

func testParticipationScores(relevance, substance int) *participationScores {
	return &participationScores{
		Relevance: participationDimensionScore{Score: &relevance, Reason: "是否在对机器人说话"},
		Substance: participationDimensionScore{Score: &substance, Reason: "回应是否有具体价值"},
	}
}

func TestParticipationScoresGateIndependently(t *testing.T) {
	for _, level := range []ChatInLevel{ChatInLevelLow, ChatInLevelMedium, ChatInLevelHigh, ChatInLevelMax} {
		settings := (BotConfig{ChatInLevel: level}).chatInSettings()
		for _, tc := range []struct {
			name, category       string
			relevance, substance int
			want                 bool
		}{
			{"sticker", "chat_in", 100, 20, false},
			{"below boundary", "chat_in", 100, 59, false},
			{"useful unrelated contribution", "chat_in", 0, 60, true},
			{"followup without new knowledge", "bot_related", 90, 10, true},
			{"misclassified unrelated message", "bot_related", 59, 100, false},
			{"public request", "needs_response", 0, 10, true},
		} {
			d := proactiveReplyDecision{ShouldReply: true, Category: tc.category, DirectedAtBot: tc.category == "bot_related", Scores: testParticipationScores(tc.relevance, tc.substance)}
			if got := d.allows(1, settings); got != tc.want {
				t.Fatalf("%s/%s allowed=%t want=%t", level, tc.name, got, tc.want)
			}
			d.ShouldReply = false
			if d.allows(0, settings) {
				t.Fatal("scores overrode silence")
			}
		}
		if (proactiveReplyDecision{ShouldReply: true, Category: "chat_in", Substantive: true}).allows(0, settings) {
			t.Fatal("legacy boolean bypassed numeric content gate")
		}
	}
}

func TestParticipationScoreValidationAndReason(t *testing.T) {
	for _, scores := range []string{
		`{}`,
		`{"relevance":{"score":50,"reason":"相关"}}`,
		`{"relevance":{"score":50,"reason":"相关"},"substance":{"score":101,"reason":"补充"}}`,
		`{"relevance":{"score":-1,"reason":"相关"},"substance":{"score":80,"reason":"补充"}}`,
		`{"relevance":{"score":50,"reason":"相关"},"substance":{"score":80.5,"reason":"补充"}}`,
		`{"relevance":{"score":50,"reason":"相关"},"substance":{"score":80,"reason":" "}}`,
	} {
		raw := fmt.Sprintf(`{"should_reply":true,"category":"chat_in","scores":%s,"reason":"接话"}`, scores)
		if _, ok := parseProactiveReplyDecision(raw); ok {
			t.Fatalf("invalid scores accepted: %s", scores)
		}
	}
	data, _ := json.Marshal(proactiveReplyDecision{ShouldReply: true, Category: "chat_in", Scores: testParticipationScores(90, 20), Reason: "适合接梗"})
	d, ok := parseProactiveReplyDecision(string(data))
	if !ok || *d.Scores.Relevance.Score != 90 || *d.Scores.Substance.Score != 20 {
		t.Fatal("score round trip lost values")
	}
	cfg := BotConfig{ChatInLevel: ChatInLevelHigh}
	reason := proactiveReplyDecisionReason(d, true, false, true, true, false, false, cfg, cfg.chatInSettings())
	for _, want := range []string{"未放行", "60", "相关度 90", "内容实质性 20"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason lacks %q: %s", want, reason)
		}
	}
}
