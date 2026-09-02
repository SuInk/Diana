// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func pokeEnvelope(t *testing.T) oneBotEnvelope {
	t.Helper()
	raw := []byte(`{
		"post_type": "notice",
		"notice_type": "notify",
		"sub_type": "poke",
		"group_id": 20002,
		"user_id": 10005,
		"target_id": 10000,
		"self_id": 10000,
		"time": 1700000000
	}`)
	var envelope oneBotEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestMessageEventFromEnvelopeParsesPoke(t *testing.T) {
	event := messageEventFromEnvelope(pokeEnvelope(t))
	if event.Kind != EventKindNotice || event.SubType != "poke" {
		t.Fatalf("event = %#v", event)
	}
	if event.UserID != "10005" || event.TargetID != "10000" || event.GroupID != "20002" {
		t.Fatalf("ids = %#v", event)
	}
}

func pokeTestEvent() MessageEvent {
	return MessageEvent{
		Kind:     EventKindNotice,
		SubType:  "poke",
		SelfID:   "10000",
		UserID:   "10005",
		TargetID: "10000",
		GroupID:  "20002",
	}
}

func TestHandlePokeNoticeRepliesInPersona(t *testing.T) {
	channel := &recordingChannel{}
	provider := &capturingLLMProvider{reply: "干嘛戳我呀"}
	runtime := NewRuntime(BotConfig{
		BotAccount:       "10000",
		PokeReplyEnabled: boolPointer(true),
		SystemPrompt:     "你是猫娘",
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	if err := runtime.handleNotice(context.Background(), pokeTestEvent()); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != "干嘛戳我呀" || channel.sent[0].GroupID != "20002" {
		t.Fatalf("sent = %#v", channel.sent)
	}
	// 旁路生成要带上人设。
	request := provider.requestSnapshot()
	if len(request.Messages) < 2 || request.Messages[0].Role != "system" {
		t.Fatalf("messages = %#v", request.Messages)
	}

	// 冷却期内的第二次戳直接忽略。
	if err := runtime.handleNotice(context.Background(), pokeTestEvent()); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("cooldown ignored: %#v", channel.sent)
	}
}

func TestHandlePokeNoticeGates(t *testing.T) {
	channel := &recordingChannel{}
	provider := &capturingLLMProvider{reply: "嗯？"}
	factory := func() (LLMProvider, error) { return provider, nil }

	// 开关默认关：一言不发。
	off := NewRuntime(BotConfig{BotAccount: "10000"}, channel, NewPluginManager(), nil, nil, nil, factory)
	if err := off.handleNotice(context.Background(), pokeTestEvent()); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("disabled poke replied: %#v", channel.sent)
	}

	// 戳的不是机器人：不掺和别人互戳。
	on := NewRuntime(BotConfig{BotAccount: "10000", PokeReplyEnabled: boolPointer(true)}, channel, NewPluginManager(), nil, nil, nil, factory)
	other := pokeTestEvent()
	other.TargetID = "10006"
	if err := on.handleNotice(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	// 机器人戳自己（有实现会回环）：同样忽略。
	self := pokeTestEvent()
	self.UserID = "10000"
	if err := on.handleNotice(context.Background(), self); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("gated pokes replied: %#v", channel.sent)
	}

	// 模型不可用：沉默而不是报错刷屏。
	silent := NewRuntime(BotConfig{BotAccount: "10000", PokeReplyEnabled: boolPointer(true)}, channel, NewPluginManager(), nil, nil, nil, nil)
	if err := silent.handleNotice(context.Background(), pokeTestEvent()); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("llm-less poke replied: %#v", channel.sent)
	}
}

func TestClaimPokeReplyCooldown(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	now := time.Now()
	if !runtime.claimPokeReply("bot", "10005", now) {
		t.Fatal("first claim rejected")
	}
	if runtime.claimPokeReply("bot", "10005", now.Add(pokeReplyCooldown/2)) {
		t.Fatal("cooldown not enforced")
	}
	// 不同的人互不影响，冷却过了再戳能回。
	if !runtime.claimPokeReply("bot", "10006", now) {
		t.Fatal("other user blocked")
	}
	if !runtime.claimPokeReply("bot", "10005", now.Add(pokeReplyCooldown+time.Second)) {
		t.Fatal("expired cooldown still blocking")
	}
}
