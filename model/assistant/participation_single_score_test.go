package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func floatPtr(v float64) *float64 { return &v }

func TestAnswerabilityIsSharedGate(t *testing.T) {
	for _, tc := range []struct {
		rel, chat, quality float64
		cool, want         bool
	}{
		{1, 0, 0.49, true, false}, {0, 1, 0.49, true, false}, {1, 1, 0.49, true, false},
		{1, 0, 0.50, false, true}, {0, 1, 0.50, true, true}, {0, 1, 1, false, false},
		{0, 0, 1, true, false},
	} {
		p := ParticipationPreferences{RelevanceLevel: "medium", ChatLevel: "medium", AnswerabilityLevel: "medium"}
		v := testRatings(tc.rel, tc.chat)
		v.Answerability.Score = floatPtr(tc.quality)
		got, _ := p.ratingsAllow(v, tc.cool)
		if got != tc.want {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
	p := ParticipationPreferences{RelevanceLevel: "always", ChatLevel: "off"}
	v := testRatings(0.1, 0)
	v.Answerability.Score = floatPtr(0.1)
	if got, _ := p.ratingsAllow(v, true); got {
		t.Fatal("always relevance bypassed quality gate")
	}
	p.AnswerabilityLevel = "off"
	if got, _ := p.ratingsAllow(v, true); !got {
		t.Fatal("disabled quality gate still blocks")
	}
	data, _ := json.Marshal(PayloadFromConfig(BotConfig{Participation: &p}))
	var payload ConfigPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if ConfigFromPayload(payload, BotConfig{}).participationPreferences().answerabilityLevel() != "off" {
		t.Fatal("quality setting lost")
	}
}
func testRatings(r, c float64) participationRatings {
	return participationRatings{participationRating{&r, "相关度原因"}, participationRating{&c, "闲聊原因"}, participationRating{floatPtr(1), "可回答"}}
}
func TestParticipationRatingsORAndOff(t *testing.T) {
	for _, tc := range []struct {
		a, b       string
		r, c       float64
		cool, want bool
	}{
		{"medium", "medium", 0.8, 0.1, true, true}, {"medium", "medium", 0.1, 0.8, true, true},
		{"medium", "medium", 0.49, 0.49, true, false}, {"off", "medium", 1, 0.1, true, false},
		{"medium", "off", 0.1, 1, true, false}, {"off", "off", 1, 1, true, false},
		{"always", "off", 0.01, 0.01, false, true}, {"off", "always", 0.01, 0.01, true, true},
		{"off", "always", 0.01, 0.01, false, false}, {"always", "always", 0, 0, true, false},
		{"high", "medium", 0.3, 0, true, true}, {"low", "off", 0.69, 1, true, false},
	} {
		p := ParticipationPreferences{RelevanceLevel: tc.a, ChatLevel: tc.b}
		got, _ := p.ratingsAllow(testRatings(tc.r, tc.c), tc.cool)
		if got != tc.want {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
}
func TestParticipationRatingsProtocol(t *testing.T) {
	good := `{"relevance":{"score":0.25,"reason":"未直接对机器人说话"},"answerability":{"score":0.8,"reason":"有意义的回复"},"chat_in":{"score":0.80,"reason":"自然接梗"}}`
	if _, err := parseParticipationRatings(good); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`true`, `0.8`, `{"score":0.8,"reason":"x"}`, strings.Replace(good, "0.25", "1.25", 1), strings.Replace(good, "0.25", `"0.25"`, 1), good + " {}", good + " junk", strings.Replace(good, `"relevance":`, `"should_reply":true,"relevance":`, 1)} {
		if _, err := parseParticipationRatings(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestParticipationSevenLevelBoundaries(t *testing.T) {
	for level, threshold := range map[string]float64{"minimal": 0.90, "low": 0.70, "medium": 0.50, "high": 0.30, "extreme": 0.10} {
		if !ratingPasses(threshold, level) || ratingPasses(threshold-0.01, level) {
			t.Fatalf("boundary %s %.2f", level, threshold)
		}
		p := ParticipationPreferences{RelevanceLevel: level, ChatLevel: level}
		r, c := p.ratingLevels()
		if r != level || c != level {
			t.Fatalf("lost %s", level)
		}
	}
	if ratingPasses(1, "off") || !ratingPasses(0, "always") || ratingPasses(1, "invalid") {
		t.Fatal("off/always/invalid mismatch")
	}
}
func TestParticipationRatingsRouting(t *testing.T) {
	provider := &capturingLLMProvider{}
	r := NewRuntime(BotConfig{Participation: &ParticipationPreferences{RelevanceLevel: "medium", ChatLevel: "high", CooldownSeconds: 30}}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m", RawMessage: "接着聊"}
	for _, tc := range []struct {
		rel, chat float64
		want      bool
	}{{0.5, 0.1, true}, {0.1, 0.3, true}, {0.1, 0.29, false}, {0, 0, false}} {
		provider.reply = fmt.Sprintf(`{"relevance":{"score":%.2f,"reason":"相关度"},"answerability":{"score":0.8,"reason":"有意义的回复"},"chat_in":{"score":%.2f,"reason":"闲聊"}}`, tc.rel, tc.chat)
		_, _, _, got := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}})
		if got != tc.want {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
	r.markChatInReplied(event)
	provider.reply = `{"relevance":{"score":0.9,"reason":"直接接话"},"answerability":{"score":0.8,"reason":"有意义的回复"},"chat_in":{"score":0.1,"reason":"不适合闲聊"}}`
	_, _, _, got := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event}})
	if !got {
		t.Fatal("chat cooldown blocked relevance branch")
	}
}
func TestParticipationRatingsPromptAndConfig(t *testing.T) {
	for _, level := range []string{"off", "low", "medium", "high", "always"} {
		p := ParticipationPreferences{RelevanceLevel: level, ChatLevel: level}
		prompt := p.prompt()
		for _, banned := range []string{"should_reply", "true", "false", "substance", "confidence", "category"} {
			if strings.Contains(prompt, banned) {
				t.Fatalf("prompt contains %s", banned)
			}
		}
		if !strings.Contains(prompt, level) {
			t.Fatal("level missing")
		}
		encoded, _ := json.Marshal(PayloadFromConfig(BotConfig{Participation: &p}))
		var payload ConfigPayload
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatal(err)
		}
		restored := ConfigFromPayload(payload, BotConfig{}).participationPreferences()
		a, b := restored.ratingLevels()
		if a != level || b != level {
			t.Fatalf("lost levels %s %s", a, b)
		}
	}
}
