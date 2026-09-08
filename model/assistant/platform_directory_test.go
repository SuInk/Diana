package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func directoryTestClient(fn func(*http.Request) any) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := json.Marshal(fn(r))
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})}
}
func directoryTestToken() *platformTokenCache {
	return &platformTokenCache{token: "test-secret-token", expiresAt: time.Now().Add(time.Hour)}
}

func TestDirectoryGETParametersAcrossPlatforms(t *testing.T) {
	client := directoryTestClient(func(r *http.Request) any {
		q := r.URL.Query()
		if q.Get("id") != "用户&a" || q.Get("keep") != "1" || len(q["ids"]) != 2 || q.Get("active") != "false" {
			t.Fatalf("query lost: %s", r.URL)
		}
		return map[string]any{"ok": true}
	})
	f := NewFeishuChannel(FeishuConfig{})
	f.client = client
	f.tokens = directoryTestToken()
	d := NewDingTalkChannel(DingTalkConfig{})
	d.client = client
	d.tokens = directoryTestToken()
	w := NewWeComChannel(WeComConfig{})
	w.client = client
	w.tokens = directoryTestToken()
	q := NewQQOfficialChannel(QQOfficialConfig{})
	q.client = client
	q.tokens = directoryTestToken()
	for _, c := range []Channel{f, d, w, q} {
		if _, err := c.CallAPI(context.Background(), "GET /test?keep=1", map[string]any{"id": "用户&a", "ids": []string{"a", "b"}, "active": false}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFeishuDirectoryPaginatesAndDoesNotInventBots(t *testing.T) {
	c := NewFeishuChannel(FeishuConfig{})
	c.tokens = directoryTestToken()
	pages := 0
	c.client = directoryTestClient(func(r *http.Request) any {
		if strings.HasSuffix(r.URL.Path, "/members") {
			pages++
			if r.URL.Query().Get("member_id_type") != "open_id" {
				t.Fatal("wrong ID type")
			}
			if r.URL.Query().Get("page_token") == "" {
				return map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"member_id": "ou_1", "name": "one"}}, "has_more": true, "page_token": "next"}}
			}
			return map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"member_id": "ou_2", "name": "two"}}, "has_more": false}}
		}
		return map[string]any{"code": 0, "data": map[string]any{"name": "chat", "owner_id": "ou_1", "user_manager_id_list": []string{"ou_2"}, "user_count": "2", "bot_count": "1"}}
	})
	list, err := c.GroupMembers(context.Background(), "oc_chat")
	if err != nil || len(list.Members) != 2 || list.Complete || list.Total != 3 || pages != 2 {
		t.Fatalf("bad directory: %+v %v", list, err)
	}
	if list.Members[0].Role != "owner" || list.Members[1].Role != "admin" {
		t.Fatal("roles not tied to IDs")
	}
	ctx := withDirectoryEvent(context.Background(), MessageEvent{UserID: "not-open-id", UserIDType: "user_id"})
	if _, err := c.GroupMember(ctx, "oc_chat", "not-open-id"); err == nil {
		t.Fatal("mixed ID types")
	}
}

func TestDingTalkDirectoryRequiresStaffIdentityAndPaginates(t *testing.T) {
	c := NewDingTalkChannel(DingTalkConfig{})
	c.tokens = directoryTestToken()
	c.client = directoryTestClient(func(r *http.Request) any {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/v1.0/im/groups/baseInfos/query":
			return map[string]any{"success": true, "result": map[string]any{"title": "group", "memberCount": 2}}
		case "/v1.0/im/innerGroups/memberLists/query":
			if body["userId"] != "staff" {
				t.Fatal("operator lost")
			}
			if directoryInt(body["nextToken"]) == 0 {
				return map[string]any{"list": []any{map[string]any{"userId": "a", "name": "A"}}, "hasMore": true, "nextToken": 1}
			}
			return map[string]any{"list": []any{map[string]any{"userId": "b", "name": "B"}}, "hasMore": false}
		case "/v1.0/im/innerGroups/members/check":
			return map[string]any{"result": true}
		case "/topapi/v2/user/get":
			if r.URL.Host != "oapi.dingtalk.com" || r.URL.Query().Get("access_token") != "test-secret-token" {
				t.Fatal("legacy authentication wrong")
			}
			return map[string]any{"errcode": 0, "result": map[string]any{"name": "Name"}}
		default:
			return map[string]any{"result": map[string]any{"groupRoles": []any{map[string]any{"roleName": "管理员", "openRoleId": "custom"}}}}
		}
	})
	ctx := withDirectoryEvent(context.Background(), MessageEvent{UserID: "staff", UserIDType: "dingtalk_userid"})
	list, err := c.GroupMembers(ctx, "cid-group")
	if err != nil || len(list.Members) != 2 || !list.Complete {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	member, err := c.GroupMember(ctx, "cid-group", "a")
	if err != nil || !member.MembershipVerified || member.Role != "" || member.Title != "管理员" {
		t.Fatalf("custom role became authority: %+v %v", member, err)
	}
	if _, err := c.GroupMembers(withDirectoryEvent(ctx, MessageEvent{UserID: "external", UserIDType: "dingtalk_sender_id"}), "cid-group"); err == nil {
		t.Fatal("senderId treated as staff userId")
	}
}

