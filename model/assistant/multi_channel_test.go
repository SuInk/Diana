// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type multiChannelProbe struct {
	mu     sync.Mutex
	event  MessageEvent
	sent   []OutgoingMessage
	status ChannelStatus
}

func (c *multiChannelProbe) Connect(ctx context.Context, handler EventHandler) error {
	if c.event.Kind != "" {
		_ = handler(ctx, c.event)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (c *multiChannelProbe) Send(_ context.Context, msg OutgoingMessage) error {
	c.mu.Lock()
	c.sent = append(c.sent, msg)
	c.mu.Unlock()
	return nil
}

func (c *multiChannelProbe) CallAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}

func (c *multiChannelProbe) Status() ChannelStatus { return c.status }
func (c *multiChannelProbe) Close() error          { return nil }

type reconnectingChannelProbe struct {
	multiChannelProbe
	mu       sync.Mutex
	attempts int
	started  chan int
}

func (c *reconnectingChannelProbe) Connect(ctx context.Context, _ EventHandler) error {
	c.mu.Lock()
	c.attempts++
	attempt := c.attempts
	c.mu.Unlock()
	c.started <- attempt
	if attempt == 1 {
		return errors.New("initial handshake failed")
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestMultiChannelRetriesFailedBindingUntilItRecovers(t *testing.T) {
	flaky := &reconnectingChannelProbe{started: make(chan int, 2)}
	channel := NewMultiChannel([]ChannelBinding{{
		ProfileID: "telegram-profile",
		Platform:  PlatformTelegram,
		Name:      "Telegram",
		Channel:   flaky,
	}})
	channel.reconnectInitialDelay = 5 * time.Millisecond
	channel.reconnectMaxDelay = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- channel.Connect(ctx, func(context.Context, MessageEvent) error { return nil })
	}()

	for want := 1; want <= 2; want++ {
		select {
		case got := <-flaky.started:
			if got != want {
				t.Fatalf("connect attempt = %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("connect attempt %d did not start", want)
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Connect error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("multi channel did not stop after recovery")
	}
}

func TestMultiChannelRoutesRepliesToSourceProfile(t *testing.T) {
	oneBot := &multiChannelProbe{status: ChannelStatus{Connected: true, SelfID: "onebot"}}
	telegram := &multiChannelProbe{status: ChannelStatus{Connected: true, SelfID: "tg-bot"}}
	channel := NewMultiChannel([]ChannelBinding{
		{ProfileID: "onebot-profile", Platform: PlatformOneBotV11, Name: "OneBot", Channel: oneBot},
		{ProfileID: "tg-profile", Platform: PlatformTelegram, Name: "Telegram", Channel: telegram},
	})

	if err := channel.Send(context.Background(), OutgoingMessage{ProfileID: "tg-profile", Platform: PlatformTelegram, UserID: "42", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if len(oneBot.sent) != 0 || len(telegram.sent) != 1 || telegram.sent[0].Text != "hello" {
		t.Fatalf("onebot sent=%#v telegram sent=%#v", oneBot.sent, telegram.sent)
	}
	statuses := channel.ChannelStatuses()
	if len(statuses) != 2 || statuses[0].ProfileID != "onebot-profile" || statuses[1].Platform != PlatformTelegram {
		t.Fatalf("statuses=%#v", statuses)
	}
}

func TestMultiChannelAlwaysIsolatesConversationKeys(t *testing.T) {
	tests := []struct {
		name      string
		platform  string
		kind      EventKind
		namespace string
	}{
		{name: "cross-platform groups", platform: PlatformTelegram, kind: EventKindGroup},
		{name: "same-platform groups", platform: PlatformOneBotV11, kind: EventKindGroup},
		{name: "cross-platform private chats", platform: PlatformTelegram, kind: EventKindPrivate},
		{name: "same-platform private chats", platform: PlatformOneBotV11, kind: EventKindPrivate},
		{name: "shared namespace cannot bypass isolation", platform: PlatformTelegram, kind: EventKindGroup, namespace: "shared"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oneBot := &multiChannelProbe{event: MessageEvent{Kind: tt.kind, GroupID: "100", UserID: "200", MessageID: "first-message", ContextNamespace: tt.namespace}}
			other := &multiChannelProbe{event: MessageEvent{Kind: tt.kind, GroupID: "100", UserID: "200", MessageID: "second-message", ContextNamespace: tt.namespace}}
			channel := NewMultiChannel([]ChannelBinding{
				{ProfileID: "onebot-profile", Platform: PlatformOneBotV11, Channel: oneBot},
				{ProfileID: "other-profile", Platform: tt.platform, Channel: other},
			})

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			events := make(chan MessageEvent, 2)
			done := make(chan error, 1)
			go func() {
				done <- channel.Connect(ctx, func(_ context.Context, event MessageEvent) error {
					events <- event
					return nil
				})
			}()
			received := make([]MessageEvent, 0, 2)
			for len(received) < 2 {
				select {
				case event := <-events:
					received = append(received, event)
				case <-ctx.Done():
					t.Fatal("channels did not deliver both events")
				}
			}
			first, second := received[0], received[1]
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("multi channel did not stop")
			}
			if first.ProfileID == second.ProfileID || first.ContextNamespace != first.ProfileID || second.ContextNamespace != second.ProfileID {
				t.Fatalf("source metadata missing: %#v %#v", first, second)
			}
			if sessionKey(first) == sessionKey(second) {
				t.Fatalf("conversation keys must differ: %q and %q", sessionKey(first), sessionKey(second))
			}
		})
	}
}

func TestRuntimeBindsConversationNamespaceToSourceProfile(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "qq", Platform: PlatformOneBotV11}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "qq", Platform: PlatformOneBotV11, Channel: &recordingChannel{}},
		{ProfileID: "tg", Platform: PlatformTelegram, Channel: &recordingChannel{}},
	}), NewPluginManager(), nil, nil, nil, nil)
	for _, namespace := range []string{"", "shared", "qq", "tg"} {
		event := runtime.bindInboundEventIdentity(MessageEvent{
			Kind: EventKindGroup, GroupID: "100", UserID: "200",
			Platform: PlatformTelegram, ProfileID: "tg", ContextNamespace: namespace,
		})
		if event.ContextNamespace != "tg" || event.ProfileID != "tg" || event.Platform != PlatformTelegram {
			t.Fatalf("namespace %q changed source identity: %#v", namespace, event)
		}
	}
}

