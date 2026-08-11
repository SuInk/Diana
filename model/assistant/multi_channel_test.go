package assistant

import (
	"context"
	"sync"
	"testing"
	"time"
)

type multiChannelProbe struct {
	mu        sync.Mutex
	event     MessageEvent
	sent      []OutgoingMessage
	apiCalls  []string
	apiCalled chan string
	status    ChannelStatus
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

func (c *multiChannelProbe) CallAPI(_ context.Context, action string, _ map[string]any) (map[string]any, error) {
	c.mu.Lock()
	c.apiCalls = append(c.apiCalls, action)
	called := c.apiCalled
	c.mu.Unlock()
	if called != nil {
		called <- action
	}
	return map[string]any{}, nil
}

func (c *multiChannelProbe) Status() ChannelStatus { return c.status }
func (c *multiChannelProbe) Close() error          { return nil }

func TestMultiChannelRoutesRepliesToSourceProfile(t *testing.T) {
	qq := &multiChannelProbe{status: ChannelStatus{Connected: true, SelfID: "qq-bot"}}
	telegram := &multiChannelProbe{status: ChannelStatus{Connected: true, SelfID: "tg-bot"}}
	channel := NewMultiChannel([]ChannelBinding{
		{ProfileID: "qq-profile", Platform: PlatformNapCat, Name: "QQ", Channel: qq},
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

func TestRuntimeRoutesDelayedDeleteToSourceProfile(t *testing.T) {
	first := &multiChannelProbe{apiCalled: make(chan string, 1)}
	second := &multiChannelProbe{apiCalled: make(chan string, 1)}
	channel := NewMultiChannel([]ChannelBinding{
		{ProfileID: "first", Platform: PlatformNapCat, Channel: first},
		{ProfileID: "second", Platform: PlatformNapCat, Channel: second},
	})
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.scheduleMessageDeletes(MessageEvent{ProfileID: "second", Platform: PlatformNapCat}, []string{"42"}, 5*time.Millisecond)

	select {
	case action := <-second.apiCalled:
		if action != "delete_msg" {
			t.Fatalf("second profile action = %q", action)
		}
	case <-time.After(time.Second):
		t.Fatal("source profile did not receive delete_msg")
	}
	select {
	case action := <-first.apiCalled:
		t.Fatalf("first profile received unexpected action %q", action)
	case <-time.After(50 * time.Millisecond):
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
				{ProfileID: "qq-profile", Platform: PlatformNapCat, Channel: qq},
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
			{ID: "qq", Platform: PlatformNapCat, SystemPrompt: "QQ prompt"},
			{ID: "tg", Platform: PlatformTelegram, SystemPrompt: "Telegram prompt", TelegramBotToken: "token"},
		},
	})
	if got := runtime.effectiveConfigForEvent(MessageEvent{ProfileID: "tg"}).SystemPrompt; got != "Telegram prompt" {
		t.Fatalf("Telegram event prompt=%q", got)
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
