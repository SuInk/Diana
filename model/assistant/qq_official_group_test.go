// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// qqGroupFakeAPI 按「方法 路径」回固定 JSON；值为 nil 的路径回 403 + 11253。
type qqGroupFakeAPI struct {
	mu       sync.Mutex
	routes   map[string]any
	requests []string
	server   *httptest.Server
}

func newQQGroupFakeAPI(t *testing.T, routes map[string]any) *qqGroupFakeAPI {
	t.Helper()
	api := &qqGroupFakeAPI{routes: routes}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		api.mu.Lock()
		api.requests = append(api.requests, key)
		body, ok := api.routes[key]
		api.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if body == nil {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":11253,"message":"no permission"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (a *qqGroupFakeAPI) channel() *QQOfficialChannel {
	channel := NewQQOfficialChannel(QQOfficialConfig{AppID: "app", AppSecret: "secret"})
	channel.apiBaseOverride = a.server.URL
	channel.tokens = &platformTokenCache{fetch: func(context.Context) (string, time.Duration, error) {
		return "token", time.Hour, nil
	}}
	return channel
}

func qqGroupCtx() context.Context {
	return withDirectoryEvent(context.Background(), MessageEvent{Platform: PlatformQQOfficial, PlatformScope: "qq_group", GroupID: "GOPENID"})
}

func TestQQOfficialGroupInfoUsesV2Info(t *testing.T) {
	api := newQQGroupFakeAPI(t, map[string]any{
		"GET /v2/groups/GOPENID/info": map[string]any{
			"group_openid": "GOPENID", "group_name": "读书会", "group_finger_memo": "每周共读",
			"group_class_text": "文化", "group_tags": []string{"阅读", "成长"}, "group_member_num": 256,
		},
	})
	info, err := api.channel().GroupInfo(qqGroupCtx(), "GOPENID")
	if err != nil {
		t.Fatal(err)
	}
	if info.GroupName != "读书会" || info.Description != "每周共读" || info.Category != "文化" ||
		strings.Join(info.Tags, ",") != "阅读,成长" || info.MemberCount != 256 || !info.MemberCountKnown {
		t.Fatalf("info = %#v", info)
	}
	// 控制台没有事件上下文、ID 也不是见过的子频道，同样按 QQ 群查。
	if _, err := api.channel().GroupInfo(context.Background(), "GOPENID"); err != nil {
		t.Fatalf("console lookup: %v", err)
	}
}

func TestQQOfficialGroupMembersPaginatesByCursor(t *testing.T) {
	api := newQQGroupFakeAPI(t, map[string]any{
		"GET /v2/groups/GOPENID/info": map[string]any{"group_member_num": 3},
		"GET /v2/groups/GOPENID/members?cursor=": map[string]any{
			"members": []any{
				map[string]any{"member_openid": "M1", "username": "甲", "member_role": "owner"},
				map[string]any{"member_openid": "M2", "username": "乙", "member_role": "member", "bot": true},
			},
			"next_cursor": "C2",
		},
		"GET /v2/groups/GOPENID/members?cursor=C2": map[string]any{
			"members":     []any{map[string]any{"member_openid": "M3", "username": "丙", "member_role": "admin"}},
			"next_cursor": "",
		},
	})
	dir, err := api.channel().GroupMembers(qqGroupCtx(), "GOPENID")
	if err != nil {
		t.Fatal(err)
	}
	if !dir.Complete || dir.Total != 3 || len(dir.Members) != 3 || dir.Members[0].Role != "owner" || !dir.Members[1].IsBot || dir.Members[2].Nickname != "丙" {
		t.Fatalf("directory = %#v", dir)
	}
}

// 成员接口只对白名单开放：名单回落成空表加说明，单查成员给出可识别的错误。
func TestQQOfficialGroupMembersFallsBackOn11253(t *testing.T) {
	api := newQQGroupFakeAPI(t, map[string]any{
		"GET /v2/groups/GOPENID/info":            map[string]any{"group_member_num": 9},
		"GET /v2/groups/GOPENID/members?cursor=": nil,
		"GET /v2/groups/GOPENID/members/M1":      nil,
	})
	channel := api.channel()
	dir, err := channel.GroupMembers(qqGroupCtx(), "GOPENID")
	if err != nil || dir.Complete || len(dir.Members) != 0 || len(dir.Warnings) == 0 || dir.Total != 9 {
		t.Fatalf("directory = %#v err=%v", dir, err)
	}
	if _, err := channel.GroupMember(qqGroupCtx(), "GOPENID", "M1"); !errors.Is(err, errQQGroupNoPermission) {
		t.Fatalf("member error = %v", err)
	}
}

// 问机器人自己的身份走人人可用的 bot_state，不依赖白名单。
func TestQQOfficialGroupMemberSelfUsesBotState(t *testing.T) {
	api := newQQGroupFakeAPI(t, map[string]any{
		"GET /v2/groups/GOPENID/bot_state": map[string]any{"member_openid": "BOTOPENID", "member_role": "admin", "allow_proactive_msg": true, "recv_msg_setting": "all"},
		"GET /v2/groups/GOPENID/members/M1": map[string]any{
			"member_openid": "M1", "username": "甲", "member_role": "member",
		},
	})
	channel := api.channel()
	self, err := channel.GroupMember(qqGroupCtx(), "GOPENID", "app")
	if err != nil || self.Role != "admin" || !self.IsBot {
		t.Fatalf("self = %#v err=%v", self, err)
	}
	if !channel.isQQGroupSelf("GOPENID", "BOTOPENID") {
		t.Fatal("bot_state openid should be remembered as self")
	}
	member, err := channel.GroupMember(qqGroupCtx(), "GOPENID", "M1")
	if err != nil || member.Role != "member" || member.Nickname != "甲" {
		t.Fatalf("member = %#v err=%v", member, err)
	}
}

func TestQQOfficialGroupBlacklist(t *testing.T) {
	api := newQQGroupFakeAPI(t, map[string]any{
		"GET /v2/groups/GOPENID/member_blacklist?cursor=&limit=100": map[string]any{
			"users":       []any{map[string]any{"member_openid": "M1", "username": "甲", "banned_at": "2025-07-01T10:30:00+08:00"}},
			"next_cursor": "",
		},
		"POST /v2/groups/GOPENID/member_blacklist": map[string]any{"fail_openids": []string{"M2"}},
	})
	channel := api.channel()
	users, err := channel.QQGroupBlacklist(context.Background(), "GOPENID")
	if err != nil || len(users) != 1 || users[0].MemberOpenID != "M1" {
		t.Fatalf("blacklist = %#v err=%v", users, err)
	}
	failed, err := channel.SetQQGroupBlacklist(context.Background(), "GOPENID", "del", []string{"M1", "M2"})
	if err != nil || len(failed) != 1 || failed[0] != "M2" {
		t.Fatalf("set blacklist failed=%v err=%v", failed, err)
	}
	if _, err := channel.SetQQGroupBlacklist(context.Background(), "GOPENID", "kick", []string{"M1"}); err == nil {
		t.Fatal("unknown op must be refused before calling the platform")
	}
	tooMany := make([]string, qqGroupBatchLimit+1)
	if _, err := channel.SetQQGroupBlacklist(context.Background(), "GOPENID", "add", tooMany); err == nil {
		t.Fatal("more than 20 members must be refused")
	}
}

func TestQQGroupMuteUpdatesExistingAndClamps(t *testing.T) {
	var got []map[string]any
	call := func(_ context.Context, action string, params map[string]any) (map[string]any, error) {
		if strings.HasPrefix(action, "GET ") {
			return map[string]any{"members": []any{map[string]any{"member_openid": "M1"}}}, nil
		}
		got = append(got, params["members"].([]any)[0].(map[string]any))
		return map[string]any{}, nil
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := qqGroupMute(context.Background(), call, "GOPENID", "M1", 90*24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if got[0]["op"] != "update" || got[0]["mute_expire_at"] != now.Add(qqGroupMuteMax).Format(time.RFC3339) {
		t.Fatalf("mute = %#v", got[0])
	}
}

func TestQQOfficialGroupEventsFromDispatch(t *testing.T) {
	add, ok, handled := qqOfficialGroupEventFromDispatch("GROUP_MEMBER_ADD", json.RawMessage(`{"timestamp":1784276757,"group_openid":"GOPENID","member_openid":"M1"}`), "self")
	if !handled || !ok || add.Kind != EventKindNotice || add.SubType != "group_increase" || add.GroupID != "GOPENID" || add.UserID != "M1" || add.PlatformScope != "qq_group" {
		t.Fatalf("member add = %#v", add)
	}
	leave, ok, _ := qqOfficialGroupEventFromDispatch("GROUP_MEMBER_REMOVE", json.RawMessage(`{"timestamp":1,"group_openid":"GOPENID","member_openid":"M1"}`), "self")
	if !ok || leave.SubType != "group_decrease" {
		t.Fatalf("member remove = %#v", leave)
	}
	for eventType, sub := range map[string]string{
		"GROUP_ADD_ROBOT": qqOfficialNoticeBotJoin, "GROUP_DEL_ROBOT": qqOfficialNoticeBotLeave,
		"GROUP_MSG_REJECT": qqOfficialNoticeProactiveReject, "GROUP_MSG_RECEIVE": qqOfficialNoticeProactiveAccept,
	} {
		event, ok, _ := qqOfficialGroupEventFromDispatch(eventType, json.RawMessage(`{"group_openid":"GOPENID","op_member_openid":"OP1","timestamp":1}`), "self")
		if !ok || event.SubType != sub || event.UserID != "OP1" || event.OperatorID != "OP1" {
			t.Fatalf("%s = %#v", eventType, event)
		}
	}
	if _, _, handled := qqOfficialGroupEventFromDispatch("GROUP_AT_MESSAGE_CREATE", json.RawMessage(`{}`), "self"); handled {
		t.Fatal("message events must stay on the message path")
	}
}

func TestQQOfficialJoinRequestEventAndApproval(t *testing.T) {
	raw := json.RawMessage(`{"group_openid":"GOPENID","join_request_id":"REQ1","member_openid":"M1","username":"申请人","apply_at":"2026-08-05T17:32:52+08:00","apply_source":"invited","invited_by":"M9","risk_tips":"warning_tips","verify_info":{"method":"admin_review_qa","review_qa_list":[{"question":"从哪来","answer":"朋友推荐"}]}}`)
	event, ok, handled := qqOfficialGroupEventFromDispatch("GROUP_JOIN_REQUEST", raw, "self")
	if !handled || !ok || event.Kind != EventKindRequest || event.oneBotRequest == nil {
		t.Fatalf("join request = %#v", event)
	}
	req := event.oneBotRequest
	for _, want := range []string{"朋友推荐", "邀请人：M9", "可疑申请"} {
		if !strings.Contains(req.Comment, want) {
			t.Fatalf("comment %q missing %q", req.Comment, want)
		}
	}
	item := OneBotRequestRecord{ID: "r1", Platform: PlatformQQOfficial, RequestType: "group", SubType: "add", Flag: req.Flag}
	action, params, err := oneBotRequestAction(item, true, nil)
	if err != nil || action != "POST /v2/groups/GOPENID/approval_join_request/M1" || params["op"] != "approve" || params["join_request_id"] != "REQ1" {
		t.Fatalf("approve = %s %#v %v", action, params, err)
	}
	_, params, err = oneBotRequestAction(item, false, map[string]any{"reason": "不符合要求"})
	if err != nil || params["op"] != "decline" || params["reject_reason"] != "不符合要求" {
		t.Fatalf("decline = %#v %v", params, err)
	}
	// 平台按策略已自动通过的申请没有待办。
	auto := json.RawMessage(`{"group_openid":"GOPENID","join_request_id":"REQ2","member_openid":"M2","auto_approved":{"strategy_id":"st_1"}}`)
	if _, ok, handled := qqOfficialGroupEventFromDispatch("GROUP_JOIN_REQUEST", auto, "self"); !handled || ok {
		t.Fatal("auto approved request must not become pending")
	}
}

func TestPlatformRequestsListSyncsQQOfficialJoinRequests(t *testing.T) {
	store := &memoryOneBotRequestStore{}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"GET /v2/groups/GOPENID/join_request_list": {
			"list":        []any{map[string]any{"join_request_id": "REQ1", "member_openid": "M1", "username": "甲", "verify_info": map[string]any{"method": "verify_message", "verify_message": "想进群"}}},
			"next_cursor": "",
		},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", Platform: PlatformQQOfficial}, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetOneBotRequestStore(store)
	event := MessageEvent{Platform: PlatformQQOfficial, PlatformScope: "qq_group", Kind: EventKindGroup, GroupID: "GOPENID", UserID: "owner", SelfID: "self"}
	output, err := newDianaOneBotRequestsTool(runtime, event).Run(context.Background(), map[string]any{"operation": "list"})
	if err != nil || !strings.Contains(output, "补拉到 1 条") || !strings.Contains(output, "想进群") {
		t.Fatalf("output=%s err=%v", output, err)
	}
}

func TestQQOfficialMemberIntentDroppedAfterRejection(t *testing.T) {
	channel := NewQQOfficialChannel(QQOfficialConfig{AppID: "app", AppSecret: "secret"})
	if channel.identifyIntents()&qqIntentGroupMembers == 0 {
		t.Fatal("member intent should be requested by default")
	}
	channel.noteIntentRejected(errors.New("io timeout"))
	if channel.identifyIntents()&qqIntentGroupMembers == 0 {
		t.Fatal("unrelated errors must not drop the intent")
	}
	channel.noteIntentRejected(&websocket.CloseError{Code: 4014, Text: "disallowed intents"})
	if got := channel.identifyIntents(); got&qqIntentGroupMembers != 0 || got&qqIntentGroupAndC2C == 0 {
		t.Fatalf("intents after rejection = %b", got)
	}
}

func TestQQOfficialJoinApprovalStrategy(t *testing.T) {
	api := newQQGroupFakeAPI(t, map[string]any{
		"GET /v2/groups/join_approval_strategy?cursor=&limit=50": map[string]any{
			"strategies":  []any{map[string]any{"strategy_id": "st_1", "is_enable": "on"}},
			"next_cursor": "N2",
		},
		"GET /v2/groups/join_approval_strategy?cursor=N2&limit=50": map[string]any{
			"strategies":  []any{map[string]any{"strategy_id": "st_2", "is_enable": "off"}},
			"next_cursor": "",
		},
		"POST /v2/groups/join_approval_strategy":                      map[string]any{"strategy_id": "st_3", "is_enable": "on"},
		"PATCH /v2/groups/join_approval_strategy/st_1":                map[string]any{},
		"POST /v2/groups/join_approval_strategy/st_1/execute":         map[string]any{},
		"POST /v2/groups/join_approval_strategy/st_1/whitelist_users": map[string]any{"whitelist_user_count": 2},
		"DELETE /v2/groups/join_approval_strategy/st_1":               map[string]any{},
	})
	channel := api.channel()
	ctx := context.Background()
	list, err := channel.JoinApprovalStrategy(ctx, QQJoinStrategyRequest{Op: "list"})
	if err != nil || len(list["strategies"].([]any)) != 2 {
		t.Fatalf("list = %#v err=%v", list, err)
	}
	if _, err := channel.JoinApprovalStrategy(ctx, QQJoinStrategyRequest{Op: "create", Body: map[string]any{"group_openids": []string{"G1"}, "remark": "测试"}}); err != nil {
		t.Fatal(err)
	}
	for _, req := range []QQJoinStrategyRequest{
		{Op: "update", StrategyID: "st_1", Body: map[string]any{"is_enable": "off", "group_action": map[string]any{"op": "add", "group_openids": []any{"G2"}}}},
		{Op: "execute", StrategyID: "st_1"},
		{Op: "whitelist", StrategyID: "st_1", Body: map[string]any{"op": "add", "whitelist_users": []any{"10001", "10002"}}},
		{Op: "delete", StrategyID: "st_1"},
	} {
		if _, err := channel.JoinApprovalStrategy(ctx, req); err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
	}
	// 参数按文档先在本地挡掉，不打到平台。
	before := len(api.requests)
	for _, req := range []QQJoinStrategyRequest{
		{Op: "create", Body: map[string]any{"group_openids": []string{"G1"}, "group_ids": []string{"20001"}}},
		{Op: "create", Body: map[string]any{"group_ids": []string{"20001"}, "is_enable": "yes"}},
		{Op: "create", Body: map[string]any{"group_ids": []string{"20001"}, "remark": strings.Repeat("字", 256)}},
		{Op: "update", StrategyID: "st_1", Body: map[string]any{"group_action": map[string]any{"op": "move"}}},
		{Op: "whitelist", StrategyID: "st_1", Body: map[string]any{"op": "add"}},
		{Op: "execute"},
		{Op: "rename", StrategyID: "st_1"},
	} {
		if _, err := channel.JoinApprovalStrategy(ctx, req); err == nil {
			t.Fatalf("%s %#v should be refused", req.Op, req.Body)
		}
	}
	if len(api.requests) != before {
		t.Fatalf("invalid requests reached platform: %v", api.requests[before:])
	}
}
