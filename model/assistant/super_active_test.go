package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestSuperActiveRoutingIgnoresOldSamplingAndCooldown(t *testing.T) {
	for _, category := range []string{"chat_in", "needs_response", "bot_related"} {
		t.Run(category, func(t *testing.T) {
			provider := &capturingLLMProvider{reply: fmt.Sprintf(`{"should_reply":true,"confidence":0.78,"category":%q,"target_message_id":"msg","directed_at_bot":true,"substantive":false}`, category)}
			r := NewRuntime(BotConfig{BotAccount: "42", ResponseMode: ResponseModeSuperActive, ProactiveReplyChance: 0.05, ProactiveReplyThreshold: 0.99}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "msg", RawMessage: "今天心情不错"}
			r.markChatInReplied(event)
			for i := 0; i < 10; i++ {
				routed, _, _, allowed := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}})
				if !allowed || !routed.proactiveReply {
					t.Fatalf("route rejected: %s", routed.routingReason)
				}
			}
		})
	}
}

func TestSuperActiveIntentAllowsSocialRepliesAndQuestions(t *testing.T) {
	cfg := BotConfig{ResponseMode: ResponseModeSuperActive}.WithDefaults()
	settings := cfg.chatInSettings()
	if !settings.Enabled || settings.Chance != 1 || settings.Cooldown != 0 {
		t.Fatalf("settings = %#v", settings)
	}
	for _, category := range []string{"chat_in", "needs_response", "bot_related"} {
		d := proactiveReplyDecision{ShouldReply: true, Confidence: 0.78, Category: category, DirectedAtBot: true}
		if !d.allows(0.9, settings) {
			t.Fatalf("super active rejected %s", category)
		}
		d.ShouldReply = false
		if d.allows(0.9, settings) {
			t.Fatalf("super active overrode silence for %s", category)
		}
	}
	for _, d := range []proactiveReplyDecision{
		{ShouldReply: true, Confidence: 0.9, Category: "none"},
		{ShouldReply: true, Confidence: 0.9, Category: "bot_related"},
		{ShouldReply: true, Confidence: -0.1, Category: "chat_in"},
		{ShouldReply: true, Confidence: 1.1, Category: "chat_in"},
	} {
		if d.allows(0.9, settings) {
			t.Fatalf("invalid decision passed: %#v", d)
		}
	}
	standard := BotConfig{ResponseMode: ResponseModeStandard}.WithDefaults()
	if (proactiveReplyDecision{ShouldReply: true, Confidence: 0.78, Category: "needs_response"}).allows(0.9, standard.chatInSettings()) {
		t.Fatal("standard mode threshold changed")
	}
}

func TestSuperActivePromptsAndQuality(t *testing.T) {
	cfg := BotConfig{ResponseMode: ResponseModeSuperActive}.WithDefaults()
	prompt := proactiveReplyRouterPromptForChatIn(defaultProactiveReplyRouterPrompt, cfg.chatInSettings(), false)
	if prompt != superActiveIntentPrompt {
		t.Fatal("super active must use its own intent policy")
	}
	if !strings.Contains(replyQualityPromptForConfig(cfg), "正常的寒暄") {
		t.Fatal("audit missing social reply policy")
	}
	r := &Runtime{}
	for _, chatIn := range []bool{true, false} {
		event := MessageEvent{chatInReply: chatIn}
		if err := r.proactiveQualityError(event, proactiveReplyQualityDecision{ShouldSend: true, Confidence: 0.78}, cfg); err != nil {
			t.Fatal(err)
		}
		if err := r.proactiveQualityError(event, proactiveReplyQualityDecision{ShouldSend: false, Confidence: 0.99}, cfg); err == nil {
			t.Fatal("explicit audit rejection must be preserved")
		}
	}
}

func TestSuperActiveGroupOverride(t *testing.T) {
	r := NewRuntime(BotConfig{ResponseMode: ResponseModeSuperActive}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"quiet": {GroupID: "quiet", ResponseMode: ResponseModeQuiet},
		"super": {GroupID: "super", ResponseMode: ResponseModeSuperActive},
	}})
	for _, group := range []string{"quiet", "super", "inherit"} {
		settings := r.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: group}).chatInSettings()
		if settings.SuperActive != (group != "quiet") {
			t.Fatalf("group %s settings = %#v", group, settings)
		}
	}
}
