package assistant

import "testing"

// TestOneBotEventPayloadForGroup 验证对应功能场景。
func TestOneBotEventPayloadForGroup(t *testing.T) {
	event := MessageEvent{
		Kind:        EventKindGroup,
		Time:        123,
		SelfID:      "42",
		UserID:      "1001",
		GroupID:     "2002",
		MessageID:   "3003",
		MessageType: "group",
		RawMessage:  "hello",
		Segments:    []MessageSegment{{Type: "text", Data: map[string]string{"text": "hello"}}},
		SenderName:  "Alice",
	}

	payload := oneBotEventPayload(event)
	if payload["post_type"] != "message" || payload["message_type"] != "group" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload["self_id"] != int64(42) || payload["group_id"] != int64(2002) {
		t.Fatalf("numeric ids not converted: %#v", payload)
	}
	if payload["raw_message"] != "hello" {
		t.Fatalf("raw_message = %#v", payload["raw_message"])
	}
}

// TestConfigFromPayloadKeepsNoneBotBridgeToken 验证对应功能场景。
func TestConfigFromPayloadKeepsNoneBotBridgeToken(t *testing.T) {
	got := ConfigFromPayload(ConfigPayload{
		Enabled:               true,
		NoneBotBridgeEnabled:  true,
		NoneBotBridgeEndpoint: "ws://127.0.0.1:8080/onebot/v11/ws",
	}, BotConfig{NoneBotBridgeToken: "old-token"})

	if got.NoneBotBridgeToken != "old-token" {
		t.Fatalf("NoneBotBridgeToken = %q", got.NoneBotBridgeToken)
	}
}