func TestRuntimeUsesSourceProfileConfiguration(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "qq", SystemPrompt: "QQ prompt"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{
		ActiveID: "qq",
		Profiles: []BotConfig{
			{ID: "qq", Platform: PlatformOneBotV11, SystemPrompt: "QQ prompt"},
			{ID: "tg", Platform: PlatformTelegram, SystemPrompt: "Telegram prompt", TelegramBotToken: "token"},
		},
	})
	if got := runtime.effectiveConfigForEvent(MessageEvent{ProfileID: "tg"}).SystemPrompt; got != "Telegram prompt" {
		t.Fatalf("Telegram event prompt=%q", got)
	}
}

func TestRuntimeRoutesOneBotMediaCallsToSourceProfile(t *testing.T) {
	first := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_image": {"url": "https://example.com/first.png"},
	}}
	second := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_image": {"url": "https://example.com/second.png"},
	}}
	multi := NewMultiChannel([]ChannelBinding{
		{ProfileID: "onebot-first", Platform: PlatformOneBotV11, Channel: first},
		{ProfileID: "onebot-second", Platform: PlatformOneBotV11, Channel: second},
	})
	runtime := NewRuntime(BotConfig{ID: "onebot-second", Platform: PlatformOneBotV11}, multi, NewPluginManager(), nil, nil, nil, nil)

	event := runtime.enrichMediaReferences(context.Background(), MessageEvent{
		ProfileID: "onebot-first",
		Platform:  PlatformOneBotV11,
		MessageID: "image-message",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"file": "image-token",
		}}},
	})
	if got := event.Segments[0].Data[imageResolvedSourceKey+"1"]; got != "https://example.com/first.png" {
		t.Fatalf("resolved source = %q, segments=%#v", got, event.Segments)
	}
	if firstCalls, secondCalls := first.callsSnapshot(), second.callsSnapshot(); len(firstCalls) != 1 || firstCalls[0].action != "get_image" || len(secondCalls) != 0 {
		t.Fatalf("first calls=%#v second calls=%#v", firstCalls, secondCalls)
	}
}

