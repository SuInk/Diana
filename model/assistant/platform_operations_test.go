// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPlatformToolGroupNameAndAdminsOnOneBot(t *testing.T) {
	tool, _, channel := governanceToolFor(t, PlatformOneBotV11, "owner")
	for _, input := range []map[string]any{
		{"operation": "set_group_name", "name": "新群名"},
		{"operation": "set_admin", "user_id": "555"},
		{"operation": "unset_admin", "user_id": "555"},
	} {
		if _, err := tool.Run(context.Background(), input); err != nil {
			t.Fatalf("%v error = %v", input["operation"], err)
		}
	}
	calls := channel.callsSnapshot()
	if got := recordedCallsByAction(calls, "set_group_name"); len(got) != 1 || got[0].params["group_name"] != "新群名" {
		t.Fatalf("set_group_name calls = %#v", got)
	}
	admins := recordedCallsByAction(calls, "set_group_admin")
	if len(admins) != 2 || admins[0].params["enable"] != true || admins[1].params["enable"] != false || fmt.Sprint(admins[0].params["user_id"]) != "555" {
		t.Fatalf("set_group_admin calls = %#v", admins)
	}
	// OneBot 没有邀请链接接口，如实拒绝。
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "invite_link"}); err == nil || !strings.Contains(err.Error(), "暂不支持") {
		t.Fatalf("invite_link on OneBot error = %v", err)
	}
}

// QQ 群的管理员只有群主能任免：机器人只是管理员时在门口拒掉。
func TestPlatformToolSetAdminNeedsBotGroupOwnerOnQQ(t *testing.T) {
	tool, _, channel := governanceToolFor(t, PlatformOneBotV11, "admin")
	_, err := tool.Run(context.Background(), map[string]any{"operation": "set_admin", "user_id": "555"})
	if err == nil || !strings.Contains(err.Error(), "只有群主能设置") {
		t.Fatalf("set_admin as admin bot error = %v", err)
	}
	if got := recordedCallsByAction(channel.callsSnapshot(), "set_group_admin"); len(got) != 0 {
		t.Fatalf("must not call set_group_admin: %#v", got)
	}
}

// 群管理员能改群名，但不能借机器人任免管理员；群主可以。
func TestPlatformToolAdminAppointmentIsGroupOwnerOnly(t *testing.T) {
	roles := moderationRoles()
	roles["10000"] = "owner"
	channel := newRoleTestChannel(roles)
	run := func(actor string, input map[string]any) error {
		event := MessageEvent{Kind: EventKindGroup, UserID: actor, GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11}
		tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)
		_, err := tool.Run(context.Background(), input)
		return err
	}
	if err := run("admin1", map[string]any{"operation": "set_group_name", "name": "x"}); err != nil {
		t.Fatalf("admin set_group_name error = %v", err)
	}
	if err := run("admin1", map[string]any{"operation": "set_admin", "user_id": "555"}); err == nil || !strings.Contains(err.Error(), "只有机器人主人和群主") {
		t.Fatalf("admin set_admin error = %v", err)
	}
	if err := run("gowner", map[string]any{"operation": "unset_admin", "user_id": "admin1"}); err != nil {
		t.Fatalf("group owner unset_admin error = %v", err)
	}
	if got := recordedCallsByAction(channel.callsSnapshot(), "set_group_admin"); len(got) != 1 || fmt.Sprint(got[0].params["user_id"]) != "admin1" {
		t.Fatalf("set_group_admin calls = %#v", got)
	}
}

