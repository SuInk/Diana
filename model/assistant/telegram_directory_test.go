package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTelegramMemberStatesAndNativeRoles(t *testing.T) {
	for _, tc := range []struct {
		status  string
		present bool
		want    string
	}{
		{"creator", true, "owner"}, {"administrator", true, "admin"}, {"member", true, "member"}, {"restricted", true, "member"}, {"restricted", false, ""}, {"left", false, ""}, {"kicked", false, ""},
	} {
		t.Run(tc.status+tc.want, func(t *testing.T) {
			api := newFakeTelegramAPI(t, map[string]any{"getChatMember": map[string]any{"status": tc.status, "is_member": tc.present, "custom_title": "title", "user": map[string]any{"id": 12345, "first_name": "雨夹雪", "username": "rain", "is_bot": false}}})
			member, err := api.channel().GroupMember(context.Background(), "-1001", "12345")
			if tc.want == "" {
				if err == nil {
					t.Fatal("absent member accepted")
				}
				return
			}
			if err != nil || member.Role != tc.want || !member.MembershipVerified || member.Username != "rain" || member.AvatarURL != "" {
				t.Fatalf("member=%+v err=%v", member, err)
			}
			if len(api.callsOf("getChatMember")) != 1 || len(api.callsOf("get_group_member_info")) != 0 {
				t.Fatal("wrong protocol")
			}
		})
	}
}

func TestTelegramDirectoryIsExplicitlyPartialAndTracksUpdates(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{"getChatAdministrators": []any{map[string]any{"status": "creator", "user": map[string]any{"id": 11111, "first_name": "Owner"}}}, "getChatMemberCount": 42})
	c := api.channel()
	c.dispatch(context.Background(), telegramUpdate{Message: &telegramMessage{Chat: &telegramChat{ID: -1001, Type: "supergroup"}, From: &telegramUser{ID: 22222, FirstName: "Rain", Username: "rain"}}})
	directory, err := c.GroupMembers(context.Background(), "-1001")
	if err != nil || directory.Complete || !directory.TotalKnown || directory.Total != 42 || len(directory.Members) != 2 {
		t.Fatalf("directory=%+v err=%v", directory, err)
	}
	if directory.Members[1].MembershipVerified || directory.Members[1].Username != "rain" {
		t.Fatal("observed user treated as verified or lost username")
	}
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, c, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaGroupTool(r, MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-1001"})
	if tool.Name() != "diana.group" {
		t.Fatal("TG still uses OneBot tool name")
	}
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "members", "query": "@rain"})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaGroupResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.MemberListComplete == nil || *result.MemberListComplete || !result.Limited || result.GroupTotal != 42 || len(result.Members) != 1 || result.Members[0].AvatarSource != "member_avatar:22222" {
		t.Fatalf("misleading result: %s", raw)
	}
	c.dispatch(context.Background(), telegramUpdate{ChatMember: &telegramMemberUpdate{Chat: telegramChat{ID: -1001}, New: telegramChatMember{User: telegramUser{ID: 22222}, Status: "left"}}})
	c.mu.RLock()
	_, stillCached := c.knownMembers["-1001"][22222]
	c.mu.RUnlock()
	if stillCached {
		t.Fatal("left member stayed in candidate cache")
	}
	c.dispatch(context.Background(), telegramUpdate{MyChatMember: &telegramMemberUpdate{Chat: telegramChat{ID: -1001}, New: telegramChatMember{User: telegramUser{ID: 33333}, Status: "kicked"}}})
	c.mu.RLock()
	remaining := len(c.knownMembers["-1001"])
	c.mu.RUnlock()
	if remaining != 0 {
		t.Fatal("bot removal did not clear chat cache")
	}
}

