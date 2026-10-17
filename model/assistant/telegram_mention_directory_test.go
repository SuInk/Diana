// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

type mentionDirectoryStubChannel struct {
	nilChannel
	identities map[string][2]string
	asked      []string
}

func (c *mentionDirectoryStubChannel) MentionIdentity(_ context.Context, groupID, userID string) (string, string, bool) {
	c.asked = append(c.asked, groupID+"/"+userID)
	identity, ok := c.identities[userID]
	return identity[0], identity[1], ok
}

// 提醒投递、很久没说话的群友不在近期消息里，以前只能显示成 @<数字ID>。
func TestTelegramMentionFallsBackToChannelDirectory(t *testing.T) {
	channel := &mentionDirectoryStubChannel{identities: map[string][2]string{
		"10002": {"小汐", "xiaoxi_dev"},
		"10003": {"雨夹雪", ""},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := telegramMentionTestEvent("10001", 20, nil)
	msg := runtime.resolveOutgoingMentionNames(event, OutgoingMessage{Text: "[diana-at:10002] [diana-at:10003] [diana-at:10004]"})
	text, _ := renderTelegramOutgoingText(msg.Text, msg.MentionNames, msg.MentionUsernames)
	if text != "@xiaoxi_dev @雨夹雪 @10004" {
		t.Fatalf("rendered %q (names=%v usernames=%v)", text, msg.MentionNames, msg.MentionUsernames)
	}
	if len(channel.asked) != 3 || channel.asked[0] != "-10001/10002" {
		t.Fatalf("directory lookups = %v", channel.asked)
	}
}

func TestTelegramMentionDirectorySkipsKnownUsernamesAndPrivateChats(t *testing.T) {
	channel := &mentionDirectoryStubChannel{identities: map[string][2]string{"10002": {"别人", "other_name"}}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	current := telegramMentionTestEvent("10002", 20, telegramMentionTestUsername("recent_name"))
	msg := runtime.resolveOutgoingMentionNames(current, OutgoingMessage{Text: "[diana-at:10002]"})
	if msg.MentionUsernames["10002"] != "recent_name" || len(channel.asked) != 0 {
		t.Fatalf("usernames=%v asked=%v", msg.MentionUsernames, channel.asked)
	}
	private := telegramMentionTestEvent("10001", 20, nil)
	private.Kind, private.GroupID = EventKindPrivate, ""
	runtime.resolveOutgoingMentionNames(private, OutgoingMessage{Text: "[diana-at:10002]"})
	if len(channel.asked) != 0 {
		t.Fatalf("private chat asked directory: %v", channel.asked)
	}
}

func TestTelegramChannelMentionIdentityUsesKnownMembers(t *testing.T) {
	channel := &TelegramChannel{}
	channel.rememberMember("-10001", telegramUser{ID: 10002, FirstName: "小汐", Username: "xiaoxi_dev"})
	name, username, ok := channel.MentionIdentity(context.Background(), "-10001", "10002")
	if !ok || name != "小汐" || username != "xiaoxi_dev" {
		t.Fatalf("name=%q username=%q ok=%v", name, username, ok)
	}
	if _, _, ok := channel.MentionIdentity(context.Background(), "", "10002"); ok {
		t.Fatal("private chat lookup should not succeed")
	}
}
