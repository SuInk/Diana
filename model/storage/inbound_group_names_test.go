// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Telegram 这类平台没有「列出我加入的群」接口，控制台只能从本地事件认识群。
// 群名和归属机器人都得跟着一起取出来，否则群管理页只能显示一串群号，也无从
// 判断该不该按 QQ 的规则拼群头像。
func TestListInboundEventGroupsReadsNameAndProfileFromPayload(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "group-names.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now()
	insert := func(id, groupID, profileID, payload string, offset time.Duration) {
		t.Helper()
		at := now.Add(offset)
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO inbound_events (id, session, kind, profile_id, group_id, user_id, message_id, event_time, payload, priority, available_at, attempts, status, created_at, updated_at)
VALUES (?, ?, 'group', ?, ?, 'u1', ?, ?, ?, 0, ?, 0, ?, ?, ?)
`, id, "group:"+groupID, profileID, groupID, id, at.Unix(), payload, at.Unix(), inboundStatusDone, at.Unix(), at.Unix()); err != nil {
			t.Fatal(err)
		}
	}

	// 同一个群改过名字：应该取最近一条事件里的名字，而不是第一条。
	insert("tg-1", "-1001", "tg-profile", `{"group_name":"旧名字"}`, -30*time.Minute)
	insert("tg-2", "-1001", "tg-profile", `{"group_name":"读书会"}`, -5*time.Minute)
	// payload 里没有群名的事件（比如 OneBot 侧）不该把名字算成空以外的东西。
	insert("qq-1", "111", "qq-profile", `{}`, -10*time.Minute)
	// payload 不是合法 JSON 时只能没有名字，但不能让整个列表查询失败。
	insert("broken-1", "222", "qq-profile", `not json`, -9*time.Minute)

	groups, err := store.ListInboundEventGroups(ctx, now.Add(-time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]InboundEventGroup, len(groups))
	for _, group := range groups {
		byID[group.GroupID] = group
	}
	if len(byID) != 3 {
		t.Fatalf("groups = %#v", groups)
	}
	if got := byID["-1001"]; got.GroupName != "读书会" || got.BotProfileID != "tg-profile" || got.Events != 2 {
		t.Fatalf("telegram group = %#v", got)
	}
	if got := byID["111"]; got.GroupName != "" || got.BotProfileID != "qq-profile" {
		t.Fatalf("onebot group = %#v", got)
	}
	if got := byID["222"]; got.GroupName != "" || got.BotProfileID != "qq-profile" {
		t.Fatalf("group with unparsable payload = %#v", got)
	}

	// 按机器人筛选时，名字与归属仍要跟着那台机器人的事件走。
	scoped, err := store.ListInboundEventGroups(ctx, now.Add(-time.Hour), "tg-profile")
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].GroupID != "-1001" || scoped[0].GroupName != "读书会" {
		t.Fatalf("scoped groups = %#v", scoped)
	}
}

// 会话筛选器原来只列群（ListInboundEventGroups 明确排掉了空群号），私聊事件
// 在控制台里既看不到入口，也没法单独筛。
func TestListInboundEventPrivateChatsAndUserFilter(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "private-chats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now()
	insert := func(id, groupID, userID, payload string, offset time.Duration) {
		t.Helper()
		at := now.Add(offset)
		kind := "group"
		if groupID == "" {
			kind = "private"
		}
		session := "private:" + userID
		if groupID != "" {
			session = "group:" + groupID
		}
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO inbound_events (id, session, kind, profile_id, group_id, user_id, message_id, event_time, payload, priority, available_at, attempts, status, created_at, updated_at)
VALUES (?, ?, ?, 'tg-profile', ?, ?, ?, ?, ?, 0, ?, 0, ?, ?, ?)
`, id, session, kind, groupID, userID, id, at.Unix(), payload, at.Unix(), inboundStatusDone, at.Unix(), at.Unix()); err != nil {
			t.Fatal(err)
		}
	}

	insert("dm-1", "", "880024", `{"sender_name":"旧昵称","profile_id":"tg-profile"}`, -30*time.Minute)
	insert("dm-2", "", "880024", `{"sender_name":"远野","profile_id":"tg-profile"}`, -5*time.Minute)
	insert("dm-3", "", "880099", `{"sender_name":"栖迟","profile_id":"tg-profile"}`, -8*time.Minute)
	// 同一个人在群里发的消息不属于这段私聊，不能混进来。
	insert("grp-1", "100200301", "880024", `{"sender_name":"远野"}`, -6*time.Minute)

	chats, err := store.ListInboundEventPrivateChats(ctx, now.Add(-time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]InboundEventPrivateChat{}
	for _, chat := range chats {
		byID[chat.UserID] = chat
	}
	if len(byID) != 2 {
		t.Fatalf("private chats = %#v", chats)
	}
	// 昵称取最近一条，不是第一条。
	if got := byID["880024"]; got.Events != 2 || got.UserName != "远野" || got.BotProfileID != "tg-profile" {
		t.Fatalf("private chat = %#v", got)
	}
	if got := byID["880099"]; got.Events != 1 || got.UserName != "栖迟" {
		t.Fatalf("second private chat = %#v", got)
	}

	// 按人筛私聊时，同一个人的群消息必须排除。
	page, err := store.ListInboundEventDetails(ctx, InboundEventQuery{Since: now.Add(-time.Hour), Limit: 10, UserID: "880024"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("filtered events = %d, want 2", len(page.Events))
	}
	for _, event := range page.Events {
		if strings.TrimSpace(event.GroupID) != "" {
			t.Fatalf("group message leaked into the private chat filter: %#v", event)
		}
	}
}
