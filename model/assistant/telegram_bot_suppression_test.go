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

// 被标记成机器人的账号明确 @ 了本机时，不再交给语义判定：判据就在 event 上。
// 线上那条以「@Diana（3129583166）」开头的群消息被判成「没提到你」整条丢掉，
// 走的正是这条路径，而且白花了一次模型调用。
func TestMarkedBotExplicitMentionSkipsJudgment(t *testing.T) {
	cfg := BotConfig{BotAccount: "3129583166", MarkedBotIDs: []string{"160867498"}}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		t.Fatal("显式 @ 不该再问模型")
		return nil, nil
	})
	base := MessageEvent{
		Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "791503570",
		UserID: "160867498", SelfID: "3129583166", MessageID: "m1",
	}

	atSelf := base
	atSelf.Segments = []MessageSegment{
		{Type: "at", Data: map[string]string{"qq": "3129583166"}},
		{Type: "text", Data: map[string]string{"text": " 这玩意好吃吗"}},
	}
	if runtime.requiresTelegramBotMentionJudgment(atSelf) {
		t.Fatal("被标记的账号明确 @ 本机时不该再走语义判定")
	}

	replyToSelf := base
	replyToSelf.Quoted = &QuotedMessage{MessageID: "bot-1", UserID: "3129583166"}
	if runtime.requiresTelegramBotMentionJudgment(replyToSelf) {
		t.Fatal("被标记的账号回复本机时不该再走语义判定")
	}
}

// 没点名本机的普通发言仍旧要判：这条闸是用来挡机器人噪音的，不能顺手拆掉。
func TestMarkedBotOrdinaryMessageStillNeedsJudgment(t *testing.T) {
	cfg := BotConfig{BotAccount: "3129583166", MarkedBotIDs: []string{"160867498"}}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	plain := MessageEvent{
		Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "791503570",
		UserID: "160867498", SelfID: "3129583166", MessageID: "m2",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "今天天气不错"}}},
	}
	if !runtime.requiresTelegramBotMentionJudgment(plain) {
		t.Fatal("被标记的账号普通发言仍应走语义判定")
	}
	// @ 的是别人，也不算点名本机。
	atOther := plain
	atOther.Segments = []MessageSegment{
		{Type: "at", Data: map[string]string{"qq": "999"}},
		{Type: "text", Data: map[string]string{"text": " 你怎么看"}},
	}
	if !runtime.requiresTelegramBotMentionJudgment(atOther) {
		t.Fatal("@ 别人时仍应走语义判定")
	}
	// 引用的是别人的消息，同样不算。
	quoteOther := plain
	quoteOther.Quoted = &QuotedMessage{MessageID: "x", UserID: "999"}
	if !runtime.requiresTelegramBotMentionJudgment(quoteOther) {
		t.Fatal("引用别人时仍应走语义判定")
	}
}
