// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type pokeFallbackChannel struct {
	*recordingChannel
	failing map[string]bool
}

func (c *pokeFallbackChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	response, _ := c.recordingChannel.CallAPI(ctx, action, params)
	if c.failing[action] {
		return nil, errors.New("unsupported action")
	}
	return response, nil
}

func pokeTestRuntime(channel Channel) *Runtime {
	return NewRuntime(BotConfig{BotAccount: "10000", Platform: PlatformOneBotV11}, channel, NewPluginManager(), nil, nil, nil, nil)
}

func TestPokeToolPokesSenderInGroupAndPrivate(t *testing.T) {
	channel := &recordingChannel{}
	runtime := pokeTestRuntime(channel)
	group := MessageEvent{Kind: EventKindGroup, UserID: "555", GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11}
	if _, err := newDianaPokeTool(runtime, group).Run(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	calls := recordedCallsByAction(channel.callsSnapshot(), "group_poke")
	if len(calls) != 1 || stringFromAny(calls[0].params["group_id"]) != "123" || stringFromAny(calls[0].params["user_id"]) != "555" {
		t.Fatalf("group_poke calls = %#v", calls)
	}
	private := MessageEvent{Kind: EventKindPrivate, UserID: "666", SelfID: "10000", Platform: PlatformOneBotV11}
	if _, err := newDianaPokeTool(runtime, private).Run(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if calls := recordedCallsByAction(channel.callsSnapshot(), "friend_poke"); len(calls) != 1 || stringFromAny(calls[0].params["user_id"]) != "666" {
		t.Fatalf("friend_poke calls = %#v", calls)
	}
}

func TestPokeToolOnlyTargetsPeopleInTheConversation(t *testing.T) {
	channel := &recordingChannel{}
	runtime := pokeTestRuntime(channel)
	event := MessageEvent{
		Kind: EventKindGroup, UserID: "555", GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11,
		Segments: []MessageSegment{{Type: "at", Data: map[string]string{"qq": "777"}}},
		Quoted:   &QuotedMessage{UserID: "888"},
	}
	tool := newDianaPokeTool(runtime, event)
	for _, allowed := range []string{"777", "888"} {
		if _, err := tool.Run(context.Background(), map[string]any{"user_id": allowed}); err != nil {
			t.Fatalf("poke %s: %v", allowed, err)
		}
	}
	for _, denied := range []string{"999", "10000"} {
		if _, err := tool.Run(context.Background(), map[string]any{"user_id": denied}); err == nil {
			t.Fatalf("poke %s should be rejected", denied)
		}
	}
}

func TestPokeSendRespectsPersonCooldownAndSessionLimit(t *testing.T) {
	channel := &recordingChannel{}
	runtime := pokeTestRuntime(channel)
	event := MessageEvent{Kind: EventKindGroup, UserID: "1", GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11}
	if _, err := runtime.sendPoke(context.Background(), event, "1", pokeSceneChat); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.sendPoke(context.Background(), event, "1", pokeSceneChat); !errors.Is(err, errPokeRateLimited) {
		t.Fatalf("same person within cooldown err = %v", err)
	}
	for _, target := range []string{"2", "3"} {
		if _, err := runtime.sendPoke(context.Background(), event, target, pokeSceneChat); err != nil {
			t.Fatalf("poke %s: %v", target, err)
		}
	}
	if _, err := runtime.sendPoke(context.Background(), event, "4", pokeSceneChat); !errors.Is(err, errPokeRateLimited) {
		t.Fatalf("session limit not enforced: %v", err)
	}
	if got := len(recordedCallsByAction(channel.callsSnapshot(), "group_poke")); got != pokeSendSessionLimit {
		t.Fatalf("group_poke calls = %d, want %d", got, pokeSendSessionLimit)
	}
}

func TestPokeFallsBackToSendPoke(t *testing.T) {
	channel := &pokeFallbackChannel{recordingChannel: &recordingChannel{}, failing: map[string]bool{"friend_poke": true}}
	runtime := pokeTestRuntime(channel)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "666", SelfID: "10000", Platform: PlatformOneBotV11}
	action, err := runtime.sendPoke(context.Background(), event, "666", pokeSceneBack)
	if err != nil || action != "send_poke" {
		t.Fatalf("action = %q err = %v", action, err)
	}
}

func TestPokeToolDescriptionUsesItNaturally(t *testing.T) {
	description := newDianaPokeTool(nil, MessageEvent{}).Description()
	for _, want := range []string{"顺手用", "戳回去", "安慰", "晚安", "不要用的时候", "还不熟", "不要在回复里说"} {
		if !strings.Contains(description, want) {
			t.Fatalf("description missing %q", want)
		}
	}
}

func TestParsePokeReactionNormalizesChoices(t *testing.T) {
	for _, tc := range []struct {
		raw        string
		action     string
		text       string
		pokes      bool
		sendsText  bool
		shouldFail bool
	}{
		{raw: `{"action":"poke","text":"忽略"}`, action: "poke", pokes: true},
		{raw: "```json\n{\"action\":\"both\",\"text\":\"还不睡？\"}\n```", action: "both", text: "还不睡？", pokes: true, sendsText: true},
		{raw: `{"action":"both","text":""}`, action: "poke", pokes: true},
		{raw: `{"action":"text","text":""}`, action: "none"},
		{raw: `{"action":"none"}`, action: "none"},
		{raw: `{"action":"hug"}`, shouldFail: true},
	} {
		reaction, err := parsePokeReaction(tc.raw)
		if tc.shouldFail {
			if err == nil {
				t.Fatalf("%s accepted", tc.raw)
			}
			continue
		}
		if err != nil || reaction.Action != tc.action || reaction.Text != tc.text || reaction.pokesBack() != tc.pokes || reaction.sendsText() != tc.sendsText {
			t.Fatalf("%s → %+v err=%v", tc.raw, reaction, err)
		}
	}
}