func TestWeComDirectoryIsApplicationScoped(t *testing.T) {
	c := NewWeComChannel(WeComConfig{})
	c.tokens = directoryTestToken()
	c.client = directoryTestClient(func(r *http.Request) any {
		if r.URL.Path == "/cgi-bin/appchat/get" {
			if r.URL.Query().Get("chatid") != "chat" {
				t.Fatal("chatid missing")
			}
			return map[string]any{"errcode": 0, "chat_info": map[string]any{"name": "Chat", "owner": "u&a", "userlist": []string{"u&a", "other"}}}
		}
		if r.URL.Query().Get("userid") != "u&a" {
			t.Fatal("userid encoding lost")
		}
		return map[string]any{"errcode": 0, "name": "Owner"}
	})
	member, err := c.GroupMember(context.Background(), "chat", "u&a")
	if err != nil || member.Role != "owner" || member.Nickname != "Owner" {
		t.Fatalf("member=%+v %v", member, err)
	}
	if _, err := c.GroupMember(context.Background(), "chat", "outsider"); err == nil {
		t.Fatal("nonmember accepted")
	}
	c.client = directoryTestClient(func(*http.Request) any { return map[string]any{"errcode": 60011, "errmsg": "not authorized"} })
	if _, err := c.GroupMembers(context.Background(), "chat"); err == nil {
		t.Fatal("authorization failure became empty list")
	}
}

func TestQQOfficialDirectorySeparatesGroupsAndChannels(t *testing.T) {
	c := NewQQOfficialChannel(QQOfficialConfig{})
	c.tokens = directoryTestToken()
	calls := 0
	c.client = directoryTestClient(func(r *http.Request) any {
		calls++
		switch r.URL.Path {
		case "/channels/channel":
			return map[string]any{"guild_id": "guild", "private_type": 0}
		case "/guilds/guild":
			return map[string]any{"name": "Guild", "member_count": 1}
		case "/guilds/guild/members":
			return []any{map[string]any{"user": map[string]any{"id": "user", "username": "User"}, "roles": []string{"2"}}}
		default:
			return map[string]any{"id": "message"}
		}
	})
	ctx := withDirectoryEvent(context.Background(), MessageEvent{PlatformScope: "qq_group", GroupID: "channel"})
	if _, err := c.GroupMembers(ctx, "channel"); err == nil || calls != 0 {
		t.Fatal("ordinary group used guild API")
	}
	ctx = withDirectoryEvent(context.Background(), MessageEvent{PlatformScope: "qq_guild", GroupID: "channel", GuildID: "guild"})
	list, err := c.GroupMembers(ctx, "channel")
	if err != nil || len(list.Members) != 1 || !list.Complete || list.Members[0].Role != "admin" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	event, ok := qqOfficialEventFromDispatch("AT_MESSAGE_CREATE", json.RawMessage(`{"id":"m","channel_id":"channel","guild_id":"guild","author":{"id":"user"}}`), "bot")
	if !ok || event.PlatformScope != "qq_guild" || event.GuildID != "guild" {
		t.Fatal("native scope lost")
	}
	c.client = directoryTestClient(func(r *http.Request) any {
		if r.URL.Path != "/channels/channel/messages" {
			t.Fatalf("guild reply misrouted: %s", r.URL.Path)
		}
		return map[string]any{"id": "m"}
	})
	if err := c.Send(context.Background(), OutgoingMessage{GroupID: "channel", PlatformScope: "qq_guild", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformTransportErrorsHideCredentials(t *testing.T) {
	c := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "GET", URL: r.URL.String(), Err: errors.New("network error")}
	})}
	_, err := platformJSONRequest(context.Background(), c, "GET", "https://example.com/api?access_token=secret-token", nil, nil)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("credential leak: %v", err)
	}
	if _, err := platformAvatar(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("local file used as avatar")
	}
}

func TestPlatformAvatarRedirectDoesNotTransmitCredentialsOrReferer(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "1")
	requests := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Referer") != "" {
			t.Error("avatar redirect leaked source URL")
		}
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	if _, err := platformAvatar(context.Background(), source.URL+"?access_token=secret"); err == nil || requests != 0 {
		t.Fatal("authenticated avatar followed redirect")
	}
	if avatar, err := platformAvatar(context.Background(), source.URL+"?image=public"); err != nil || avatar.ContentType != "image/png" || requests != 1 {
		t.Fatalf("public avatar redirect failed: %v", err)
	}
}
