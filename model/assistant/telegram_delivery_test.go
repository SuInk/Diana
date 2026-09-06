package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestTelegramMergeThresholdUsesOrdinaryMessagesWithoutAudit(t *testing.T) {
	withFastSendTiming(t)
	for _, multi := range []bool{false, true} {
		for _, kind := range []EventKind{EventKindGroup, EventKindPrivate} {
			for _, tc := range []struct {
				name, text string
				threshold  int
				want       []string
			}{
				{"merge", "第一条" + notificationSplitMarker + "第二条", 1, []string{"第一条\n\n第二条"}},
				{"no merge", "第一条" + notificationSplitMarker + "第二条", 0, []string{"第一条", "第二条"}},
				{"single", replySingleMarker + "第一条" + notificationSplitMarker + "第二条", 1, []string{"第一条\n第二条"}},
				{"over capacity", strings.Repeat("甲", 3000) + notificationSplitMarker + strings.Repeat("乙", 3000), 1, []string{strings.Repeat("甲", 3000), strings.Repeat("乙", 3000)}},
				{"rich text", "**标题**" + notificationSplitMarker + "\n```python\nprint(1)\n```", 1, []string{"标题\n\nprint(1)"}},
				{"mention capacity", strings.Repeat("甲", 2050) + "[diana-at:10001]" + notificationSplitMarker + strings.Repeat("乙", 2020), 1, []string{strings.Repeat("甲", 2050) + "@" + strings.Repeat("名", 60), strings.Repeat("乙", 2020)}},
			} {
				t.Run(tc.name+map[bool]string{false: "/direct", true: "/multi"}[multi]+"/"+string(kind), func(t *testing.T) {
					api := newFakeTelegramAPI(t, map[string]any{"sendMessage": map[string]any{"message_id": 1}})
					var channel Channel = api.channel()
					event := MessageEvent{Kind: kind, UserID: "10001", SelfID: "42", SenderName: strings.Repeat("名", 60)}
					if kind == EventKindGroup {
						event.GroupID = "-100123"
						event.MessageThreadID = "17"
					}
					onebot := &recordingChannel{}
					if multi {
						channel = NewMultiChannel([]ChannelBinding{{ProfileID: "tg", Platform: PlatformTelegram, Channel: channel}, {ProfileID: "qq", Platform: PlatformOneBotV11, Channel: onebot}})
						event.ProfileID = "tg"
					}
					provider := &qualityTestProvider{reply: `{"should_send":true,"confidence":0.99,"account_safe":true}`}
					// Default config is deliberately OneBot; the actual transport wins.
					rt := NewRuntime(BotConfig{ForwardReplyThreshold: tc.threshold, SendChunkIntervalMS: 1}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
					if _, err := rt.sendDecorated(context.Background(), event, tc.text, outboundDecoration{}); err != nil {
						t.Fatal(err)
					}
					calls := api.callsOf("sendMessage")
					if len(calls) != len(tc.want) || len(api.calls) != len(tc.want) || len(provider.requests) != 0 || len(onebot.callsSnapshot()) != 0 {
						t.Fatalf("unexpected dispatch: calls=%v audits=%d onebot=%v", api.calls, len(provider.requests), onebot.callsSnapshot())
					}
					for i, call := range calls {
						if call.Params["text"] != tc.want[i] || call.Params["chat_id"] != firstNonEmpty(event.GroupID, event.UserID) {
							t.Fatalf("message %d has wrong text or destination", i)
						}
						if kind == EventKindGroup && call.Params["message_thread_id"] != "17" {
							t.Fatal("group topic was lost")
						}
						if kind == EventKindPrivate && call.Params["message_thread_id"] != nil {
							t.Fatal("ordinary private reply acquired a topic")
						}
					}
					if tc.name == "rich text" {
						entities, _ := calls[0].Params["entities"].([]any)
						if len(entities) != 2 {
							t.Fatalf("lost bold/code entities: %v", entities)
						}
					}
				})
			}
		}
	}
}

func TestOneBotAPIsRejectTelegramBeforeAuditOrHTTP(t *testing.T) {
	for _, multi := range []bool{false, true} {
		api := newFakeTelegramAPI(t, nil)
		var channel Channel = api.channel()
		event := MessageEvent{Platform: PlatformTelegram, ProfileID: "tg", Kind: EventKindGroup, GroupID: "-100123", UserID: "10001"}
		if multi {
			channel = NewMultiChannel([]ChannelBinding{{ProfileID: "tg", Platform: PlatformTelegram, Channel: channel}})
		}
		provider := &qualityTestProvider{}
		rt := NewRuntime(BotConfig{ID: "tg", Platform: PlatformTelegram}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
		if _, err := rt.CallOneBotAPI(context.Background(), "get_group_list", nil); err == nil {
			t.Fatal("public OneBot API accepted Telegram")
		}
		if _, err := rt.callOneBotAPIForEvent(context.Background(), event, "send_group_forward_msg", nil); err == nil {
			t.Fatal("event OneBot API accepted Telegram")
		}
		if _, err := rt.sendForwardNodesWithResult(context.Background(), event, buildForwardNodes([]string{"正文"}, "Diana", "42")); err == nil {
			t.Fatal("OneBot forward accepted Telegram")
		}
		if len(api.calls) != 0 || len(provider.requests) != 0 {
			t.Fatalf("unsupported operation reached HTTP/audit: %v %d", api.calls, len(provider.requests))
		}
	}
}

func TestTelegramLengthCheckUsesRenderedText(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		allowed    bool
	}{
		{"boundary", strings.Repeat("字", telegramTextLimit), true},
		{"markup excluded", "**" + strings.Repeat("字", telegramTextLimit) + "**", true},
		{"astral boundary", strings.Repeat("\U0001f600", telegramTextLimit/2), true},
		{"overflow", strings.Repeat("字", telegramTextLimit+1), false},
		{"astral overflow", strings.Repeat("\U0001f600", telegramTextLimit/2+1), false},
		{"code overflow", "```text\n" + strings.Repeat("字", telegramTextLimit+1) + "\n```", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeTelegramAPI(t, nil)
			err := api.channel().Send(context.Background(), OutgoingMessage{UserID: "10001", Text: tc.text})
			if (err == nil) != tc.allowed || (!tc.allowed && len(api.calls) != 0) {
				t.Fatalf("allowed=%v error=%v calls=%d", tc.allowed, err, len(api.calls))
			}
		})
	}
}

