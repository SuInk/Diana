package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func scoredParticipationJSON(category string, values ...int) string {
	scores := map[string]any{}
	for i, dimension := range participationDimensions {
		scores[dimension.Key] = map[string]any{"score": values[i], "reason": "测试理由"}
	}
	encoded, _ := json.Marshal(map[string]any{"scores": scores, "category": category, "target_message_id": "m", "turn_message_ids": []string{"m"}})
	return string(encoded)
}

func TestParticipationScoresDetermineDecision(t *testing.T) {
	settings := BotConfig{Participation: &ParticipationPreferences{Desire: 100}}.chatInSettings()
	for _, tc := range []struct {
		values  []int
		average float64
		allowed bool
	}{
		{[]int{59, 60, 60, 60, 60}, 59.8, false},
		{[]int{20, 40, 60, 80, 100}, 60, true},
		{[]int{100, 100, 100, 100, 100}, 100, true},
		{[]int{0, 0, 0, 0, 0}, 0, false},
	} {
		raw := scoredParticipationJSON("chat_in", tc.values...)
		// Deprecated model booleans and confidence cannot override valid scores.
		raw = strings.TrimSuffix(raw, "}") + `,"should_reply":false,"confidence":0.01}`
		d, ok := parseProactiveReplyDecision(raw)
		if !ok || d.Scores.average() != tc.average || d.allows(.99, settings) != tc.allowed || d.ShouldReply != tc.allowed {
			t.Fatalf("raw=%s decision=%+v parsed=%t", raw, d, ok)
		}
	}
}

func TestParticipationScoresRejectIncompleteOrInvalidValues(t *testing.T) {
	base := scoredParticipationJSON("chat_in", 80, 80, 80, 80, 80)
	for _, raw := range []string{
		strings.Replace(base, `"score":80`, `"score":null`, 1),
		strings.Replace(base, `"score":80`, `"score":101`, 1),
		strings.Replace(base, `"score":80`, `"score":-1`, 1),
		strings.Replace(base, `"score":80`, `"score":80.5`, 1),
		strings.Replace(base, `"desire"`, `"unexpected"`, 1),
		strings.Replace(base, `"reason":"测试理由"`, `"reason":""`, 1),
		`{"scores":null,"category":"chat_in","should_reply":true,"confidence":1}`,
	} {
		if _, ok := parseProactiveReplyDecision(raw); ok {
			t.Fatalf("accepted malformed score: %s", raw)
		}
	}
}

func TestParticipationScoreRoutingAndCooldown(t *testing.T) {
	provider := &capturingLLMProvider{reply: scoredParticipationJSON("chat_in", 80, 90, 50, 70, 85)}
	logs := &captureAppLogs{}
	r := NewRuntime(BotConfig{Participation: &ParticipationPreferences{Desire: 100, CooldownSeconds: 30}, ProactiveReplyChance: .00001, ProactiveReplyThreshold: 1}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	r.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "g", UserID: "u", MessageID: "m", RawMessage: "茶喝完了"}
	route := func(want bool) {
		t.Helper()
		result, _, _, allowed := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}})
		if allowed != want || !strings.Contains(result.routingReason, "发言评分 75.0/100") {
			t.Fatalf("allowed=%t reason=%s", allowed, result.routingReason)
		}
	}
	route(true)
	metadata := logs.entries[0].Metadata
	if metadata["scores"] == nil || metadata["reply_score"] != 75.0 || metadata["confidence"] != nil || metadata["should_reply"] != nil {
		t.Fatalf("score log: %+v", metadata)
	}
	r.markChatInReplied(event)
	route(false)
	provider.reply = scoredParticipationJSON("needs_response", 80, 90, 50, 70, 85)
	route(true)
}
