// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

func TestNoticeUsesGroupScopedConfig(t *testing.T) {
	runtime := NewRuntime(BotConfig{
		WelcomeEnabled: true,
		WelcomeMessage: "全局欢迎",
	}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(staticGroupConfigStore{cfg: GroupConfig{
		BotProfileID:   "bot-a",
		GroupID:        "g1",
		Enabled:        true,
		EnabledSet:     true,
		WelcomeEnabled: true,
		WelcomeMessage: "本群欢迎",
		ReplyGate:      &ReplyGate{UserAdmission: UserAdmissionWhitelist, AllowedUsers: []string{"u1"}},
	}})

	cfg := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindNotice, ProfileID: "bot-a", GroupID: "g1", UserID: "u1"})
	if cfg.WelcomeMessage != "本群欢迎" || cfg.ReplyGate == nil || !cfg.ReplyGate.IsAllowedUser("u1") {
		t.Fatalf("notice group config = %#v", cfg)
	}
}

func TestWelcomeNoticeHonorsGroupAdmissionAndReplyGate(t *testing.T) {
	t.Run("group admission", func(t *testing.T) {
		channel := &recordingChannel{}
		runtime := NewRuntime(BotConfig{
			WelcomeEnabled: true, WelcomeMessage: "欢迎 {user_id}",
			GroupAdmission: GroupAdmission{Mode: GroupAdmissionWhitelist, AllowedGroups: []string{"allowed"}},
		}, channel, NewPluginManager(), nil, nil, nil, nil)
		if err := runtime.handleNotice(context.Background(), MessageEvent{Kind: EventKindNotice, SubType: "group_increase", GroupID: "blocked", UserID: "u1"}); err != nil {
			t.Fatal(err)
		}
		if len(channel.sent) != 0 {
			t.Fatalf("disallowed group received welcome: %#v", channel.sent)
		}
	})

	t.Run("group reply gate", func(t *testing.T) {
		channel := &recordingChannel{}
		runtime := NewRuntime(BotConfig{WelcomeEnabled: true, WelcomeMessage: "全局欢迎"}, channel, NewPluginManager(), nil, nil, nil, nil)
		runtime.SetGroupConfigStore(staticGroupConfigStore{cfg: GroupConfig{
			GroupID: "g1", Enabled: true, EnabledSet: true,
			WelcomeEnabled: true, WelcomeMessage: "本群欢迎 {user_id}",
			ReplyGate: &ReplyGate{UserAdmission: UserAdmissionWhitelist, AllowedUsers: []string{"allowed-user"}},
		}})
		blocked := MessageEvent{Kind: EventKindNotice, SubType: "group_increase", GroupID: "g1", UserID: "blocked-user"}
		if err := runtime.handleNotice(context.Background(), blocked); err != nil {
			t.Fatal(err)
		}
		if len(channel.sent) != 0 {
			t.Fatalf("non-whitelisted member received welcome: %#v", channel.sent)
		}
		allowed := blocked
		allowed.UserID = "allowed-user"
		if err := runtime.handleNotice(context.Background(), allowed); err != nil {
			t.Fatal(err)
		}
		if len(channel.sent) != 1 || channel.sent[0].Text != "本群欢迎 allowed-user" {
			t.Fatalf("group welcome = %#v", channel.sent)
		}
	})
}

func TestPokeNoticeHonorsGroupAdmissionAndReplyGate(t *testing.T) {
	provider := &capturingLLMProvider{reply: "嗯？"}
	factory := func() (LLMProvider, error) { return provider, nil }

	disallowedGroup := NewRuntime(BotConfig{
		BotAccount: "10000", PokeReplyEnabled: boolPointer(true),
		GroupAdmission: GroupAdmission{Mode: GroupAdmissionWhitelist, AllowedGroups: []string{"other"}},
	}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, factory)
	if err := disallowedGroup.handleNotice(context.Background(), pokeTestEvent()); err != nil {
		t.Fatal(err)
	}
	if len(provider.requestSnapshot().Messages) != 0 {
		t.Fatal("poke in a disallowed group reached the model")
	}

	blockedUser := NewRuntime(BotConfig{
		BotAccount: "10000", PokeReplyEnabled: boolPointer(true),
		ReplyGate: &ReplyGate{BlockedUsers: []string{"10005"}},
	}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, factory)
	if err := blockedUser.handleNotice(context.Background(), pokeTestEvent()); err != nil {
		t.Fatal(err)
	}
	if len(provider.requestSnapshot().Messages) != 0 {
		t.Fatal("blocked user's poke reached the model")
	}
}
