// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

func TestPrivateAdmissionDefaultsAndPayloadRoundTrip(t *testing.T) {
	for _, cfg := range []BotConfig{DefaultBotConfig(), (BotConfig{}).WithDefaults()} {
		if cfg.PrivateAdmission.Mode != PrivateAdmissionDisabled {
			t.Fatalf("default private admission = %q, want disabled", cfg.PrivateAdmission.Mode)
		}
	}
	for _, mode := range []string{PrivateAdmissionDisabled, PrivateAdmissionAll, PrivateAdmissionOwnerOnly, PrivateAdmissionWhitelist} {
		cfg := BotConfig{PrivateAdmission: PrivateAdmission{Mode: mode, AllowedUsers: []string{"42"}}}.WithDefaults()
		restored := ConfigFromPayload(PayloadFromConfig(cfg), DefaultBotConfig()).WithDefaults()
		if restored.PrivateAdmission.Mode != mode || len(restored.PrivateAdmission.AllowedUsers) != 1 || restored.PrivateAdmission.AllowedUsers[0] != "42" {
			t.Fatalf("private admission lost on save: %+v", restored.PrivateAdmission)
		}
	}
}

func TestDisabledPrivateAdmissionIgnoresMessagesAndPokes(t *testing.T) {
	for _, userID := range []string{"owner", "stranger"} {
		t.Run(userID, func(t *testing.T) {
			channel := &recordingChannel{}
			store := newMemoryInboundEventStore()
			runtime := NewRuntime(BotConfig{
				BotAccount: "10000", OwnerID: "owner", PokeReplyEnabled: boolPointer(true),
			}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
				t.Fatal("disabled private chat must not request a model")
				return nil, nil
			})
			runtime.SetInboundEventStore(store)
			event := MessageEvent{Kind: EventKindPrivate, UserID: userID, MessageID: "m1", RawMessage: "hello"}
			if err := runtime.HandleEvent(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			poke := MessageEvent{Kind: EventKindNotice, SubType: "poke", SelfID: "10000", UserID: userID, TargetID: "10000"}
			if err := runtime.HandleEvent(context.Background(), poke); err != nil {
				t.Fatal(err)
			}
			if len(store.order) != 0 || len(channel.sentSnapshot()) != 0 || len(runtime.contextHistory(event)) != 0 {
				t.Fatal("disabled private chat queued, replied or entered history")
			}
		})
	}
}

func TestDisabledPrivateAdmissionStopsQueuedMessagesBeforePreprocessing(t *testing.T) {
	cfg := BotConfig{ID: "bot", Enabled: true, OwnerID: "owner", PrivateAdmission: PrivateAdmission{Mode: PrivateAdmissionAll}}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, ProfileID: cfg.ID, UserID: "owner", MessageID: "queued", RawMessage: "hello"}
	if !runtime.privateAdmissionAllows(event) {
		t.Fatal("private chat must be admitted before it is disabled")
	}
	cfg.PrivateAdmission.Mode = PrivateAdmissionDisabled
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), event)
	if handled || outcome != "ignored_private_admission" || len(runtime.contextHistory(event)) != 0 {
		t.Fatalf("queued private chat handled=%v outcome=%q", handled, outcome)
	}
}

func TestDisabledPrivateAdmissionDoesNotExemptTelegramUsernameOwner(t *testing.T) {
	cfg := BotConfig{Platform: PlatformTelegram, OwnerID: "@owner"}
	owner := MessageEvent{Kind: EventKindPrivate, Platform: PlatformTelegram, UserID: "123", SenderUsername: "owner"}
	if privateAdmissionAllowsConfig(cfg, owner) {
		t.Fatal("disabled private chat must also ignore Telegram username owners")
	}
}
