// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"
	"time"
)

func TestSkipStickerOnlyRouting(t *testing.T) {
	cfg := BotConfig{BotAccount: "10001"}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	now := time.Now().Unix()
	sticker := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "30001", SelfID: "10001", MessageID: "m1", Time: now,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"sub_type": "1", "summary": "[动画表情]"}}}}
	batch := []proactiveReplyCandidate{{Event: sticker}}

	if !runtime.skipStickerOnlyRouting(batch, cfg, "low") {
		t.Fatal("an unaddressed sticker with the bot quiet should skip routing")
	}
	if runtime.skipStickerOnlyRouting(batch, cfg, "always") {
		t.Fatal("always lets everything through, routing must run")
	}

	photo := sticker
	photo.Segments = []MessageSegment{{Type: "image", Data: map[string]string{"sub_type": "0"}}}
	if runtime.skipStickerOnlyRouting([]proactiveReplyCandidate{{Event: photo}}, cfg, "low") {
		t.Fatal("a plain photo is not a sticker")
	}
	withText := sticker
	withText.Segments = append([]MessageSegment{{Type: "text", Data: map[string]string{"text": "哈哈"}}}, sticker.Segments...)
	if runtime.skipStickerOnlyRouting([]proactiveReplyCandidate{{Event: withText}}, cfg, "low") {
		t.Fatal("a sticker with text must still be rated")
	}
	quoted := sticker
	quoted.Quoted = &QuotedMessage{UserID: "10001", MessageID: "b0"}
	if runtime.skipStickerOnlyRouting([]proactiveReplyCandidate{{Event: quoted}}, cfg, "low") {
		t.Fatal("a sticker quoting someone must still be rated")
	}

	asked := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "30001", SelfID: "10001", MessageID: "a1", Time: now - 30, ToMe: true,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "帮我看看"}}}}
	other := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	other.remember(asked)
	if other.skipStickerOnlyRouting(batch, cfg, "low") {
		t.Fatal("someone just addressed the bot; a follow-up sticker must be rated")
	}

	bot := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", SelfID: "10001", MessageID: "b1", Time: now - 60, Outbound: true,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "我刚说了句话"}}}}
	runtime.remember(bot)
	if runtime.skipStickerOnlyRouting(batch, cfg, "low") {
		t.Fatal("a sticker right after the bot spoke may be a reply to it and must be rated")
	}
}
