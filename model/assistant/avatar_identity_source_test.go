// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

// 头像来源由模型点名，运行时只负责换成地址。以前是拿「群头像 / 我的头像 / 你的头像」
// 三张词表去正文里猜，这里逐个确认换成 id 之后每一种来源都还能选到。
func TestAvatarIdentityImageURLsResolvesEachSource(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "123456",
		UserID:    "10001",
		MessageID: "avatar-sources",
	}
	tests := []struct {
		name     string
		selected []string
		want     string
	}{
		{name: "group", selected: []string{avatarSourceGroup}, want: OneBotGroupAvatarURL("123456")},
		{name: "bot", selected: []string{avatarSourceBot}, want: OneBotMemberAvatarURL("42")},
		{name: "sender", selected: []string{avatarSourceSender}, want: OneBotMemberAvatarURL("10001")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := runtime.avatarIdentityImageURLs(context.Background(), event, test.selected)
			if len(got) != 1 || got[0] != test.want {
				t.Fatalf("urls = %#v, want %q", got, test.want)
			}
		})
	}
}

// 成员头像按 id 直接取，不核对在不在当前会话：隐藏成员的 Telegram 群里核对不了，
// 硬核对只会把活跃群友挡在外面。
func TestAvatarIdentityImageURLsResolvesMemberWithoutMembershipCheck(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "10001",
		MessageID: "avatar-stranger",
	}
	if got := runtime.avatarIdentityImageURLs(context.Background(), event, []string{avatarSourceMemberPrefix + "99999"}); len(got) != 1 || got[0] != OneBotMemberAvatarURL("99999") {
		t.Fatalf("member outside conversation not resolved: %#v", got)
	}
	if got := runtime.avatarIdentityImageURLs(context.Background(), event, []string{avatarSourceMemberPrefix}); len(got) != 0 {
		t.Fatalf("empty member id resolved: %#v", got)
	}
	if got := runtime.avatarIdentityImageURLs(context.Background(), event, []string{avatarSourceMemberPrefix + "10001"}); len(got) != 1 {
		t.Fatalf("sender avatar not resolved: %#v", got)
	}
}

func TestAvatarIdentityImageURLsPrivateGroupRequiresExplicitID(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind: EventKindPrivate,
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "把 123456789 的群头像改一下"}},
		},
	}
	got := runtime.avatarIdentityImageURLs(context.Background(), event, []string{avatarSourceGroupPrefix + "123456789"})
	if len(got) != 1 || got[0] != OneBotGroupAvatarURL("123456789") {
		t.Fatalf("explicit group avatar = %#v", got)
	}
	if got := runtime.avatarIdentityImageURLs(context.Background(), event, []string{avatarSourceGroupPrefix + "987654321"}); len(got) != 0 {
		t.Fatalf("unmentioned group avatar resolved: %#v", got)
	}
}

// 没点名时只按 @ 兜底，机器人自己不算。
func TestDefaultAvatarIdentitySourcesUsesMentionsOnly(t *testing.T) {
	event := MessageEvent{
		Kind:    EventKindGroup,
		GroupID: "123456",
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "42"}},
			{Type: "at", Data: map[string]string{"qq": "20002"}},
			{Type: "text", Data: map[string]string{"text": " 把头像改成赛博风"}},
		},
	}
	got := defaultAvatarIdentitySources(event, "42")
	if len(got) != 1 || got[0] != avatarSourceMemberPrefix+"20002" {
		t.Fatalf("default sources = %#v", got)
	}
	if got := defaultAvatarIdentitySources(MessageEvent{Kind: EventKindPrivate, UserID: "10001"}, "42"); len(got) != 0 {
		t.Fatalf("private message produced default sources: %#v", got)
	}
}
