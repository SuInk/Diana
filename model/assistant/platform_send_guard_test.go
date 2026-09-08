package assistant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPluginDeliveryUsesTargetPlatform(t *testing.T) {
	for _, platform := range []string{PlatformTelegram, PlatformQQOfficial, PlatformDingTalk, PlatformFeishu, PlatformWeCom} {
		t.Run(platform, func(t *testing.T) {
			qq := &failingOutboundChannel{groupIDs: []string{"qq-other-group"}}
			target := &recordingChannel{}
			multi := NewMultiChannel([]ChannelBinding{
				{ProfileID: "qq", Platform: PlatformOneBotV11, Channel: qq},
				{ProfileID: "target", Platform: platform, Channel: target},
			})
			r := NewRuntime(BotConfig{Platform: PlatformOneBotV11}, multi, NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: EventKindGroup, ProfileID: "target", Platform: platform, GroupID: "-1004402809405", UserID: "42", SelfID: "99"}
			response := PluginResponse{ForwardMessages: []OutgoingMessage{{Text: "ranking"}, {Text: "details", ImageURLs: []string{"https://example.com/image.png"}}}}
			if err := r.sendForwardPluginResponse(context.Background(), event, response, r.Config()); err != nil {
				t.Fatal(err)
			}
			if err := r.sendDirectPluginResponse(context.Background(), event, "video", nil, []string{"https://example.com/video.mp4"}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.sendNestedForwardPluginResponse(context.Background(), event, response, "summary", r.Config()); err != nil {
				t.Fatal(err)
			}
			if _, err := r.sendRealForwardMessages(context.Background(), event, response.ForwardMessages, r.Config()); err == nil {
				t.Fatal("unsupported raw forward accepted")
			}
			if err := r.send(context.Background(), event, "next reply"); err != nil {
				t.Fatalf("subsequent reply blocked: %v", err)
			}
			sent := target.sentSnapshot()
			if len(sent) < 5 || sent[0].Text != "ranking" || sent[len(sent)-1].Text != "next reply" {
				t.Fatalf("lost plugin response: %#v", sent)
			}
			for _, msg := range sent {
				if msg.ProfileID != "target" || msg.Platform != platform || msg.GroupID != event.GroupID {
					t.Fatalf("wrong destination: %#v", msg)
				}
			}
			if len(target.callsSnapshot()) != 0 || qq.groupListAttempts() != 0 || qq.sendAttempts() != 0 {
				t.Fatal("non-OneBot response reached OneBot API")
			}
		})
	}
}

func TestNonOneBotFailureDoesNotUseQQMembership(t *testing.T) {
	for _, platform := range []string{PlatformTelegram, PlatformQQOfficial, PlatformDingTalk, PlatformFeishu, PlatformWeCom} {
		t.Run(platform, func(t *testing.T) {
			qq := &failingOutboundChannel{groupIDs: []string{"other"}}
			failure := errors.New("native send failed")
			target := &failingOutboundChannel{err: failure}
			r := NewRuntime(BotConfig{}, NewMultiChannel([]ChannelBinding{
				{ProfileID: "qq", Platform: PlatformOneBotV11, Channel: qq},
				{ProfileID: "native", Platform: platform, Channel: target},
			}), NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: EventKindGroup, ProfileID: "native", Platform: platform, GroupID: "123"}
			err := r.send(context.Background(), event, "test")
			if !errors.Is(err, failure) || errors.Is(err, errGroupSendUnavailable) || r.blockedGroupSendError(event) != nil {
				t.Fatalf("native error misclassified: %v", err)
			}
			if qq.groupListAttempts()+target.groupListAttempts() != 0 {
				t.Fatal("queried OneBot membership for another platform")
			}
		})
	}
}

func TestGroupSendStateIsScopedToBot(t *testing.T) {
	a := &failingOutboundChannel{err: errors.New("send failed"), groupIDs: []string{"other"}}
	b := &failingOutboundChannel{groupIDs: []string{"same"}}
	r := NewRuntime(BotConfig{}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "a", Platform: PlatformOneBotV11, Channel: a},
		{ProfileID: "b", Platform: PlatformOneBotV11, Channel: b},
	}), NewPluginManager(), nil, nil, nil, nil)
	eventA := MessageEvent{Kind: EventKindGroup, ProfileID: "a", Platform: PlatformOneBotV11, GroupID: "same"}
	eventB := eventA
	eventB.ProfileID = "b"
	r.proactiveBatches = map[string]*proactiveReplyBatch{
		"a": {items: []proactiveReplyCandidate{{Event: eventA}}},
		"b": {items: []proactiveReplyCandidate{{Event: eventB}}},
	}
	if err := r.send(context.Background(), eventA, "first"); !errors.Is(err, errGroupSendUnavailable) {
		t.Fatalf("missing group not blocked: %v", err)
	}
	if err := r.send(context.Background(), eventB, "second"); err != nil {
		t.Fatalf("another bot blocked: %v", err)
	}
	if a.groupListAttempts() != 1 || b.groupListAttempts() != 0 {
		t.Fatal("membership queried on wrong bot")
	}
	b.err = errors.New("temporary send failure")
	if err := r.send(context.Background(), eventB, "retry later"); !errors.Is(err, errOutboundSend) || errors.Is(err, errGroupSendUnavailable) {
		t.Fatalf("used another bot's membership list: %v", err)
	}
	if a.groupListAttempts() != 1 || b.groupListAttempts() != 1 {
		t.Fatal("membership did not follow the failing bot")
	}
	if r.proactiveBatches["a"] != nil || r.proactiveBatches["b"] == nil {
		t.Fatal("group block canceled another bot's queued reply")
	}
	if r.groupOutboundDelivery(eventA) == r.groupOutboundDelivery(eventB) {
		t.Fatal("bots share backoff gate")
	}
	eventB.Time = time.Now().Add(time.Second).Unix()
	if r.ignoreUnavailableGroupEvent(eventB) || r.blockedGroupSendError(eventA) == nil {
		t.Fatal("another bot cleared the block")
	}
}

