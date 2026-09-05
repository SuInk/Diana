package assistant

import (
	"context"
	"testing"
)

func TestTelegramBotSemanticMentionDecision(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		want  bool
	}{
		{"implicit mention", `{"mentions_self":true}`, true},
		{"unrelated", `{"mentions_self":false}`, false},
		{"missing decision", `{}`, false},
		{"invalid decision", `not json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &capturingLLMProvider{reply: tc.reply}
			r := NewRuntime(BotConfig{BotAccount: "8888", GroupTriggers: []string{"Diana"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "g", UserID: "42", SenderIsBot: true}
			if !r.requiresTelegramBotMentionJudgment(event) {
				t.Fatal("bot semantic gate must default on")
			}
			if got := r.telegramBotMessageMentionsSelf(context.Background(), event, "你刚才那个建议能展开说说吗"); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTelegramBotSemanticGateScope(t *testing.T) {
	for _, tc := range []struct {
		name     string
		event    MessageEvent
		disabled bool
		want     bool
	}{
		{"bot group", MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, SenderIsBot: true}, false, true},
		{"human", MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup}, false, false},
		{"private", MessageEvent{Platform: PlatformTelegram, Kind: EventKindPrivate, SenderIsBot: true}, false, false},
		{"other platform", MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, SenderIsBot: true}, false, false},
		{"disabled", MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, SenderIsBot: true}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := BotConfig{}
			if tc.disabled {
				cfg.TelegramSuppressBotMessages = boolPointer(false)
			}
			r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			if got := r.requiresTelegramBotMentionJudgment(tc.event); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