func TestPlatformToolTelegramGroupOperations(t *testing.T) {
	tool, _, channel := governanceToolFor(t, PlatformTelegram, "administrator")
	channel.apiResponses["createChatInviteLink"] = map[string]any{"invite_link": "https://t.me/+abc"}
	for _, input := range []map[string]any{
		{"operation": "set_group_name", "name": "New title"},
		{"operation": "set_admin", "user_id": "555"},
		{"operation": "unset_admin", "user_id": "555"},
	} {
		if _, err := tool.Run(context.Background(), input); err != nil {
			t.Fatalf("%v error = %v", input["operation"], err)
		}
	}
	output, err := tool.Run(context.Background(), map[string]any{"operation": "invite_link"})
	if err != nil || !strings.Contains(output, "https://t.me/+abc") {
		t.Fatalf("invite_link output=%s err=%v", output, err)
	}
	calls := channel.callsSnapshot()
	if got := recordedCallsByAction(calls, "setChatTitle"); len(got) != 1 || got[0].params["title"] != "New title" {
		t.Fatalf("setChatTitle calls = %#v", got)
	}
	promote := recordedCallsByAction(calls, "promoteChatMember")
	if len(promote) != 2 {
		t.Fatalf("promoteChatMember calls = %#v", promote)
	}
	if promote[0].params["can_restrict_members"] != true || promote[0].params["can_promote_members"] != nil || promote[0].params["can_change_info"] != nil {
		t.Fatalf("promote params = %#v", promote[0].params)
	}
	if promote[1].params["can_restrict_members"] != false || promote[1].params["can_promote_members"] != false {
		t.Fatalf("demote params = %#v", promote[1].params)
	}
	// 不能用 exportChatInviteLink：它会作废已经发出去的主链接。
	if got := recordedCallsByAction(calls, "exportChatInviteLink"); len(got) != 0 {
		t.Fatalf("must not revoke primary link: %#v", got)
	}
}

