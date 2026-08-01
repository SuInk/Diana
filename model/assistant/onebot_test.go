package assistant

import (
	"encoding/json"
	"testing"
)

// TestCQToSegmentsAndPlainText 验证对应功能场景。
func TestCQToSegmentsAndPlainText(t *testing.T) {
	got := CQToSegments("hi [CQ:at,qq=123] 看图 [CQ:image,file=a.jpg]")
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4: %#v", len(got), got)
	}
	if got[1].Type != "at" || got[1].Data["qq"] != "123" {
		t.Fatalf("at segment = %#v", got[1])
	}
	if text := PlainText(got); text != "hi @123  看图 [图片]" {
		t.Fatalf("PlainText = %q", text)
	}
}

// TestImageURLsExtractsRemoteAndBase64Images 验证图片段能提取远端或 data URL。
func TestImageURLsExtractsRemoteAndBase64Images(t *testing.T) {
	got := ImageURLs([]MessageSegment{
		{Type: "image", Data: map[string]string{"url": "https://example.com/a.jpg"}},
		{Type: "image", Data: map[string]string{"file": "base64://abcd"}},
		{Type: "image", Data: map[string]string{"file": "local-cache.jpg"}},
	})
	want := []string{"https://example.com/a.jpg", "data:image/jpeg;base64,abcd"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestTextToOneBotSegmentsKeepsCQAt 验证对应功能场景。
func TestTextToOneBotSegmentsKeepsCQAt(t *testing.T) {
	got := TextToOneBotSegments("[CQ:at,qq=123] hello")
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Type != "at" || got[0].Data["qq"] != "123" {
		t.Fatalf("first segment = %#v", got[0])
	}
}

// TestOneBotChannelSendPrefixesReplyAndMention 验证对应功能场景。
func TestOneBotChannelSendPrefixesReplyAndMention(t *testing.T) {
	message := buildOutgoingSegments(OutgoingMessage{
		GroupID:        "123",
		Text:           "你好",
		ReplyMessageID: "456",
		MentionUserID:  "789",
	})
	if len(message) < 3 {
		t.Fatalf("message = %#v", message)
	}
	if message[0]["type"] != "reply" || message[1]["type"] != "at" {
		t.Fatalf("message = %#v", message)
	}
}

// TestMessageEventFromEnvelopeNoticeGroupIncrease 验证对应功能场景。
func TestMessageEventFromEnvelopeNoticeGroupIncrease(t *testing.T) {
	event := messageEventFromEnvelope(oneBotEnvelope{
		Time:     123,
		SelfID:   "42",
		PostType: "notice",
		SubType:  "group_increase",
		UserID:   "10001",
		GroupID:  "20002",
	})
	if event.Kind != EventKindNotice || event.SubType != "group_increase" {
		t.Fatalf("event = %#v", event)
	}
	if event.UserID != "10001" || event.GroupID != "20002" {
		t.Fatalf("event = %#v", event)
	}
}

// TestOneBotEnvelopeAllowsObjectStatus 验证对应功能场景。
func TestOneBotEnvelopeAllowsObjectStatus(t *testing.T) {
	var envelope oneBotEnvelope
	err := json.Unmarshal([]byte(`{"status":{"online":true,"good":true},"retcode":0,"echo":"debug"}`), &envelope)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !envelopeStatusOK(envelope) {
		t.Fatalf("status should be ok: %#v", envelope.Status)
	}
	if text := envelopeStatusText(envelope.Status); text == "" {
		t.Fatal("status text should not be empty")
	}
}
