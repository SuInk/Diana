package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestAssistantModeIntentPolicy(t *testing.T) {
	cfg := BotConfig{ResponseMode: ResponseModeAssistant, ReplyStyle: ReplyStyleGentle, NaturalInterjectionEnabled: boolPointer(true)}.WithDefaults()
	settings := cfg.chatInSettings()
	if !settings.Assistant || !settings.Enabled || settings.Natural || cfg.ReplyStyle != ReplyStyleGentle {
		t.Fatalf("assistant settings = %#v", settings)
	}
	for _, tc := range []struct {
		category                string
		request, directed, want bool
	}{
		{"needs_response", true, false, true},
		{"bot_related", true, true, true},
		{"bot_related", false, true, true},
		{"bot_related", true, false, false},
		{"chat_in", true, false, false},
		{"none", false, false, false},
	} {
		d := proactiveReplyDecision{ShouldReply: true, Confidence: 0.8, Category: tc.category, RequestsResponse: tc.request, DirectedAtBot: tc.directed, Substantive: true}
		if got := d.allows(0.99, settings); got != tc.want {
			t.Fatalf("decision %#v allowed=%v", d, got)
		}
	}
	prompt := proactiveReplyRouterPromptForChatIn(defaultProactiveReplyRouterPrompt, settings, true)
	if !strings.Contains(prompt, assistantIntentPrompt) || !strings.Contains(prompt, socialReplyGuard) {
		t.Fatal("assistant mode must retain configured social replies")
	}
	restored := ConfigFromPayload(PayloadFromConfig(cfg), BotConfig{})
	if restored.ResponseMode != ResponseModeAssistant || !restored.chatInSettings().Assistant {
		t.Fatal("assistant mode lost in config round trip")
	}
}

func TestAssistantEvidenceLedgerRespectsConfiguredPolicy(t *testing.T) {
	plugins := NewDefaultPluginManager()
	if _, err := plugins.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingEvidenceLedger: false}); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(BotConfig{ResponseMode: ResponseModeAssistant}, nilChannel{}, plugins, nil, nil, nil, nil)
	if !r.evidenceLedgerAdvisory(MessageEvent{}) {
		t.Fatal("assistant mode must not override evidence settings")
	}
	r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"standard": {GroupID: "standard", ResponseMode: ResponseModeStandard},
	}})
	if !r.evidenceLedgerAdvisory(MessageEvent{Kind: EventKindGroup, GroupID: "standard"}) {
		t.Fatal("other modes must retain the configured evidence policy")
	}
}

func TestAssistantModeDoesNotInjectSilencePolicy(t *testing.T) {
	r := NewRuntime(BotConfig{ResponseMode: ResponseModeAssistant}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	prompt := r.systemPrompt(MessageEvent{Kind: EventKindPrivate, UserID: "u"}, nil)
	if strings.Contains(prompt, "无论是否被直接请求") || strings.Contains(prompt, "运行时会静默处理且不累计拒答") {
		t.Fatal("assistant mode must not force direct replies to be silent")
	}
}

func TestAssistantModeCanChatAtLowDesire(t *testing.T) {
	settings := BotConfig{ResponseMode: ResponseModeAssistant}.WithDefaults().chatInSettings()
	active := BotConfig{ResponseMode: ResponseModeActive}.WithDefaults().chatInSettings()
	if settings.Level != ChatInLevelLow || settings.Chance >= active.Chance || settings.Cooldown <= active.Cooldown {
		t.Fatalf("assistant=%#v active=%#v", settings, active)
	}
	d := proactiveReplyDecision{ShouldReply: true, Confidence: 0.99, Category: "chat_in", Substantive: true}
	if !d.allows(0.7, settings) {
		t.Fatal("assistant must allow contextual chat")
	}
	d.Substantive = false
	if d.allows(0.7, settings) {
		t.Fatal("assistant must not send empty filler")
	}
	d.Substantive = true
	d.Confidence = 0.8
	if d.allows(0.7, settings) {
		t.Fatal("help threshold must not lower casual chat threshold")
	}
}

func TestAssistantModeRoutesPublicHelpAndGroupOverride(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"should_reply":true,"confidence":0.8,"category":"needs_response","requests_response":true,"target_message_id":"help"}`}
	r := NewRuntime(BotConfig{BotAccount: "42", ResponseMode: ResponseModeSuperActive, ProactiveReplyChance: 0.05, ProactiveReplyThreshold: 0.99}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"help-group": {GroupID: "help-group", ResponseMode: ResponseModeAssistant},
	}})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "help-group", UserID: "u", MessageID: "help", RawMessage: "这个编译错误怎么解决"}
	settings := r.effectiveConfigForEvent(event).chatInSettings()
	if !settings.Assistant || settings.SuperActive {
		t.Fatalf("group override = %#v", settings)
	}
	routed, _, _, allowed := r.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: event.RawMessage}})
	if !allowed || !routed.proactiveReply || routed.chatInReply {
		t.Fatalf("public help blocked: %s", routed.routingReason)
	}
	if !strings.Contains(provider.requestSnapshot().Messages[0].Content, assistantIntentPrompt) {
		t.Fatal("routing request missing assistant policy")
	}
}
