package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestParticipationUsesScoresAndBooleanDecision(t *testing.T) {
	for _, level := range []ChatInLevel{ChatInLevelLow, ChatInLevelMedium, ChatInLevelHigh, ChatInLevelMax} {
		settings := (BotConfig{ChatInLevel: level}).chatInSettings()
		for _, want := range []bool{false, true} {
			raw := fmt.Sprintf(`{"should_reply":%t,"category":"chat_in","scores":{"relevance":{"score":30,"reason":"群友间的讨论"},"substance":{"score":80,"reason":"有具体信息补充"}},"target_message_id":"m","reason":"结合上下文判断"}`, want)
			d, ok := parseProactiveReplyDecision(raw)
			if !ok || d.allows(.99, settings) != want {
				t.Fatalf("level=%s decision=%+v parsed=%t", level, d, ok)
			}
		}
	}
	for _, raw := range []string{
		`{"scores":{"desire":{"score":100}},"category":"chat_in"}`,
		`{"should_reply":"yes","category":"chat_in","reason":"可以接话"}`,
		`{"should_reply":true,"reason":"可以接话"}`,
		`{"should_reply":true,"category":"chat_in"}`,
	} {
		if _, ok := parseProactiveReplyDecision(raw); ok {
			t.Fatalf("invalid decision accepted: %s", raw)
		}
	}
	// Leftover score fields cannot overrule an explicit decision to stay quiet.
	d, ok := parseProactiveReplyDecision(`{"should_reply":false,"category":"chat_in","reason":"已经回答过","scores":{"desire":{"score":100}}}`)
	if ok && d.ShouldReply {
		t.Fatal("scores overrode silence")
	}
}

func TestParticipationDecisionRoutingAndCooldown(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"should_reply":true,"category":"chat_in","scores":{"relevance":{"score":30,"reason":"群友间的讨论"},"substance":{"score":80,"reason":"有具体信息补充"}},"target_message_id":"m","reason":"有合适的补充"}`}
	logs := &captureAppLogs{}
	r := NewRuntime(BotConfig{Participation: &ParticipationPreferences{Desire: 100, CooldownSeconds: 30}, ProactiveReplyChance: .00001, ProactiveReplyThreshold: 1}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	r.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "g", UserID: "u", MessageID: "m", RawMessage: "茶喝完了"}
	route := func(want bool) {
		t.Helper()
		result, _, _, allowed := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}})
		if allowed != want {
			t.Fatalf("allowed=%t reason=%s", allowed, result.routingReason)
		}
	}
	route(true)
	metadata := logs.entries[0].Metadata
	if metadata["scores"] == nil || metadata["reply_score"] != nil || metadata["reply_level"] != ChatInLevelMax || metadata["should_reply"] != true {
		t.Fatalf("decision log: %+v", metadata)
	}
	scores, ok := metadata["scores"].(*participationScores)
	if !ok || !scores.valid() || *scores.Relevance.Score != 30 || *scores.Substance.Score != 80 || metadata["substance_threshold"] != 60 {
		t.Fatalf("numeric scores missing from log: %+v", metadata)
	}
	original := provider.reply
	provider.reply = `{"should_reply":true,"category":"chat_in","scores":{"relevance":{"score":95,"reason":"承接机器人话题"},"substance":{"score":20,"reason":"只是描述表情包"}},"target_message_id":"m","reason":"可以接梗"}`
	route(false)
	provider.reply = original
	r.markChatInReplied(event)
	route(false)
	for _, category := range []string{"needs_response", "bot_related"} {
		provider.reply = fmt.Sprintf(`{"should_reply":true,"category":%q,"target_message_id":"m","reason":"需要回应请求"}`, category)
		route(true)
	}
	provider.reply = `{"should_reply":false,"category":"chat_in","scores":{"relevance":{"score":30,"reason":"群友间的讨论"},"substance":{"score":80,"reason":"有具体信息补充"}},"target_message_id":"m","reason":"用户要求停止"}`
	route(false)
}

func TestParticipationLevelRulesReplaceDimensions(t *testing.T) {
	rules := map[ChatInLevel]string{
		ChatInLevelLow: "偶尔补充重要信息", ChatInLevelMedium: "答完收住",
		ChatInLevelHigh: "主动参与闲聊", ChatInLevelMax: "不重复、不硬插",
	}
	for level, want := range rules {
		prompt := (BotConfig{ChatInLevel: level}).participationPreferences().prompt()
		if !strings.Contains(prompt, want) || !strings.Contains(prompt, `"should_reply":true`) {
			t.Fatalf("level=%s rule missing", level)
		}
		for _, banned := range []string{"主动参与=", "闲聊接话=", "算术平均"} {
			if strings.Contains(prompt, banned) {
				t.Fatalf("level=%s still requests %q", level, banned)
			}
		}
	}
	off := (BotConfig{ChatInLevel: ChatInLevelOff}).chatInSettings()
	if (proactiveReplyDecision{ShouldReply: true, Category: "chat_in"}).allows(0, off) {
		t.Fatal("legacy disabled participation was reopened")
	}
	if !(proactiveReplyDecision{ShouldReply: true, Category: "bot_related", DirectedAtBot: true}).allows(1, off) {
		t.Fatal("disabled participation blocked a directed request")
	}
}