func TestTelegramForwardPluginUsesNativeAPI(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{
		"sendMessage": map[string]any{"message_id": 101},
		"sendPhoto":   map[string]any{"message_id": 102},
		"sendVideo":   map[string]any{"message_id": 103},
	})
	qq := &failingOutboundChannel{groupIDs: []string{"unrelated-qq-group"}}
	r := NewRuntime(BotConfig{}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "qq", Platform: PlatformOneBotV11, Channel: qq},
		{ProfileID: "tg", Platform: PlatformTelegram, Channel: api.channel()},
	}), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "tg", Platform: PlatformTelegram, GroupID: "-1004402809405", UserID: "42"}
	response := PluginResponse{Forward: true, ForwardMessages: []OutgoingMessage{
		{Text: "relationship ranking"},
		{ImageURLs: []string{"https://example.com/ranking.png"}},
	}}
	if _, err := r.deliverResolverResponse(context.Background(), event, response); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(video, []byte("test video"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.sendDirectPluginResponse(context.Background(), event, "", nil, []string{video}); err != nil {
		t.Fatalf("native local video upload failed: %v", err)
	}
	if err := r.send(context.Background(), event, "next reply"); err != nil {
		t.Fatal(err)
	}
	if len(api.callsOf("sendMessage")) != 2 || len(api.callsOf("sendPhoto")) != 1 || len(api.callsOf("sendVideo")) != 1 {
		t.Fatalf("unexpected native delivery: %#v", api.calls)
	}
	if qq.groupListAttempts() != 0 || qq.sendAttempts() != 0 {
		t.Fatal("Telegram result queried or used QQ")
	}
	for _, call := range api.calls {
		if call.Method != "sendMessage" && call.Method != "sendPhoto" && call.Method != "sendVideo" {
			t.Fatalf("unexpected protocol call: %s", call.Method)
		}
	}
}

func TestGroupBackoffUsesTargetAccountStatus(t *testing.T) {
	active := &failingOutboundChannel{}
	scripted := newScriptedBackoffChannel("group")
	target := &statusOverrideChannel{Channel: scripted}
	target.setStatus(ChannelStatus{Connected: false})
	r := NewRuntime(BotConfig{}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "active", Platform: PlatformOneBotV11, Channel: active},
		{ProfileID: "offline", Platform: PlatformOneBotV11, Channel: target},
	}), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "offline", Platform: PlatformOneBotV11, GroupID: "group"}
	if err := r.send(context.Background(), event, "test"); !errors.Is(err, errOutboundChannelOffline) {
		t.Fatalf("offline target did not defer: %v", err)
	}
	if len(scripted.attemptTexts("group")) != 0 {
		t.Fatal("online sibling masked offline target")
	}
	target.setStatus(ChannelStatus{Connected: true})
	if err := r.send(context.Background(), event, "recovered"); err != nil {
		t.Fatal(err)
	}
}

func TestMissingOutboundProfileCannotFallBackToSibling(t *testing.T) {
	target := &recordingChannel{}
	r := NewRuntime(BotConfig{}, NewMultiChannel([]ChannelBinding{{ProfileID: "existing", Platform: PlatformTelegram, Channel: target}}), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "deleted", Platform: PlatformTelegram, GroupID: "group"}
	if err := r.sendOutgoing(context.Background(), event, OutgoingMessage{Text: "test"}); err == nil {
		t.Fatal("missing profile fell back to another bot")
	}
	if len(target.sentSnapshot()) != 0 {
		t.Fatal("wrong bot sent message")
	}
}

func TestMalformedOneBotGroupListCannotProveAbsence(t *testing.T) {
	for _, items := range [][]any{{"bad"}, {map[string]any{"group_name": "missing id"}}, {map[string]any{"group_id": "other"}, nil}} {
		channel := &statusOverrideChannel{Channel: &recordingChannel{apiResponses: map[string]map[string]any{"get_group_list": {"items": items}}}}
		channel.setStatus(ChannelStatus{Connected: true})
		r := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
		if missing, verified := r.groupMissingFromOneBot(MessageEvent{Kind: EventKindGroup, GroupID: "target"}); missing || verified {
			t.Fatalf("malformed list treated as complete: %#v", items)
		}
	}
}

func TestAdminGroupSendUsesOneBotBindingForGuardAndDelivery(t *testing.T) {
	tg := &recordingChannel{}
	qq := &recordingChannel{}
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "tg", Platform: PlatformTelegram, Channel: tg},
		{ProfileID: "qq", Platform: PlatformOneBotV11, Channel: qq},
	}), NewPluginManager(), nil, nil, nil, nil)
	if _, err := r.SendGroupMessage(context.Background(), "123456", "admin test"); err != nil {
		t.Fatal(err)
	}
	if calls := qq.callsSnapshot(); len(calls) != 1 || calls[0].action != "send_group_msg" {
		t.Fatalf("admin send did not select QQ: %#v", calls)
	}
	if len(tg.callsSnapshot()) != 0 || len(tg.sentSnapshot()) != 0 {
		t.Fatal("admin OneBot send reached Telegram")
	}
}