func TestTelegramAuthorityAndCrossGroupMembershipIgnoreStaleCache(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{"getChatMember": map[string]any{"status": "left", "user": map[string]any{"id": 12345, "first_name": "Ex-admin"}}})
	r := NewRuntime(BotConfig{Platform: PlatformTelegram, OwnerID: "99999"}, api.channel(), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-1001", UserID: "12345", SenderRole: "admin"}
	r.members.store(event.GroupID, event.UserID, memberInfo{At: time.Now(), Role: "admin"})
	if _, err := r.canConfigureGroup(context.Background(), event); err == nil {
		t.Fatal("stale administrator role granted permission")
	}
	allowed := r.crossGroupCurrentMembers(event, map[string][]MessageEvent{"12345": nil})
	if allowed["12345"] {
		t.Fatal("stale cache allowed cross-group memory disclosure")
	}
	if len(api.callsOf("getChatMember")) != 2 {
		t.Fatal("membership was not checked natively")
	}
}

func TestTelegramAvatarUsesBytesAndLargestPhotoWithoutTokenURLs(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	api := newFakeTelegramAPI(t, map[string]any{"getUserProfilePhotos": map[string]any{"photos": [][]any{{map[string]any{"file_id": "big", "width": 640, "height": 640}, map[string]any{"file_id": "small", "width": 160, "height": 160}}}}, "getFile": map[string]any{"file_path": "photos/avatar.png"}, "download:photos/avatar.png": png})
	c := api.channel()
	r := NewRuntime(BotConfig{Platform: PlatformTelegram, BotAccount: "12345"}, c, NewPluginManager(), nil, nil, nil, nil)
	urls := r.avatarIdentityImageURLs(context.Background(), MessageEvent{Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "22222"}, []string{avatarSourceSender})
	if len(urls) != 1 || urls[0] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(png) || strings.Contains(urls[0], "test-token") || strings.Contains(urls[0], "qlogo") {
		t.Fatalf("unsafe avatar source: %v", urls)
	}
	if got := api.callsOf("getFile"); len(got) != 1 || got[0].Params["file_id"] != "big" {
		t.Fatalf("wrong size: %+v", got)
	}
	photos := api.callsOf("getUserProfilePhotos")
	if len(photos) != 1 || stringFromAny(photos[0].Params["user_id"]) != "22222" {
		t.Fatal("wrong profile photo target")
	}
}

func TestTelegramAvatarMissingDoesNotFallBackToQQAndDownloadErrorsHideToken(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{"getUserProfilePhotos": map[string]any{"photos": []any{}}, "getFile": map[string]any{"file_path": "photo.png"}})
	c := api.channel()
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, c, NewPluginManager(), nil, nil, nil, nil)
	if urls := r.avatarIdentityImageURLs(context.Background(), MessageEvent{Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "22222"}, []string{avatarSourceSender}); len(urls) != 0 {
		t.Fatalf("invented avatar: %v", urls)
	}
	transport := c.client.Transport
	c.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/file/bot") {
			return nil, errors.New("offline")
		}
		return transport.RoundTrip(req)
	})
	_, _, err := c.downloadFileByID(context.Background(), "photo", 1024)
	if err == nil || strings.Contains(err.Error(), "test-token") || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("unsafe download error: %v", err)
	}
}

func TestTelegramCredentialChangeClearsDirectoryAndUpdateOffset(t *testing.T) {
	c := NewTelegramChannel(TelegramConfig{BotToken: "old"})
	c.rememberMember("-1001", telegramUser{ID: 12345, FirstName: "old"})
	c.offset = 100
	c.botUsername = "old"
	c.setStatus(true, "12345", "")
	c.SetConfig(TelegramConfig{BotToken: "new"})
	if len(c.knownMembers) != 0 || c.offset != 0 || c.botUsername != "" || c.Status().SelfID != "" {
		t.Fatal("old identity state survived credential change")
	}
}

func TestTelegramGroupInfoIncludesNativeCount(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{"getChat": map[string]any{"id": -1001, "title": "Group"}, "getChatMemberCount": 42})
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, api.channel(), NewPluginManager(), nil, nil, nil, nil)
	info, err := r.GetGroupInfo(context.Background(), "-1001")
	if err != nil || info.GroupName != "Group" || info.MemberCount != 42 || info.MemberCountKnown == nil || !*info.MemberCountKnown || info.AvatarURL != "" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
}

