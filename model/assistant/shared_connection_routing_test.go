package assistant

import (
	"context"
	"strings"
	"testing"
)

func sharedRoutingRuntime(t *testing.T) *Runtime {
	t.Helper()
	a := DefaultBotConfig()
	a.ID = "main"
	a.Enabled = true
	a.Name = "然然"
	a.BotAccount = "10001"
	a.GroupTriggers = []string{"嘉然"}
	a.OneBotAccessToken = "test"
	b := a
	b.ID = "whale"
	b.Name = "鲸鱼娘"
	b.GroupTriggers = []string{"大肥鱼"}
	b.ConnectionProfileID = a.ID
	b.ConnectionReplyPrefix = true
	r := NewRuntime(a, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{b, a}})
	return r
}
func sharedRoutingEvent(id, text string) MessageEvent {
	return MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, ProfileID: "main", ContextNamespace: "main", SelfID: "10001", GroupID: "g", UserID: "u", MessageID: id, RawMessage: text, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}}
}
func assertSharedRecipient(t *testing.T, r *Runtime, e MessageEvent, want string) {
	t.Helper()
	selected := []string{}
	for _, id := range []string{"main", "whale"} {
		e.ProfileID = id
		e.ContextNamespace = id
		out, ok := r.routeSharedGroupEvent(context.Background(), e)
		if ok {
			selected = append(selected, id)
			if want == "whale" && !out.ToMe {
				t.Fatal("selected named/quoted role was not directly addressed")
			}
		}
	}
	if want == "" {
		if len(selected) != 0 {
			t.Fatalf("unexpected recipients: %v", selected)
		}
		return
	}
	if len(selected) != 1 || selected[0] != want {
		t.Fatalf("recipients=%v want=%s", selected, want)
	}
}
func TestSharedRoutingNamesOverrideAccountMentionAndQuotes(t *testing.T) {
	r := sharedRoutingRuntime(t)
	e := sharedRoutingEvent("1", "鲸鱼娘，出来吃饭")
	e.ToMe = true
	e.Quoted = &QuotedMessage{UserID: "10001", MessageID: "main-reply"}
	assertSharedRecipient(t, r, e, "whale")
	assertSharedRecipient(t, r, sharedRoutingEvent("2", "大肥鱼 在吗"), "whale")
	assertSharedRecipient(t, r, sharedRoutingEvent("3", "嘉然 在吗"), "main")
	e = sharedRoutingEvent("4", "在吗")
	e.ToMe = true
	assertSharedRecipient(t, r, e, "main")
}
func TestSharedRoutingQuoteAndDefaultScope(t *testing.T) {
	r := sharedRoutingRuntime(t)
	source := sharedRoutingEvent("original", "大肥鱼 在吗")
	source.ProfileID = "whale"
	r.rememberSharedReply(source, OutgoingMessage{ProfileID: "whale", GroupID: "g", Text: "来了"}, "whale-reply")
	e := sharedRoutingEvent("quote", "为什么")
	e.ToMe = true
	e.Quoted = &QuotedMessage{UserID: "10001", MessageID: "whale-reply"}
	assertSharedRecipient(t, r, e, "whale")
	assertSharedRecipient(t, r, sharedRoutingEvent("follow", "然后呢"), "main")
	e = sharedRoutingEvent("bare-at", "在吗")
	e.ToMe = true
	assertSharedRecipient(t, r, e, "main")
	e = sharedRoutingEvent("other-user", "然后呢")
	e.UserID = "other"
	assertSharedRecipient(t, r, e, "main")
	e = sharedRoutingEvent("other-group", "然后呢")
	e.GroupID = "other"
	assertSharedRecipient(t, r, e, "main")

}
func TestSharedRoutingDisabledNamedRoleDoesNotFallBack(t *testing.T) {
	r := sharedRoutingRuntime(t)
	r.mu.Lock()
	r.disabledProfiles["whale"] = true
	r.mu.Unlock()
	assertSharedRecipient(t, r, sharedRoutingEvent("named", "大肥鱼 在吗"), "")
	r.mu.Lock()
	delete(r.disabledProfiles, "whale")
	r.disabledProfiles["main"] = true
	r.mu.Unlock()
	e := sharedRoutingEvent("default", "在吗")
	e.ToMe = true
	assertSharedRecipient(t, r, e, "whale")
}
func TestSharedRoutingPrefixAndPayloadRoundTrip(t *testing.T) {
	r := sharedRoutingRuntime(t)
	cfg := r.ProfileConfig("whale")
	if !ConfigFromPayload(PayloadFromConfig(cfg), BotConfig{}).ConnectionReplyPrefix {
		t.Fatal("prefix setting lost")
	}
	msg := OutgoingMessage{ProfileID: "whale", GroupID: "g", Text: "本鲸来了"}
	got := r.sharedReplyPrefix(msg)
	if got.Text != "【鲸鱼娘】本鲸来了" {
		t.Fatal(got.Text)
	}
	if r.sharedReplyPrefix(got).Text != got.Text {
		t.Fatal("duplicated prefix")
	}
	msg.GroupID = ""
	if r.sharedReplyPrefix(msg).Text != msg.Text {
		t.Fatal("private prefix")
	}
	msg.GroupID = "g"
	msg.ProfileID = "main"
	if r.sharedReplyPrefix(msg).Text != msg.Text {
		t.Fatal("disabled prefix")
	}
}

