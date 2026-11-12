// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

func ownerPromptCfg() BotConfig {
	return BotConfig{Platform: PlatformOneBotV11, OwnerID: "10001", BotAccount: "20002"}
}

func ownerPromptEvent(userID string) MessageEvent {
	return MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "40004", SelfID: "20002", UserID: userID, RawMessage: "hi"}
}

// 主人发言、被引用、被 @ 都算在场；不在场不写，别的平台的同号不算。
func TestOwnerAccountPromptOnlyWhenOwnerAppears(t *testing.T) {
	cfg := ownerPromptCfg()
	current := ownerPromptEvent("30003")
	quoted := ownerPromptEvent("50005")
	quoted.Quoted = &QuotedMessage{UserID: "10001"}
	mentioned := ownerPromptEvent("50005")
	mentioned.MentionTargets = []MessageMention{{UserID: "10001"}}
	otherPlatform := ownerPromptEvent("10001")
	otherPlatform.Platform = PlatformTelegram

	for name, history := range map[string][]MessageEvent{
		"spoke":     {ownerPromptEvent("10001")},
		"quoted":    {quoted},
		"mentioned": {mentioned},
	} {
		if got := ownerAccountPrompt(cfg, current, history); !strings.HasPrefix(got, "【主人】") || !strings.Contains(got, "10001") {
			t.Fatalf("%s: owner prompt = %q", name, got)
		}
	}
	for name, history := range map[string][]MessageEvent{
		"absent":         {ownerPromptEvent("50005")},
		"other platform": {otherPlatform},
	} {
		if got := ownerAccountPrompt(cfg, current, history); got != "" {
			t.Fatalf("%s: unexpected owner prompt %q", name, got)
		}
	}
	if got := ownerAccountPrompt(BotConfig{Platform: PlatformOneBotV11}, current, []MessageEvent{ownerPromptEvent("10001")}); got != "" {
		t.Fatalf("no owner configured but got %q", got)
	}
}

// 只要出现，内容只看配置，换谁说话都逐字相同。
func TestOwnerAccountPromptStableAcrossSpeakers(t *testing.T) {
	cfg := ownerPromptCfg()
	history := []MessageEvent{ownerPromptEvent("10001")}
	a := ownerAccountPrompt(cfg, ownerPromptEvent("30003"), history)
	b := ownerAccountPrompt(cfg, ownerPromptEvent("10001"), history)
	if a == "" || a != b {
		t.Fatalf("owner prompt varies by speaker:\n%q\n%q", a, b)
	}
}

func TestWelcomeLLMModeRecognizesOwner(t *testing.T) {
	stub := &welcomeStubLLM{text: "回来啦"}
	runtime := newLLMWelcomeRuntime(BotConfig{WelcomeEnabled: true, WelcomeMode: WelcomeModeLLM, OwnerID: "u-owner"}, &recordingChannel{}, stub)
	if err := runtime.handleNotice(context.Background(), welcomeNoticeEvent("u-owner")); err != nil {
		t.Fatal(err)
	}
	if dump := welcomeRequestDump(stub.lastRequest()); !strings.Contains(dump, welcomeOwnerPrompt) {
		t.Fatalf("owner welcome missing owner prompt:\n%s", dump)
	}

	stub2 := &welcomeStubLLM{text: "来了"}
	other := newLLMWelcomeRuntime(BotConfig{WelcomeEnabled: true, WelcomeMode: WelcomeModeLLM, OwnerID: "u-owner"}, &recordingChannel{}, stub2)
	if err := other.handleNotice(context.Background(), welcomeNoticeEvent("u-new")); err != nil {
		t.Fatal(err)
	}
	if dump := welcomeRequestDump(stub2.lastRequest()); strings.Contains(dump, welcomeOwnerPrompt) {
		t.Fatalf("stranger welcome got owner prompt:\n%s", dump)
	}
}