func TestPlatformToolOwnerOnlyOperations(t *testing.T) {
	channel := newModerationTestChannel("owner")
	channel.apiResponses["get_friend_list"] = map[string]any{"items": []any{
		map[string]any{"user_id": 10001, "nickname": "甲", "remark": "老朋友"},
		map[string]any{"user_id": 10002, "nickname": "乙"},
	}}
	cfg := BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: PlatformOneBotV11}

	// 群主和群管理员都不行，只认主人。
	admin, _, _ := platformToolFor(t, cfg, channel, MessageEvent{Kind: EventKindGroup, UserID: "someone", GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11})
	if _, err := admin.Run(context.Background(), map[string]any{"operation": "leave_group"}); err == nil || !strings.Contains(err.Error(), "只有机器人主人") {
		t.Fatalf("non-owner leave_group error = %v", err)
	}
	if _, err := admin.Run(context.Background(), map[string]any{"operation": "friend_list"}); err == nil || !strings.Contains(err.Error(), "只有主人") {
		t.Fatalf("non-owner friend_list error = %v", err)
	}
	if got := channel.callsSnapshot(); len(recordedCallsByAction(got, "set_group_leave"))+len(recordedCallsByAction(got, "get_friend_list")) != 0 {
		t.Fatalf("non-owner reached platform: %#v", got)
	}

	// 主人在私聊里也能让机器人退出指定的群。
	owner, _, _ := platformToolFor(t, cfg, channel, MessageEvent{Kind: EventKindPrivate, UserID: "owner", SelfID: "10000", Platform: PlatformOneBotV11})
	if _, err := owner.Run(context.Background(), map[string]any{"operation": "leave_group"}); err == nil || !strings.Contains(err.Error(), "需要群 ID") {
		t.Fatalf("leave_group without group error = %v", err)
	}
	if _, err := owner.Run(context.Background(), map[string]any{"operation": "leave_group", "group_id": "456"}); err != nil {
		t.Fatalf("owner leave_group error = %v", err)
	}
	leave := recordedCallsByAction(channel.callsSnapshot(), "set_group_leave")
	if len(leave) != 1 || fmt.Sprint(leave[0].params["group_id"]) != "456" || leave[0].params["is_dismiss"] != false {
		t.Fatalf("set_group_leave calls = %#v", leave)
	}
	output, err := owner.Run(context.Background(), map[string]any{"operation": "friend_list"})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		FriendCount int                       `json:"friend_count"`
		Friends     []dianaPlatformFriendItem `json:"friends"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil || payload.FriendCount != 2 || payload.Friends[0].Remark != "老朋友" || payload.Friends[1].UserID != "10002" {
		t.Fatalf("friend_list output=%s err=%v", output, err)
	}
}

func TestPlatformToolTelegramLeaveAndNoFriendList(t *testing.T) {
	channel := newModerationTestChannel("administrator")
	cfg := BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: PlatformTelegram}
	tool, _, _ := platformToolFor(t, cfg, channel, MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "-1001", SelfID: "10000", Platform: PlatformTelegram})
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "friend_list"}); err == nil || !strings.Contains(err.Error(), "不提供好友列表") {
		t.Fatalf("telegram friend_list error = %v", err)
	}
	output, err := tool.Run(context.Background(), map[string]any{"operation": "leave_group"})
	if err != nil || !strings.Contains(output, "当前这个群") {
		t.Fatalf("leave_group output=%s err=%v", output, err)
	}
	if got := recordedCallsByAction(channel.callsSnapshot(), "leaveChat"); len(got) != 1 || got[0].params["chat_id"] != "-1001" {
		t.Fatalf("leaveChat calls = %#v", got)
	}
}

func qqOfficialToolFor(t *testing.T, event MessageEvent) (*dianaPlatformTool, *moderationTestChannel) {
	t.Helper()
	channel := newModerationTestChannel("admin")
	event.Platform = PlatformQQOfficial
	event.SelfID = "bot"
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "bot", Platform: PlatformQQOfficial}, channel, event)
	return tool, channel
}

// QQ 群里开放平台只给撤回自己消息的接口，群管操作一律如实拒绝，不碰接口。
func TestPlatformToolQQOfficialGroupOnlyRecalls(t *testing.T) {
	tool, channel := qqOfficialToolFor(t, MessageEvent{Kind: EventKindGroup, PlatformScope: "qq_group", UserID: "owner", GroupID: "GROUPOPENID"})
	for _, input := range []map[string]any{
		{"operation": "mute", "user_id": "member1", "duration": 60},
		{"operation": "kick", "user_id": "member1"},
		{"operation": "mute_all"},
		{"operation": "essence_set", "message_id": "m1"},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil || !strings.Contains(err.Error(), "只在 QQ 频道里提供") {
			t.Fatalf("%v error = %v", input["operation"], err)
		}
	}
	if calls := channel.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("qq group moderation reached platform: %#v", calls)
	}
}

func TestPlatformToolQQOfficialGuildModeration(t *testing.T) {
	tool, channel := qqOfficialToolFor(t, MessageEvent{Kind: EventKindGroup, PlatformScope: "qq_guild", GuildID: "G1", UserID: "owner", GroupID: "C1"})
	for _, input := range []map[string]any{
		{"operation": "mute", "user_id": "u1", "duration": 120},
		{"operation": "unmute", "user_id": "u1"},
		{"operation": "kick", "user_id": "u1", "reject_add_request": true},
		{"operation": "mute_all"},
		{"operation": "unmute_all"},
		{"operation": "essence_set", "message_id": "m1"},
		{"operation": "essence_unset", "message_id": "m1"},
	} {
		if _, err := tool.Run(context.Background(), input); err != nil {
			t.Fatalf("%v error = %v", input["operation"], err)
		}
	}
	calls := channel.callsSnapshot()
	want := []struct {
		action string
		check  func(map[string]any) bool
	}{
		{"PATCH /guilds/G1/members/u1/mute", func(p map[string]any) bool { return p["mute_seconds"] == "120" }},
		{"PATCH /guilds/G1/members/u1/mute", func(p map[string]any) bool { return p["mute_seconds"] == "0" }},
		{"DELETE /guilds/G1/members/u1", func(p map[string]any) bool { return p["add_blacklist"] == true }},
		{"PATCH /guilds/G1/mute", func(p map[string]any) bool { return p["mute_seconds"] == "2592000" }},
		{"PATCH /guilds/G1/mute", func(p map[string]any) bool { return p["mute_seconds"] == "0" }},
		{"PUT /channels/C1/pins/m1", func(map[string]any) bool { return true }},
		{"DELETE /channels/C1/pins/m1", func(map[string]any) bool { return true }},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %#v", calls)
	}
	for i, w := range want {
		if calls[i].action != w.action || !w.check(calls[i].params) {
			t.Fatalf("call %d = %#v, want %s", i, calls[i], w.action)
		}
	}
	// 群管理员不能借机器人去管别的子频道。
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "mute_all", "group_id": "C2"}); err == nil || !strings.Contains(err.Error(), "当前子频道") {
		t.Fatalf("other channel error = %v", err)
	}
}

func TestQQOfficialDeleteMessagePathByScene(t *testing.T) {
	cases := []struct {
		event MessageEvent
		want  string
	}{
		{MessageEvent{Kind: EventKindGroup, PlatformScope: "qq_group", GroupID: "GOPENID"}, "/v2/groups/GOPENID/messages/m1"},
		{MessageEvent{Kind: EventKindGroup, PlatformScope: "qq_guild", GroupID: "C1", GuildID: "G1"}, "/channels/C1/messages/m1?hidetip=true"},
		{MessageEvent{Kind: EventKindPrivate, UserID: "UOPENID"}, "/v2/users/UOPENID/messages/m1"},
	}
	for _, c := range cases {
		got, err := qqOfficialDeleteMessagePath(c.event, "m1")
		if err != nil || got != c.want {
			t.Fatalf("path(%#v) = %q, %v; want %q", c.event, got, err, c.want)
		}
	}
	if _, err := qqOfficialDeleteMessagePath(MessageEvent{Kind: EventKindGroup, GroupID: "G"}, " "); err == nil {
		t.Fatal("empty message id must fail")
	}
}

func TestQQOfficialGuildListParsesArrayResult(t *testing.T) {
	tool, channel := qqOfficialToolFor(t, MessageEvent{Kind: EventKindPrivate, UserID: "owner"})
	channel.apiResponses["GET /users/@me/guilds"] = map[string]any{"result": json.RawMessage(`[{"id":"G1","name":"频道一","member_count":42}]`)}
	output, err := tool.Run(context.Background(), map[string]any{"operation": "group_list"})
	if err != nil || !strings.Contains(output, `"group_count":1`) || !strings.Contains(output, "频道一") || !strings.Contains(output, "QQ 群开放平台不提供列表接口") {
		t.Fatalf("group_list output=%s err=%v", output, err)
	}
}

func TestTelegramJoinRequestBecomesPlatformRequest(t *testing.T) {
	event, ok := telegramJoinRequestEvent(&telegramJoinRequest{
		Chat: telegramChat{ID: -100123, Type: "supergroup"},
		From: telegramUser{ID: 4242, FirstName: "Ann"},
		Date: 1700000000,
		Bio:  "hello",
	}, "999")
	if !ok || event.Kind != EventKindRequest || event.Platform != PlatformTelegram || event.GroupID != "-100123" || event.UserID != "4242" {
		t.Fatalf("event = %#v ok=%v", event, ok)
	}
	if event.oneBotRequest == nil || event.oneBotRequest.Comment != "hello" || event.oneBotRequest.RequestType != "group" {
		t.Fatalf("request = %#v", event.oneBotRequest)
	}
	encoded, _ := json.Marshal(event)
	if strings.Contains(string(encoded), telegramJoinRequestFlagPrefix) {
		t.Fatalf("flag leaked into event JSON: %s", encoded)
	}
	chatID, userID, ok := parseTelegramJoinRequestFlag(event.oneBotRequest.Flag)
	if !ok || chatID != "-100123" || userID != "4242" {
		t.Fatalf("flag parse = %q %q %v", chatID, userID, ok)
	}
	if _, ok := telegramJoinRequestEvent(&telegramJoinRequest{Chat: telegramChat{ID: -1}}, "999"); ok {
		t.Fatal("request without user must be ignored")
	}
}

func TestPlatformRequestsToolApprovesTelegramJoinRequest(t *testing.T) {
	store := &memoryOneBotRequestStore{items: []OneBotRequestRecord{
		{ID: "tg-1", ProfileID: "bot-1", Platform: PlatformTelegram, RequestType: "group", SubType: "add", GroupID: "-100123", UserID: "4242", Flag: telegramJoinRequestFlag("-100123", "4242", 1700000000), Status: OneBotRequestPending, CreatedAt: time.Now()},
		{ID: "tg-2", ProfileID: "bot-1", Platform: PlatformTelegram, RequestType: "group", SubType: "add", GroupID: "-100123", UserID: "4343", Flag: telegramJoinRequestFlag("-100123", "4343", 1700000001), Status: OneBotRequestPending, CreatedAt: time.Now()},
	}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{ID: "bot-1", Platform: PlatformTelegram, OwnerID: "owner"}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetOneBotRequestStore(store)
	tool := newDianaOneBotRequestsTool(runtime, MessageEvent{ProfileID: "bot-1", Platform: PlatformTelegram, Kind: EventKindPrivate, UserID: "owner"})
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "approve", "id": "tg-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "reject", "id": "tg-2"}); err != nil {
		t.Fatal(err)
	}
	calls := channel.callsSnapshot()
	if len(calls) != 2 || calls[0].action != "approveChatJoinRequest" || calls[1].action != "declineChatJoinRequest" {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].params["chat_id"] != "-100123" || fmt.Sprint(calls[0].params["user_id"]) != "4242" {
		t.Fatalf("approve params = %#v", calls[0].params)
	}
}

func TestPlatformRequestsToolOnlyWhereRequestsExist(t *testing.T) {
	for platform, want := range map[string]bool{PlatformOneBotV11: true, PlatformTelegram: true, PlatformQQOfficial: false} {
		if got := platformSupportsJoinRequests(platform); got != want {
			t.Fatalf("platformSupportsJoinRequests(%s) = %v", platform, got)
		}
	}
}

// 断线期间的入群申请和被折叠的可疑好友申请收不到推送，list 时要从平台补拉。
func TestPlatformRequestsListSyncsMissedOneBotRequests(t *testing.T) {
	store := &memoryOneBotRequestStore{}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_system_msg": {"items": []any{
			map[string]any{"group_id": 20002, "group_name": "测试群", "request_id": 1, "requester_uin": 30003, "requester_nick": "甲", "message": "想进群", "checked": false, "flag": "20002|1|1"},
			map[string]any{"group_id": 20002, "request_id": 2, "requester_uin": 30004, "checked": true, "flag": "20002|2|1"},
		}},
		"get_doubt_friends_add_request": {"items": []any{
			map[string]any{"uid": "u_abc", "nick": "乙", "source": "群聊", "msg": "你好", "reqTime": 1700000000},
		}},
	}}
	runtime := NewRuntime(BotConfig{ID: "bot-1", Platform: PlatformOneBotV11, OwnerID: "owner"}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetOneBotRequestStore(store)
	tool := newDianaOneBotRequestsTool(runtime, MessageEvent{ProfileID: "bot-1", Platform: PlatformOneBotV11, Kind: EventKindPrivate, UserID: "owner"})

	output, err := tool.Run(context.Background(), map[string]any{"operation": "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "补拉到 2 条") || !strings.Contains(output, "想进群") || !strings.Contains(output, "昵称：乙") || strings.Contains(output, "u_abc") {
		t.Fatalf("list output = %s", output)
	}
	// 再列一次不会重复入库。
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "list"}); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 2 {
		t.Fatalf("store items = %#v", store.items)
	}
	var doubt OneBotRequestRecord
	for _, item := range store.items {
		if item.SubType == oneBotRequestSubTypeDoubt {
			doubt = item
		}
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "approve", "id": doubt.ID}); err != nil {
		t.Fatal(err)
	}
	got := recordedCallsByAction(channel.callsSnapshot(), "set_doubt_friends_add_request")
	if len(got) != 1 || got[0].params["flag"] != "u_abc" || got[0].params["approve"] != true {
		t.Fatalf("doubt approval calls = %#v", got)
	}

	// 同一个人之后再申请一次，是一条新的待处理请求，不能被旧记录去重吞掉。
	channel.apiResponses["get_doubt_friends_add_request"] = map[string]any{"items": []any{
		map[string]any{"uid": "u_abc", "nick": "乙", "msg": "再试一次", "reqTime": 1700090000},
	}}
	if output, err := tool.Run(context.Background(), map[string]any{"operation": "list"}); err != nil || !strings.Contains(output, "再试一次") {
		t.Fatalf("re-application list output=%s err=%v", output, err)
	}
}

// 两个收件箱都拉不到才算补拉失败；list 照常列出已存的请求。
func TestPlatformRequestsListToleratesSyncFailure(t *testing.T) {
	store := &memoryOneBotRequestStore{items: []OneBotRequestRecord{
		{ID: "r1", ProfileID: "bot-1", Platform: PlatformOneBotV11, RequestType: "friend", UserID: "30005", Flag: "f", Status: OneBotRequestPending, CreatedAt: time.Now()},
	}}
	runtime := NewRuntime(BotConfig{ID: "bot-1", Platform: PlatformOneBotV11, OwnerID: "owner"}.WithDefaults(), &failingOutboundChannel{err: fmt.Errorf("boom")}, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetOneBotRequestStore(store)
	tool := newDianaOneBotRequestsTool(runtime, MessageEvent{ProfileID: "bot-1", Platform: PlatformOneBotV11, Kind: EventKindPrivate, UserID: "owner"})
	output, err := tool.Run(context.Background(), map[string]any{"operation": "list"})
	if err != nil || !strings.Contains(output, "没能从平台补拉") || !strings.Contains(output, `"id":"r1"`) {
		t.Fatalf("list output=%s err=%v", output, err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "sync"}); err == nil {
		t.Fatal("sync must surface the failure")
	}
}