type sharedLookupStore struct {
	*memoryInboundEventStore
	profile string
	calls   int
}

func (s *sharedLookupStore) LookupOutboundMessageProfile(_ context.Context, _ []string, group, message string) (string, error) {
	s.calls++
	if group == "g" && message == "persisted" {
		return s.profile, nil
	}
	return "", nil
}
func TestSharedRoutingQuoteUsesDurableStoreAfterRestart(t *testing.T) {
	r := sharedRoutingRuntime(t)
	store := &sharedLookupStore{memoryInboundEventStore: newMemoryInboundEventStore(), profile: "whale"}
	r.SetInboundEventStore(store)
	e := sharedRoutingEvent("incoming", "这个呢")
	e.Quoted = &QuotedMessage{UserID: "10001", MessageID: "persisted"}
	e.ToMe = true
	assertSharedRecipient(t, r, e, "whale")
	segmentEvent := sharedRoutingEvent("raw-quote", "这个呢")
	segmentEvent.Segments = append([]MessageSegment{{Type: "reply", Data: map[string]string{"id": "persisted"}}}, segmentEvent.Segments...)
	assertSharedRecipient(t, r, segmentEvent, "whale")
	if store.calls != 2 {
		t.Fatalf("lookup calls=%d", store.calls)
	}
}
func TestSharedRoutingPassiveProfileKeepsContextWithoutEnqueue(t *testing.T) {
	r := sharedRoutingRuntime(t)
	store := newMemoryInboundEventStore()
	r.SetInboundEventStore(store)
	e := sharedRoutingEvent("passive", "鲸鱼娘 在吗")
	if err := r.HandleEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	r.mu.RLock()
	history := append([]MessageEvent(nil), r.history[sessionKey(e)]...)
	r.mu.RUnlock()
	if len(history) != 1 || history[0].MessageID != e.MessageID {
		t.Fatalf("missing passive context: %v", history)
	}
	// The passive role never registers a durable reply job.
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.records) > 0 {
		t.Fatal("passive role enqueued a reply")
	}
}

type sharedReplayProvider struct{ LLMProvider }

// Uses current configured live model; delivery stays in the recording channel.
func TestLiveSharedConnectionRouting(t *testing.T) {
	client := liveLLMClient(t)
	withFastSendTiming(t)
	base := sharedRoutingRuntime(t)
	a, b := base.ProfileConfig("main"), base.ProfileConfig("whale")
	b.SystemPrompt = mustBuiltinSoul("whalegirl.md")
	a.AgentEnabled, b.AgentEnabled = false, false
	channel := &recordingChannel{}
	r := NewRuntime(a, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return sharedReplayProvider{LLMProvider: client}, nil })
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{a, b}})
	for _, tc := range []struct {
		id, text, want string
		quote          bool
	}{
		{"name-at", "大肥鱼太出圈了", "whale", false},
		{"bare-at", "在吗，今天吃什么？", "main", false},
		{"quote", "那你要几碗白饭？", "whale", true},
	} {
		e := sharedRoutingEvent(tc.id, tc.text)
		e.ToMe = true
		if tc.quote {
			e.Quoted = &QuotedMessage{UserID: "10001", MessageID: "whale-live", RawMessage: "本鲸要白饭"}
		}
		assertSharedRecipient(t, r, e, tc.want)
		e.ProfileID, e.ContextNamespace = tc.want, tc.want
		selected, ok := r.routeSharedGroupEvent(context.Background(), e)
		if !ok {
			t.Fatal("missing selection")
		}
		before := len(channel.sentSnapshot())
		_, err := r.replyTo(context.Background(), selected, selected.RawMessage)
		if err != nil {
			t.Fatalf("live reply failed: %v", err)
		}
		sent := channel.sentSnapshot()[before:]
		if len(sent) == 0 {
			t.Fatal("no recorded delivery")
		}
		for _, msg := range sent {
			if msg.ProfileID != tc.want {
				t.Fatalf("wrong sender %s", msg.ProfileID)
			}
			if tc.want == "whale" && !strings.HasPrefix(msg.Text, "【鲸鱼娘】") {
				t.Fatal("missing role prefix")
			}
			t.Logf("ROUTING_REPLAY input=%q role=%s output=%q", tc.text, tc.want, msg.Text)
		}
		if tc.want == "whale" {
			r.rememberSharedReply(selected, sent[0], "whale-live")
		}
	}
}
