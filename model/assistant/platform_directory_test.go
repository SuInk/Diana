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

	"github.com/SuInk/diana/model/llm"
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
	q := NewQQOfficialChannel(QQOfficialConfig{})
	q.client = client
	q.tokens = directoryTestToken()
	for _, c := range []Channel{q} {
		if _, err := c.CallAPI(context.Background(), "GET /test?keep=1", map[string]any{"id": "用户&a", "ids": []string{"a", "b"}, "active": false}); err != nil {
			t.Fatal(err)
		}
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

func TestOneBotMemberLookupRequiresFreshRoster(t *testing.T) {
	for _, tc := range []struct {
		name       string
		data       map[string]any
		wantMember bool
		wantAbsent bool
	}{
		{"absent_despite_profile", map[string]any{"items": []any{}}, false, true},
		{"different_member", map[string]any{"items": []any{map[string]any{"user_id": "999", "group_id": "51001"}}}, false, true},
		{"current_member", map[string]any{"items": []any{map[string]any{"user_id": "41001", "nickname": "T", "role": "member"}}}, true, false},
		{"wrong_group", map[string]any{"items": []any{map[string]any{"user_id": "41001", "group_id": "999"}}}, false, false},
		{"missing_list", map[string]any{}, false, false},
		{"invalid_list", map[string]any{"items": "broken"}, false, false},
		{"invalid_row", map[string]any{"items": []any{"broken"}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &recordingChannel{apiResponses: map[string]map[string]any{
				"get_group_member_info": {"user_id": "41001", "nickname": "T", "role": "member"},
				"get_group_member_list": tc.data,
			}}
			event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "51001", UserID: "owner"}
			r := NewRuntime(BotConfig{Platform: PlatformOneBotV11, OwnerID: "owner"}, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
			member, err := r.getCurrentGroupMemberForTool(context.Background(), event, event.GroupID, "41001")
			if tc.wantMember {
				if err != nil || !member.MembershipVerified || groupMemberToolItem(member).Mention == "" {
					t.Fatalf("member=%+v err=%v", member, err)
				}
			} else if err == nil || member.MembershipVerified || groupMemberAbsent(err) != tc.wantAbsent {
				t.Fatalf("member=%+v err=%v absent=%v", member, err, groupMemberAbsent(err))
			}
			calls := channel.callsSnapshot()
			if len(calls) != 1 || calls[0].action != "get_group_member_list" || calls[0].params["no_cache"] != true {
				t.Fatalf("not a fresh roster lookup: %+v", calls)
			}
			identity := runIdentityCheck(t, r, event, map[string]any{"user_id": "41001", "check_group_role": true})
			if !tc.wantMember && (identity.GroupRole != "" || identity.GroupRoleVerified != "" || identity.GroupRoleError == "") {
				t.Fatalf("identity_check claimed membership: %+v", identity)
			}
			// Exercise the actual model-facing tool, not only the directory helper.
			tool := newDianaPlatformTool(context.Background(), r, event)
			output, toolErr := tool.Run(context.Background(), map[string]any{"operation": "member_info", "user_id": "41001"})
			if tc.wantMember {
				if toolErr != nil || !strings.Contains(output, `"membership_verified":true`) {
					t.Fatalf("output=%s err=%v", output, toolErr)
				}
			} else if toolErr == nil || strings.Contains(output, "[diana-at:") {
				t.Fatalf("unverified member exposed: %s %v", output, toolErr)
			}
		})
	}
}

func TestOneBotProfileAloneDoesNotVerifyMembership(t *testing.T) {
	for _, data := range []map[string]any{
		{"user_id": "41001"},
		{"user_id": "41001", "nickname": "T", "role": "member"},
	} {
		member := oneBotGroupMemberInfoFromData("51001", data)
		if member.MembershipVerified || groupMemberToolItem(member).Mention != "" {
			t.Fatalf("profile treated as current membership: %+v", member)
		}
	}
}

// 重放生产截图中的账号和查询失败结果；只调用模型，不向 QQ 群发送消息。
func TestLiveProductionNonMemberLookupReplay(t *testing.T) {
	client := liveLLMClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_member_info": {"user_id": "41001", "role": "member"},
		"get_group_member_list": {"items": []any{}},
		"get_stranger_info":     {"user_id": "41001", "nickname": "T"},
	}}
	event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "51001", UserID: "owner"}
	tool, _, _ := platformToolFor(t, BotConfig{Platform: PlatformOneBotV11, OwnerID: "owner"}, channel, event)
	lookupResult, lookupErr := tool.Run(ctx, map[string]any{"operation": "member_info", "user_id": "41001"})
	if lookupErr != nil {
		t.Fatal(lookupErr)
	}
	response, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
		{Role: "system", Content: "你是嘉然。依据工具查询结果回答，不编造身份。仅输出 JSON：reply（给用户的中文回复）、membership（member/absent/unknown）、mention（是否要 @ 该账号，布尔值）。"},
		{Role: "user", Content: "用户发 QQ 资料截图问：这谁。图片显示昵称 T，QQ 41001。platform member_info 的实际结果：" + lookupResult},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response == nil {
		t.Fatal("empty model response")
	}
	var answer struct {
		Reply      string `json:"reply"`
		Membership string `json:"membership"`
		Mention    bool   `json:"mention"`
	}
	if err := json.Unmarshal([]byte(response.Text), &answer); err != nil {
		t.Fatalf("invalid model output: %v", err)
	}
	if answer.Reply == "" || (answer.Membership != "absent" && answer.Membership != "unknown") || answer.Mention || strings.Contains(answer.Reply, "[diana-at:") || strings.Contains(answer.Reply, "@41001") {
		t.Fatalf("false membership or mention: %+v", answer)
	}
	t.Logf("model=%s membership=%s mention=%v reply=%s", response.Model, answer.Membership, answer.Mention, answer.Reply)
}

