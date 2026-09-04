package assistant

import (
	"context"
	"sync"
	"testing"
)

type relayRecordingChannel struct {
	mu       sync.Mutex
	messages []OutgoingMessage
}

func (*relayRecordingChannel) Connect(context.Context, EventHandler) error { return nil }
func (c *relayRecordingChannel) Send(_ context.Context, msg OutgoingMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, msg)
	return nil
}
func (*relayRecordingChannel) CallAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, nil
}
func (*relayRecordingChannel) Status() ChannelStatus { return ChannelStatus{} }
func (*relayRecordingChannel) Close() error          { return nil }

func TestMessageRelayPluginForwardsToOtherEndpoints(t *testing.T) {
	channel := &relayRecordingChannel{}
	plugin := NewMessageRelayPlugin()
	err := plugin.RelayEvent(context.Background(), PluginRequest{
		Event:   MessageEvent{Platform: PlatformTelegram, ProfileID: "tg", Kind: EventKindGroup, GroupID: "-1001", UserID: "42", SenderName: "Miku", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "你好"}}, {Type: "image", Data: map[string]string{"cached_file": "/tmp/a.jpg"}}}},
		Channel: channel,
		Settings: SettingValues{messageRelayEndpoints: []map[string]any{
			{"profile_id": "tg", "platform": PlatformTelegram, "kind": "group", "target_id": "-1001"},
			{"profile_id": "qq", "platform": PlatformOneBotV11, "kind": "group", "target_id": "123"},
			{"profile_id": "tg", "platform": PlatformTelegram, "kind": "private", "target_id": "99"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(channel.messages) != 2 {
		t.Fatalf("messages = %#v", channel.messages)
	}
	if got := channel.messages[0]; got.ProfileID != "qq" || got.GroupID != "123" || got.Text != "【Telegram · Miku】\n你好" || len(got.ImageURLs) != 1 {
		t.Fatalf("first = %#v", got)
	}
	if got := channel.messages[1]; got.ProfileID != "tg" || got.UserID != "99" {
		t.Fatalf("second = %#v", got)
	}
}

func TestMessageRelayPluginSkipsSelfEchoAndUnselectedConversation(t *testing.T) {
	channel := &relayRecordingChannel{}
	settings := SettingValues{messageRelayEndpoints: []map[string]any{
		{"profile_id": "tg", "platform": PlatformTelegram, "kind": "group", "target_id": "-1001"},
		{"profile_id": "qq", "platform": PlatformOneBotV11, "kind": "group", "target_id": "123"},
	}}
	plugin := NewMessageRelayPlugin()
	events := []MessageEvent{
		{Platform: PlatformTelegram, ProfileID: "tg", Kind: EventKindGroup, GroupID: "other", UserID: "42", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "x"}}}},
		{Platform: PlatformTelegram, ProfileID: "tg", Kind: EventKindGroup, GroupID: "-1001", UserID: "bot", SelfID: "bot", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "x"}}}},
	}
	for _, event := range events {
		if err := plugin.RelayEvent(context.Background(), PluginRequest{Event: event, Channel: channel, Settings: settings}); err != nil {
			t.Fatal(err)
		}
	}
	if len(channel.messages) != 0 {
		t.Fatalf("messages = %#v", channel.messages)
	}
}

func TestMessageRelayPluginIsDefaultDisabled(t *testing.T) {
	manifest := NewMessageRelayPlugin().Manifest()
	if !manifest.BuiltIn || !manifest.DefaultDisabled || manifest.Version != "0.1.0" {
		t.Fatalf("manifest = %#v", manifest)
	}
}
