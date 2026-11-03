// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 开了「隐藏成员」的超级群里，非管理员机器人对别人调 getChatMember 回
// CHAT_ADMIN_REQUIRED。这是「没权限查」，不能当成「不在群」。
func TestTelegramGroupMemberReportsUnverifiableMembership(t *testing.T) {
	for _, tc := range []struct {
		description  string
		unverifiable bool
	}{
		{"Bad Request: CHAT_ADMIN_REQUIRED", true},
		{"Bad Request: PARTICIPANT_ID_INVALID", false},
	} {
		channel := NewTelegramChannel(TelegramConfig{BotToken: "test-token"})
		channel.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			body := `{"ok":false,"error_code":400,"description":"` + tc.description + `"}`
			return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		_, err := channel.GroupMember(context.Background(), "-10012345", "12345")
		if err == nil {
			t.Fatalf("%s: membership accepted", tc.description)
		}
		if got := errors.Is(err, ErrMembershipUnverifiable); got != tc.unverifiable {
			t.Fatalf("%s: unverifiable=%v, want %v (err=%v)", tc.description, got, tc.unverifiable, err)
		}
	}
}

// 隐藏成员的群里查不了成员身份，本群对话里出现过的人就是在场的证据：看头像、
// 改头像都要能取到他们；从没出现过的 ID 仍然拒绝，别的群的人也不算。
func TestTelegramHiddenMembersAvatarFromConversation(t *testing.T) {
	channel := &avatarDirectoryChannel{
		recordingChannel: &recordingChannel{},
		members:          map[string][]byte{"20002": []byte("\x89PNG\r\n\x1a\nrain"), "20003": []byte("\x89PNG\r\n\x1a\nquoted"), "20009": []byte("\x89PNG\r\n\x1a\nother")},
		hiddenMembers:    true,
	}
	runtime := NewRuntime(BotConfig{ID: "tg", Platform: PlatformTelegram}, channel, NewPluginManager(), nil, &stubReminderStore{}, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "-10012345", UserID: "10001", MessageID: "m3", Platform: PlatformTelegram}
	runtime.history[sessionKey(event)] = []MessageEvent{
		{Kind: EventKindGroup, GroupID: "-10012345", UserID: "20002", MessageID: "m1", Platform: PlatformTelegram},
		{Kind: EventKindGroup, GroupID: "-10012345", UserID: "10001", MessageID: "m2", Platform: PlatformTelegram, Quoted: &QuotedMessage{UserID: "20003"}},
		{Kind: EventKindGroup, GroupID: "-10099999", UserID: "20009", MessageID: "x1", Platform: PlatformTelegram},
	}

	for _, userID := range []string{"20002", "20003"} {
		urls := runtime.avatarIdentityImageURLs(context.Background(), event, []string{avatarSourceMemberPrefix + userID})
		if len(urls) != 1 || !strings.HasPrefix(urls[0], "data:image/png;base64,") {
			t.Fatalf("member_avatar:%s in this conversation not resolved: %#v", userID, urls)
		}
	}
	for _, userID := range []string{"20009", "77777"} {
		if urls := runtime.avatarIdentityImageURLs(context.Background(), event, []string{avatarSourceMemberPrefix + userID}); len(urls) != 0 {
			t.Fatalf("member_avatar:%s outside this conversation resolved: %#v", userID, urls)
		}
	}
}

// 比对头像时，「没权限查」的候选人不能被当成退群跳过，否则隐藏成员的群里
// 永远比对不出是谁。
func TestAvatarMatchKeepsUnverifiableTelegramMembers(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	var target, other, posted bytes.Buffer
	if err := png.Encode(&target, patternedAvatar(256)); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&other, unrelatedAvatar(256)); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&posted, patternedAvatar(320), &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(posted.Bytes())
	}))
	defer server.Close()

	channel := &avatarDirectoryChannel{
		recordingChannel: &recordingChannel{},
		members:          map[string][]byte{"20002": target.Bytes(), "20003": other.Bytes()},
		hiddenMembers:    true,
	}
	runtime := NewRuntime(BotConfig{ID: "tg", Platform: PlatformTelegram, OwnerID: "10001"}, channel, NewPluginManager(), nil, &stubReminderStore{}, nil, nil)
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "-10012345", UserID: "10001", MessageID: "m1", Platform: PlatformTelegram,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": server.URL + "/posted.jpg"}}},
	}
	match, err := runtime.matchCurrentGroupMemberAvatar(t.Context(), event)
	if err != nil || !match.Matched {
		t.Fatalf("hidden-member group never matches: %+v err=%v", match, err)
	}
}
