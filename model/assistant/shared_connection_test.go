package assistant

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedConnectionResolvesLiveSettingsAndProtectsReferences(t *testing.T) {
	source := DefaultBotConfig()
	source.ID, source.Name = "source", "主连接"
	source.OneBotAccessToken = "source-secret-token"
	source.Enabled = false
	child := DefaultBotConfig()
	child.ID, child.ConnectionProfileID, child.Enabled = "child", source.ID, true
	child.SystemPrompt = "independent persona"
	set := ProfileSet{Profiles: []BotConfig{child, source}}
	if err := set.ValidateConnections(); err != nil {
		t.Fatal(err)
	}
	resolved, err := set.ResolveConnection(set.Profiles[0])
	if err != nil || resolved.OneBotAccessToken != source.OneBotAccessToken || resolved.SystemPrompt != child.SystemPrompt || !resolved.Enabled {
		t.Fatal("source settings or independent behavior lost")
	}
	if set.Profiles[0].OneBotAccessToken != "" {
		t.Fatal("resolution copied credentials into storage")
	}
	source.OneBotAccessToken = "updated-source-token"
	set.Profiles[1] = source
	resolved, err = set.ResolveConnection(child)
	if err != nil || resolved.OneBotAccessToken != source.OneBotAccessToken {
		t.Fatal("source changes not followed")
	}
	payload := PayloadFromConfig(child)
	if ConfigFromPayload(payload, BotConfig{}).ConnectionProfileID != source.ID {
		t.Fatal("reference lost during API round trip")
	}
	if err := set.Delete(source.ID).ValidateConnections(); err == nil {
		t.Fatal("allowed deletion of referenced source")
	}
	for _, test := range []struct {
		name   string
		mutate func(*ProfileSet)
	}{
		{"self", func(s *ProfileSet) { s.Profiles[0].ConnectionProfileID = child.ID }},
		{"missing", func(s *ProfileSet) { s.Profiles[0].ConnectionProfileID = "missing" }},
		{"chain", func(s *ProfileSet) { s.Profiles[1].ConnectionProfileID = child.ID }},
		{"cross platform", func(s *ProfileSet) { s.Profiles[1].Platform = PlatformTelegram }},
		{"missing credentials", func(s *ProfileSet) { s.Profiles[1].OneBotAccessToken = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copySet := set
			copySet.Profiles = append([]BotConfig(nil), set.Profiles...)
			test.mutate(&copySet)
			if copySet.ValidateConnections() == nil {
				t.Fatal("invalid connection accepted")
			}
		})
	}
}

type sharedConnectionProbe struct {
	multiChannelProbe
	connects atomic.Int32
	closes   atomic.Int32
}

func (p *sharedConnectionProbe) Connect(ctx context.Context, handler EventHandler) error {
	p.connects.Add(1)
	return p.multiChannelProbe.Connect(ctx, handler)
}
func (p *sharedConnectionProbe) Close() error { p.closes.Add(1); return nil }