type memberLookupFailureChannel struct {
	*recordingChannel
	err error
}

func (c *memberLookupFailureChannel) CallAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, c.err
}

func TestOneBotMemberLookupFailureIsUnknown(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, errors.New("network disconnected"), errors.New("rate limited")} {
		channel := &memberLookupFailureChannel{recordingChannel: &recordingChannel{}, err: cause}
		r := NewRuntime(BotConfig{Platform: PlatformOneBotV11}, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
		member, err := r.getCurrentGroupMemberForTool(context.Background(), MessageEvent{Platform: PlatformOneBotV11}, "51001", "41001")
		if !errors.Is(err, cause) || groupMemberAbsent(err) || member.MembershipVerified || !strings.Contains(err.Error(), "无法确认") {
			t.Fatalf("lookup failure converted to membership: %+v %v", member, err)
		}
	}
}

func TestOneBotMemberListRefreshesBeforeVerifying(t *testing.T) {
	r := &Runtime{}
	members, err := r.getGroupMemberList(context.Background(), "51001", func(_ context.Context, action string, params map[string]any) (map[string]any, error) {
		if action != "get_group_member_list" || params["no_cache"] != true {
			t.Fatalf("cached membership lookup: %s %+v", action, params)
		}
		return map[string]any{"items": []any{map[string]any{"user_id": "123456"}}}, nil
	})
	if err != nil || len(members) != 1 || !members[0].MembershipVerified {
		t.Fatalf("members=%+v err=%v", members, err)
	}
}

func TestOneBotNonMemberFallsBackToAccountProfile(t *testing.T) {
	for _, profile := range []map[string]any{
		{"user_id": "41001", "nickname": "T", "role": "owner", "phone": "must-not-leak"},
		{"user_id": "different", "nickname": "Someone"},
		{"user_id": "41001"},
	} {
		channel := &recordingChannel{apiResponses: map[string]map[string]any{
			"get_group_member_list": {"items": []any{}}, "get_stranger_info": profile,
		}}
		event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, GroupID: "51001", UserID: "owner"}
		tool, runtime, _ := platformToolFor(t, BotConfig{Platform: PlatformOneBotV11, OwnerID: "owner"}, channel, event)
		output, err := tool.Run(context.Background(), map[string]any{"operation": "member_info", "user_id": "41001"})
		valid := profile["user_id"] == "41001" && profile["nickname"] == "T"
		if valid {
			if err != nil || !strings.Contains(output, `"nickname":"T"`) || !strings.Contains(output, `"membership_verified":false`) || strings.Contains(output, "[diana-at:") || strings.Contains(output, "must-not-leak") || strings.Contains(output, `"role":"owner"`) {
				t.Fatalf("profile mistaken for membership: %s %v", output, err)
			}
		} else if err == nil {
			t.Fatalf("invalid account profile accepted: %s", output)
		}
		identity := runIdentityCheck(t, runtime, event, map[string]any{"user_id": "41001", "check_group_role": true})
		if identity.GroupRole != "" || identity.GroupRoleVerified != "" || identity.GroupRoleError == "" {
			t.Fatalf("profile granted group role: %+v", identity)
		}
		if valid && (identity.DisplayName != "T" || identity.ProfileSource != "onebot_get_stranger_info") {
			t.Fatalf("profile missing: %+v", identity)
		}
		for _, call := range channel.callsSnapshot() {
			if call.action == "get_stranger_info" && (call.params["no_cache"] != true || stringFromAny(call.params["user_id"]) != "41001") {
				t.Fatalf("wrong profile request: %+v", call)
			}
		}
	}
}