func TestRuntimeRoutesGroupLookupsAndMemberCacheToSourceProfile(t *testing.T) {
	first := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_info":        {"group_id": "100", "group_name": "first group"},
		"get_group_member_info": {"group_id": "100", "user_id": "42", "level": "7"},
	}}
	second := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_info":        {"group_id": "100", "group_name": "second group"},
		"get_group_member_info": {"group_id": "100", "user_id": "42", "level": "3"},
	}}
	runtime := NewRuntime(BotConfig{ID: "onebot-second", Platform: PlatformOneBotV11}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "onebot-first", Platform: PlatformOneBotV11, Channel: first},
		{ProfileID: "onebot-second", Platform: PlatformOneBotV11, Channel: second},
	}), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind:      EventKindGroup,
		ProfileID: "onebot-first",
		Platform:  PlatformOneBotV11,
		GroupID:   "100",
		UserID:    "42",
	}

	group, err := runtime.getGroupInfoForEvent(context.Background(), event, event.GroupID)
	if err != nil || group.GroupName != "first group" {
		t.Fatalf("source group=%#v err=%v", group, err)
	}
	if _, ok := runtime.members.LevelFor(event); ok {
		t.Fatal("首次成员缓存查询应返回不可信")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if info, ok := runtime.members.lookup(event.GroupID, event.UserID); ok && info.Level == 7 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if info, ok := runtime.members.lookup(event.GroupID, event.UserID); !ok || info.Level != 7 {
		t.Fatalf("source member cache=%#v ok=%v", info, ok)
	}
	firstCalls, secondCalls := first.callsSnapshot(), second.callsSnapshot()
	if len(firstCalls) != 2 || firstCalls[0].action != "get_group_info" || firstCalls[1].action != "get_group_member_info" || len(secondCalls) != 0 {
		t.Fatalf("first calls=%#v second calls=%#v", firstCalls, secondCalls)
	}
}

func TestRuntimeRoutesGlobalOneBotCallToCurrentProfile(t *testing.T) {
	first := &recordingChannel{apiResponses: map[string]map[string]any{"get_status": {"profile": "first"}}}
	second := &recordingChannel{apiResponses: map[string]map[string]any{"get_status": {"profile": "second"}}}
	runtime := NewRuntime(BotConfig{ID: "onebot-second", Platform: PlatformOneBotV11}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "onebot-first", Platform: PlatformOneBotV11, Channel: first},
		{ProfileID: "onebot-second", Platform: PlatformOneBotV11, Channel: second},
	}), NewPluginManager(), nil, nil, nil, nil)

	status, err := runtime.CallOneBotAPI(context.Background(), "get_status", nil)
	if err != nil || status["profile"] != "second" {
		t.Fatalf("current profile status=%#v err=%v", status, err)
	}
	if len(first.callsSnapshot()) != 0 || len(second.callsSnapshot()) != 1 {
		t.Fatalf("first calls=%#v second calls=%#v", first.callsSnapshot(), second.callsSnapshot())
	}
}

func TestRuntimeGlobalOneBotCallFallsBackFromTelegramProfile(t *testing.T) {
	onebot := &recordingChannel{apiResponses: map[string]map[string]any{"get_status": {"profile": "qq"}}}
	telegram := &recordingChannel{}
	runtime := NewRuntime(BotConfig{ID: "tg", Platform: PlatformTelegram}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "qq", Platform: PlatformOneBotV11, Channel: onebot},
		{ProfileID: "tg", Platform: PlatformTelegram, Channel: telegram},
	}), NewPluginManager(), nil, nil, nil, nil)

	status, err := runtime.CallOneBotAPI(context.Background(), "get_status", nil)
	if err != nil || status["profile"] != "qq" {
		t.Fatalf("fallback status=%#v err=%v", status, err)
	}
	if len(onebot.callsSnapshot()) != 1 || len(telegram.callsSnapshot()) != 0 {
		t.Fatalf("onebot calls=%#v telegram calls=%#v", onebot.callsSnapshot(), telegram.callsSnapshot())
	}
}

func TestReminderSourceKeepsChannelRoute(t *testing.T) {
	event := reminderSourceEvent(Reminder{
		Platform:         PlatformTelegram,
		ProfileID:        "tg-profile",
		ContextNamespace: "tg-profile",
		GroupID:          "100",
		UserID:           "200",
	})
	if event.Platform != PlatformTelegram || event.ProfileID != "tg-profile" || event.ContextNamespace != "tg-profile" {
		t.Fatalf("reminder source=%#v", event)
	}
	if event.Kind != EventKindGroup || event.GroupID != "100" || event.UserID != "200" {
		t.Fatalf("reminder target=%#v", event)
	}
}
