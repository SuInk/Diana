// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

func TestQQEmojiCDNURL(t *testing.T) {
	const id = "9fd5e5bd37cddf1cdd8c0814f4313f79"
	want := "https://gxh.vip.qq.com/club/item/parcel/item/9f/" + id + "/200x200.png"
	for name, data := range map[string]map[string]string{
		"emoji_id":       {"emoji_id": id},
		"upper emoji_id": {"emoji_id": "9FD5E5BD37CDDF1CDD8C0814F4313F79"},
		"file name":      {"file": "9f-" + id + ".gif"},
	} {
		if got := qqEmojiCDNURL(data); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
	for name, data := range map[string]map[string]string{
		"plain md5 image":   {"file": "2153f3de8900cb7884c50412d2c6a7c6.png"},
		"mismatched prefix": {"file": "aa-" + id + ".gif"},
		"short id":          {"emoji_id": "9fd5e5"},
		"path injection":    {"emoji_id": "../" + id},
		"empty":             {},
	} {
		if got := qqEmojiCDNURL(data); got != "" {
			t.Errorf("%s: got %q, want empty", name, got)
		}
	}
}

func TestEnrichMediaFallsBackToQQEmojiCDN(t *testing.T) {
	const id = "e20461fc34df6a4f6521f989d8196f5b"
	channel := &recordingChannel{apiResponses: map[string]map[string]any{}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event, failures := runtime.enrichMediaReferencesDetailed(context.Background(), MessageEvent{
		MessageID: "123",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"file": "e2-" + id + ".gif", "emoji_id": id, "sub_type": "0", imageUnavailableKey: "true",
		}}},
	})
	if len(failures) != 0 {
		t.Fatalf("failures = %v", failures)
	}
	want := qqEmojiCDNURL(map[string]string{"emoji_id": id})
	if got := imageSourceCandidates(event.Segments[0]); len(got) == 0 || got[0] != want {
		t.Fatalf("candidates = %#v, want first %q", got, want)
	}
}

func TestEnrichMediaPrefersOneBotSourceOverEmojiCDN(t *testing.T) {
	const id = "e20461fc34df6a4f6521f989d8196f5b"
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_image": {"url": "https://multimedia.nt.qq.com.cn/download?fileid=fresh"},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event, _ := runtime.enrichMediaReferencesDetailed(context.Background(), MessageEvent{
		MessageID: "123",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"file": "e2-" + id + ".gif", "emoji_id": id, imageUnavailableKey: "true",
		}}},
	})
	for _, source := range imageSourceCandidates(event.Segments[0]) {
		if source == qqEmojiCDNURL(map[string]string{"emoji_id": id}) {
			t.Fatalf("CDN fallback added although get_image resolved: %#v", event.Segments[0].Data)
		}
	}
}
