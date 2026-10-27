package assistant

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
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

// replyCode 从机器人最后一条回复里取出 6 位验证码。
func replyCode(t *testing.T, channel *recordingChannel) string {
	t.Helper()
	sent := channel.sentSnapshot()
	if len(sent) == 0 {
		t.Fatal("bot sent nothing")
	}
	code := regexp.MustCompile(`\d{6}`).FindString(sent[len(sent)-1].Text)
	if code == "" {
		t.Fatalf("no code in reply %q", sent[len(sent)-1].Text)
	}
	return code
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
		if !strings.Contains(msg.Text, "绑定主人") || regexp.MustCompile(`\d{6}`).MatchString(msg.Text) {
			t.Fatalf("reminder text = %q", msg.Text)
		}
	}
	if sent[0].PassiveReplyMessageID == "" {
		t.Fatal("reminder must be a passive reply, QQ official rejects active group messages")
	}

	if !r.handleOwnerBind(ctx, ownerBindEvent(EventKindGroup, testOwnerOpenID, "绑定主人")) {
		t.Fatal("bind command was not consumed")
	}
	ownerCode := replyCode(t, channel)
	ownerID, err := r.BindOwnerByCode("qq", ownerCode)
	if err != nil {
		t.Fatal(err)
	}
	if ownerID != testOwnerOpenID || saver.owners["qq"] != testOwnerOpenID {
		t.Fatalf("bound owner = %q, persisted = %q", ownerID, saver.owners["qq"])
	}
	if !r.ProfileConfig("qq").IsOwnerEvent(ownerBindEvent(EventKindPrivate, testOwnerOpenID, "")) {
		t.Fatal("bound openid is not recognised as owner")
	}

	before := len(channel.sentSnapshot())
	for _, text := range []string{"你好", "绑定主人"} {
		other := ownerBindEvent(EventKindPrivate, testStrangerOpenID, text)
		if r.handleOwnerBind(ctx, other) || len(channel.sentSnapshot()) != before {
			t.Fatalf("bot still handled %q after owner was bound", text)
		}
	}
	if _, err := r.BindOwnerByCode("qq", ownerCode); err == nil {
		t.Fatal("owner was rebound")
	}
}

func TestOwnerBindRejectsWrongExpiredAndReusedCodes(t *testing.T) {
	ctx := context.Background()
	saver := &testOwnerIDSaver{}
	r, channel := newOwnerBindTestRuntime(saver)

	if !r.handleOwnerBind(ctx, ownerBindEvent(EventKindPrivate, testOwnerOpenID, "绑定主人")) {
		t.Fatal("bind command was not consumed")
	}
	oldCode := replyCode(t, channel)
	if _, err := r.BindOwnerByCode("qq", "绑定主人"); err == nil {
		t.Fatal("garbage code accepted")
	}

	// 同一个人重新取码，旧码作废。
	r.handleOwnerBind(ctx, ownerBindEvent(EventKindPrivate, testOwnerOpenID, "/绑定主人"))
	newCode := replyCode(t, channel)
	if newCode != oldCode {
		if _, err := r.BindOwnerByCode("qq", oldCode); err == nil {
			t.Fatal("superseded code still works")
		}
	}

	saver.fail = true
	if _, err := r.BindOwnerByCode("qq", newCode); err == nil {
		t.Fatal("persistence failure reported success")
	}
	if owner := r.ProfileConfig("qq").OwnerID; owner != "" {
		t.Fatalf("owner changed in memory despite persistence failure: %q", owner)
	}
	saver.fail = false
	if _, err := r.BindOwnerByCode("qq", newCode); err == nil {
		t.Fatal("a code is single-use even when saving failed")
	}

	r.handleOwnerBind(ctx, ownerBindEvent(EventKindPrivate, testOwnerOpenID, "绑定主人"))
	expiring := replyCode(t, channel)
	now := time.Now().Add(ownerBindCodeTTL + time.Second)
	r.now = func() time.Time { return now }
	if _, err := r.BindOwnerByCode("qq", expiring); err == nil {
		t.Fatal("expired code accepted")
	}
	if len(saver.owners) != 0 {
		t.Fatalf("owner bound unexpectedly: %+v", saver.owners)
	}
}

func TestOwnerBindOnlyForQQOfficial(t *testing.T) {
	r := NewRuntime(BotConfig{ID: "ob", Platform: PlatformOneBotV11}, &recordingChannel{}, NewPluginManager(), nil, nil, &testOwnerIDSaver{}, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "ob", Platform: PlatformOneBotV11}}})
	if _, err := r.BindOwnerByCode("ob", "123456"); err == nil {
		t.Fatal("OneBot profiles should fill the QQ number directly")
	}
	event := ownerBindEvent(EventKindPrivate, "10001", "绑定主人")
	event.Platform, event.ProfileID = PlatformOneBotV11, "ob"
	if r.handleOwnerBind(context.Background(), event) {
		t.Fatal("OneBot message consumed")
	}
}