func TestOtherPlatformsDoNotAttemptOneBotForward(t *testing.T) {
	withFastSendTiming(t)
	channel := &recordingChannel{}
	provider := &qualityTestProvider{}
	rt := NewRuntime(BotConfig{Platform: PlatformFeishu, ForwardReplyThreshold: 1, SendChunkIntervalMS: 1}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	if _, err := rt.sendDecorated(context.Background(), MessageEvent{Platform: PlatformFeishu, Kind: EventKindGroup, GroupID: "group"}, "第一段"+notificationSplitMarker+"第二段", outboundDecoration{}); err != nil {
		t.Fatal(err)
	}
	if len(channel.sentSnapshot()) != 2 || len(channel.callsSnapshot()) != 0 || len(provider.requests) != 0 {
		t.Fatal("non-OneBot transport entered the forward branch")
	}
}

func TestTelegramTransportCannotBeMislabelledAsOneBot(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	for _, channel := range []Channel{
		api.channel(),
		NewMultiChannel([]ChannelBinding{{ProfileID: "wrong", Platform: PlatformOneBotV11, Channel: api.channel()}}),
	} {
		rt := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
		if _, err := rt.CallOneBotAPI(context.Background(), "get_group_list", nil); err == nil {
			t.Fatal("mislabelled Telegram transport accepted OneBot API")
		}
	}
	if len(api.calls) != 0 {
		t.Fatal("OneBot method reached Telegram HTTP transport")
	}
}

type swappingPlatformChannel struct {
	recordingChannel
	onStatus func()
}

func (c *swappingPlatformChannel) Status() ChannelStatus {
	if c.onStatus != nil {
		change := c.onStatus
		c.onStatus = nil
		change()
	}
	return ChannelStatus{Platform: PlatformOneBotV11}
}

func TestOneBotCallUsesTheValidatedChannelSnapshot(t *testing.T) {
	api := newFakeTelegramAPI(t, nil)
	onebot := &swappingPlatformChannel{}
	rt := NewRuntime(BotConfig{}, onebot, NewPluginManager(), nil, nil, nil, nil)
	onebot.onStatus = func() {
		rt.mu.Lock()
		rt.channel = api.channel()
		rt.mu.Unlock()
	}
	if _, err := rt.callOneBotAPIForEvent(context.Background(), MessageEvent{}, "get_group_list", nil); err != nil {
		t.Fatal(err)
	}
	if len(onebot.callsSnapshot()) != 1 || len(api.calls) != 0 {
		t.Fatal("channel changed between platform validation and API dispatch")
	}
}
