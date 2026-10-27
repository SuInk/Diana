package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type testOwnerIDSaver struct {
	owners map[string]string
	fail   bool
}

func (s *testOwnerIDSaver) SaveBotConfig(BotConfig) {}
func (s *testOwnerIDSaver) SaveOwnerID(profileID, ownerID string) error {
	if s.fail {
		return fmt.Errorf("disk failure")
	}
	if s.owners == nil {
		s.owners = map[string]string{}
	}
	s.owners[profileID] = ownerID
	return nil
}

const (
	testOwnerOpenID    = "0123456789ABCDEF0123456789ABCDEF"
	testStrangerOpenID = "FEDCBA9876543210FEDCBA9876543210"
)

func newOwnerBindTestRuntime(saver ConfigSaver) (*Runtime, *recordingChannel) {
	channel := &recordingChannel{}
	cfg := BotConfig{ID: "qq", Platform: PlatformQQOfficial, Enabled: true}
	r := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, saver, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	return r, channel
}

func ownerBindEvent(kind EventKind, userID, text string) MessageEvent {
	event := MessageEvent{Kind: kind, Platform: PlatformQQOfficial, ProfileID: "qq", UserID: userID, MessageID: "m-" + userID, RawMessage: text, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}}
	if kind == EventKindGroup {
		event.GroupID = "G1"
		event.ToMe = true
	}
	return event
}

func TestOwnerBindRemindsOncePerConversationUntilBound(t *testing.T) {
	ctx := context.Background()
	saver := &testOwnerIDSaver{}
	r, channel := newOwnerBindTestRuntime(saver)

	if r.handleOwnerBind(ctx, ownerBindEvent(EventKindPrivate, testOwnerOpenID, "你好")) {
		t.Fatal("ordinary message was consumed")
	}
	r.handleOwnerBind(ctx, ownerBindEvent(EventKindPrivate, testOwnerOpenID, "在吗"))
	r.handleOwnerBind(ctx, ownerBindEvent(EventKindGroup, testStrangerOpenID, "大家好"))
	notAddressed := ownerBindEvent(EventKindGroup, testStrangerOpenID, "闲聊")
	notAddressed.GroupID, notAddressed.ToMe = "G2", false
	r.handleOwnerBind(ctx, notAddressed)
	sent := channel.sentSnapshot()
	if len(sent) != 2 {
		t.Fatalf("want one reminder per conversation (private + group G1), got %d: %+v", len(sent), sent)
	}
	for _, msg := range sent {
		if !strings.Contains(msg.Text, "绑定主人") {
			t.Fatalf("reminder text = %q", msg.Text)
		}
	}
	if sent[0].PassiveReplyMessageID == "" {
		t.Fatal("reminder must be a passive reply, QQ official rejects active group messages")
	}

	code, _, err := r.CreateOwnerBindCode("qq")
	if err != nil {
		t.Fatal(err)
	}
	if !r.OwnerBindStatus("qq").Pending {
		t.Fatal("fresh code should be pending")
	}
	if !r.handleOwnerBind(ctx, ownerBindEvent(EventKindGroup, testOwnerOpenID, "绑定主人 "+code)) {
		t.Fatal("bind command was not consumed")
	}
	if saver.owners["qq"] != testOwnerOpenID {
		t.Fatalf("persisted owner = %q", saver.owners["qq"])
	}
	status := r.OwnerBindStatus("qq")
	if status.OwnerID != testOwnerOpenID || status.Pending {
		t.Fatalf("status after bind = %+v", status)
	}
	if !r.ProfileConfig("qq").IsOwnerEvent(ownerBindEvent(EventKindPrivate, testOwnerOpenID, "")) {
		t.Fatal("bound openid is not recognised as owner")
	}

	before := len(channel.sentSnapshot())
	other := ownerBindEvent(EventKindPrivate, testStrangerOpenID, "你好")
	if r.handleOwnerBind(ctx, other) || len(channel.sentSnapshot()) != before {
		t.Fatal("bot kept reminding after owner was bound")
	}
}

func TestOwnerBindRejectsWrongExpiredAndReusedCodes(t *testing.T) {
	ctx := context.Background()
	saver := &testOwnerIDSaver{}
	r, channel := newOwnerBindTestRuntime(saver)

	code, _, err := r.CreateOwnerBindCode("qq")
	if err != nil {
		t.Fatal(err)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	if !r.handleOwnerBind(ctx, ownerBindEvent(EventKindPrivate, testStrangerOpenID, "绑定主人"+wrong)) {
		t.Fatal("bind command with wrong code was not consumed")
	}
	if len(saver.owners) != 0 {
		t.Fatal("wrong code bound an owner")
	}
	last := channel.sentSnapshot()
	if got := last[len(last)-1].Text; !strings.Contains(got, "验证码不对") {
		t.Fatalf("wrong-code reply = %q", got)
	}

	// 生成新码会作废旧码。
	newCode, _, _ := r.CreateOwnerBindCode("qq")
	if newCode != code {
		r.handleOwnerBind(ctx, ownerBindEvent(EventKindPrivate, testStrangerOpenID, "绑定主人 "+code))
		if len(saver.owners) != 0 {
			t.Fatal("superseded code still works")
		}
	}

	saver.fail = true
	r.handleOwnerBind(ctx, ownerBindEvent(EventKindPrivate, testOwnerOpenID, "绑定主人 "+newCode))
	if owner := r.ProfileConfig("qq").OwnerID; owner != "" {
		t.Fatalf("owner changed in memory despite persistence failure: %q", owner)
	}
	saver.fail = false
	r.handleOwnerBind(ctx, ownerBindEvent(EventKindPrivate, testOwnerOpenID, "绑定主人 "+newCode))
	if len(saver.owners) != 0 {
		t.Fatal("a code is single-use even when saving failed")
	}
}

func TestOwnerBindOnlyForQQOfficial(t *testing.T) {
	r := NewRuntime(BotConfig{ID: "ob", Platform: PlatformOneBotV11}, &recordingChannel{}, NewPluginManager(), nil, nil, &testOwnerIDSaver{}, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "ob", Platform: PlatformOneBotV11}}})
	if _, _, err := r.CreateOwnerBindCode("ob"); err == nil {
		t.Fatal("OneBot profiles should fill the QQ number directly")
	}
	event := ownerBindEvent(EventKindPrivate, "10001", "你好")
	event.Platform, event.ProfileID = PlatformOneBotV11, "ob"
	if r.handleOwnerBind(context.Background(), event) {
		t.Fatal("OneBot message consumed")
	}
}
