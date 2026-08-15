package assistant

import (
	"context"
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

func TestMultiChannelRoutesRepliesToSourceProfile(t *testing.T) {
	qq := &multiChannelProbe{status: ChannelStatus{Connected: true, SelfID: "qq-bot"}}
	telegram := &multiChannelProbe{status: ChannelStatus{Connected: true, SelfID: "tg-bot"}}
	channel := NewMultiChannel([]ChannelBinding{
		{ProfileID: "qq-profile", Platform: PlatformOneBotV11, Name: "QQ", Channel: qq},
		{ProfileID: "tg-profile", Platform: PlatformTelegram, Name: "Telegram", Channel: telegram},
	})

	if err := channel.Send(context.Background(), OutgoingMessage{ProfileID: "tg-profile", Platform: PlatformTelegram, UserID: "42", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if len(qq.sent) != 0 || len(telegram.sent) != 1 || telegram.sent[0].Text != "hello" {
		t.Fatalf("qq sent=%#v telegram sent=%#v", qq.sent, telegram.sent)
	}
	statuses := channel.ChannelStatuses()
	if len(statuses) != 2 || statuses[0].ProfileID != "qq-profile" || statuses[1].Platform != PlatformTelegram {
		t.Fatalf("statuses=%#v", statuses)
	}
}

func TestMultiChannelCanIsolateOrShareConversationKeys(t *testing.T) {
	tests := []struct {
		name     string
		isolate  bool
		wantSame bool
	}{
		{name: "isolated by default", isolate: true, wantSame: false},
		{name: "shared when disabled", isolate: false, wantSame: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qq := &multiChannelProbe{event: MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "200", MessageID: "qq-message"}}
			tg := &multiChannelProbe{event: MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "200", MessageID: "tg-message"}}
			channel := NewMultiChannel([]ChannelBinding{
				{ProfileID: "qq-profile", Platform: PlatformOneBotV11, Channel: qq},
				{ProfileID: "tg-profile", Platform: PlatformTelegram, Channel: tg},
			}, tt.isolate)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events := make(chan MessageEvent, 2)
			done := make(chan error, 1)
			go func() {
				done <- channel.Connect(ctx, func(_ context.Context, event MessageEvent) error {
					events <- event
					return nil
				})
			}()
			first := <-events
			second := <-events
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("multi channel did not stop")
			}
			if first.Platform == second.Platform || first.ProfileID == second.ProfileID {
				t.Fatalf("source metadata missing: %#v %#v", first, second)
			}
			if gotSame := sessionKey(first) == sessionKey(second); gotSame != tt.wantSame {
				t.Fatalf("session keys %q and %q, same=%v want %v", sessionKey(first), sessionKey(second), gotSame, tt.wantSame)
			}
		})
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
		{ProfileID: "qq-first", Platform: PlatformOneBotV11, Channel: first},
		{ProfileID: "qq-second", Platform: PlatformOneBotV11, Channel: second},
	})
	runtime := NewRuntime(BotConfig{ID: "qq-second", Platform: PlatformOneBotV11}, multi, NewPluginManager(), nil, nil, nil, nil)

	event := runtime.enrichMediaReferences(context.Background(), MessageEvent{
		ProfileID: "qq-first",
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
	runtime := NewRuntime(BotConfig{ID: "qq-second", Platform: PlatformOneBotV11}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "qq-first", Platform: PlatformOneBotV11, Channel: first},
		{ProfileID: "qq-second", Platform: PlatformOneBotV11, Channel: second},
	}), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind:      EventKindGroup,
		ProfileID: "qq-first",
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
	runtime := NewRuntime(BotConfig{ID: "qq-second", Platform: PlatformOneBotV11}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "qq-first", Platform: PlatformOneBotV11, Channel: first},
		{ProfileID: "qq-second", Platform: PlatformOneBotV11, Channel: second},
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