func TestTelegramDirectoryRoutesExactProfileAndRejectsMismatch(t *testing.T) {
	first := newFakeTelegramAPI(t, map[string]any{"getChatMember": map[string]any{"status": "member", "user": map[string]any{"id": 12345, "first_name": "first"}}})
	second := newFakeTelegramAPI(t, map[string]any{"getChatMember": map[string]any{"status": "member", "user": map[string]any{"id": 12345, "first_name": "second"}}})
	multi := NewMultiChannel([]ChannelBinding{{ProfileID: "first", Platform: PlatformTelegram, Channel: first.channel()}, {ProfileID: "second", Platform: PlatformTelegram, Channel: second.channel()}})
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, multi, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{ProfileID: "second", Platform: PlatformTelegram}
	member, err := r.getGroupMemberInfoForEvent(context.Background(), event, "-1001", "12345")
	if err != nil || member.Nickname != "second" || len(first.callsOf("getChatMember")) != 0 {
		t.Fatalf("wrong profile: %+v %v", member, err)
	}
	event.ProfileID = "missing"
	if _, err := r.getGroupMemberInfoForEvent(context.Background(), event, "-1001", "12345"); err == nil {
		t.Fatal("unknown profile fell back to another bot")
	}
	if len(second.callsOf("getChatMember")) != 1 {
		t.Fatal("unknown profile called Telegram")
	}
}

func TestTelegramDoesNotTreatAnonymousSenderAsUserAndSubscribesMembership(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{"getUpdates": []any{}})
	c := api.channel()
	c.dispatch(context.Background(), telegramUpdate{Message: &telegramMessage{Chat: &telegramChat{ID: -1001, Type: "supergroup"}, From: &telegramUser{ID: 12345}, SenderChat: &telegramChat{ID: -1001}}})
	if len(c.knownMembers) != 0 {
		t.Fatal("anonymous channel sender cached as user")
	}
	if _, err := c.fetchUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	updates := api.callsOf("getUpdates")
	body, _ := json.Marshal(updates[0].Params["allowed_updates"])
	if !strings.Contains(string(body), "chat_member") || !strings.Contains(string(body), "my_chat_member") {
		t.Fatal("membership updates not subscribed")
	}
}

func TestTelegramGroupToolPromptAndPermissionsAreRegistered(t *testing.T) {
	r := NewRuntime(BotConfig{Platform: PlatformTelegram, AgentEnabled: true}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "-1001"}
	prompt := r.systemPrompt(event, nil)
	if !strings.Contains(prompt, "调用 diana.group") || !strings.Contains(prompt, "绝不是完整名单") {
		t.Fatal("Telegram group capability missing from prompt")
	}
	if !strings.Contains(newDianaImageTool(r, event, RelationshipPolicy{AllowImageEditing: true}).(*dianaImageTool).InputSchema()["properties"].(map[string]any)["identity_sources"].(map[string]any)["description"].(string), "当前平台") {
		t.Fatal("image tool still requires QQ identifiers")
	}
}

func TestTelegramPrivateGroupAvatarRequiresRequesterMembership(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{"getChatMember": map[string]any{"status": "left", "user": map[string]any{"id": 12345}}})
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, api.channel(), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, Platform: PlatformTelegram, UserID: "12345", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "群 -10012345 的头像"}}}}
	if r.privateGroupAvatarAllowed(context.Background(), event, "-10012345") {
		t.Fatal("outsider gained access to private group avatar")
	}
	if r.privateGroupAvatarAllowed(context.Background(), event, "-100123") {
		t.Fatal("partial group ID matched")
	}
	if len(api.callsOf("getChatMember")) != 1 {
		t.Fatal("explicit negative group ID was not checked natively")
	}
}