func TestSharedConnectionConnectsOnceAndRoutesEachProfile(t *testing.T) {
	probe := &sharedConnectionProbe{multiChannelProbe: multiChannelProbe{event: MessageEvent{Kind: EventKindGroup, MessageID: "42", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "original"}}}}, status: ChannelStatus{Connected: true}}}
	channel := NewMultiChannel([]ChannelBinding{
		{ProfileID: "a", Platform: PlatformOneBotV11, ConnectionID: "shared", Channel: probe},
		{ProfileID: "b", Platform: PlatformOneBotV11, ConnectionID: "shared", Channel: probe},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan MessageEvent, 2)
	done := make(chan error, 1)
	go func() {
		done <- channel.Connect(ctx, func(_ context.Context, event MessageEvent) error {
			events <- event
			if event.ProfileID == "a" {
				event.Segments[0].Data["text"] = "changed"
				return errors.New("first handler fails")
			}
			return nil
		})
	}()
	for _, id := range []string{"a", "b"} {
		select {
		case event := <-events:
			if event.ProfileID != id || event.ContextNamespace != id {
				t.Fatalf("wrong route: %+v", event)
			}
			if id == "b" && event.Segments[0].Data["text"] != "original" {
				t.Fatal("events share mutable data")
			}
		case <-time.After(time.Second):
			t.Fatal("missing shared delivery")
		}
	}
	for _, id := range []string{"a", "b"} {
		if err := channel.Send(ctx, OutgoingMessage{ProfileID: id, Text: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := channel.Send(ctx, OutgoingMessage{Platform: PlatformOneBotV11, Text: "platform routing"}); err != nil {
		t.Fatal(err)
	}
	if len(probe.sent) != 3 || len(channel.ChannelStatuses()) != 2 {
		t.Fatal("missing replies or per-profile status")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection did not stop")
	}
	if probe.connects.Load() != 1 || probe.closes.Load() != 1 {
		t.Fatalf("connect=%d close=%d", probe.connects.Load(), probe.closes.Load())
	}
}

func TestSharedConnectionPrivateAdmissionUsesEachProfile(t *testing.T) {
	source := DefaultBotConfig()
	source.ID, source.OwnerID = "source", "owner-a"
	source.PrivateAdmission.Mode = "owner_only"
	child := source
	child.ID, child.OwnerID, child.ConnectionProfileID = "child", "owner-b", source.ID
	runtime := NewRuntime(source, &multiChannelProbe{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{source, child}})
	if runtime.privateAdmissionAllows(MessageEvent{Kind: EventKindPrivate, ProfileID: child.ID, UserID: source.OwnerID}) {
		t.Fatal("child accepted source owner")
	}
	if !runtime.privateAdmissionAllows(MessageEvent{Kind: EventKindPrivate, ProfileID: child.ID, UserID: child.OwnerID}) {
		t.Fatal("child rejected own owner")
	}
}

func TestIndependentWebSocketConnectionConflicts(t *testing.T) {
	source := DefaultBotConfig()
	source.ID, source.Name, source.OneBotTransport = "source", "主机器人", OneBotTransportForwardWS
	source.OneBotWSEndpoint, source.Enabled = "ws://HOST:80", false
	set := ProfileSet{Profiles: []BotConfig{source}}
	for _, endpoint := range []string{"ws://host", " ws://host/ ", "ws://HOST:80/#ignored"} {
		draft := source
		draft.ID = "new"
		draft.OneBotWSEndpoint = endpoint
		draft.OneBotAccessToken = "different"
		if set.ValidateIndependentConnection(draft) == nil {
			t.Errorf("accepted duplicate: %q", endpoint)
		}
	}
	if err := set.ValidateIndependentConnection(source); err != nil {
		t.Fatal("self conflicts", err)
	}
	child := source
	child.ID, child.ConnectionProfileID = "child", source.ID
	if err := set.ValidateIndependentConnection(child); err != nil {
		t.Fatal("explicit reuse conflicts", err)
	}
	for _, endpoint := range []string{"wss://host/", "ws://host:81/", "ws://host/events", "ws://host/?account=2", "not a url", ""} {
		draft := source
		draft.ID = "new"
		draft.OneBotWSEndpoint = endpoint
		if err := set.ValidateIndependentConnection(draft); err != nil {
			t.Errorf("false conflict for %q: %v", endpoint, err)
		}
	}
	source.OneBotTransport, source.OneBotReverseWSEndpoint = OneBotTransportReverseWS, "wss://HOST:443/onebot/v11/ws"
	set.Profiles[0] = source
	draft := source
	draft.ID, draft.OneBotReverseWSEndpoint = "new", "wss://host/onebot/v11/ws"
	if set.ValidateIndependentConnection(draft) == nil {
		t.Fatal("accepted duplicate reverse WS")
	}
	draft.OneBotTransport, draft.OneBotWSEndpoint = OneBotTransportForwardWS, source.OneBotReverseWSEndpoint
	if err := set.ValidateIndependentConnection(draft); err != nil {
		t.Fatal("different modes conflict", err)
	}
}
