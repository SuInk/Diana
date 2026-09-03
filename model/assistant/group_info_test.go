// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

// Telegram 没有「列出我加入的群」，但只要知道群号就能用 getChat 问到当前名称。
// 群管理页靠它把历史事件里没有名字、或者已经改过名的群显示成正确的名字，而不是
// 等群里下一条消息。
func TestTelegramGroupInfoReadsTitleFromGetChat(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{
		"getChat": map[string]any{"id": -1001, "type": "supergroup", "title": "读书会"},
	})
	channel := api.channel()

	info, err := channel.GroupInfo(context.Background(), "-1001")
	if err != nil {
		t.Fatal(err)
	}
	if info.GroupName != "读书会" {
		t.Fatalf("GroupName = %q", info.GroupName)
	}
	if info.GroupID != "-1001" {
		t.Fatalf("GroupID = %q", info.GroupID)
	}

	calls := api.callsOf("getChat")
	if len(calls) != 1 {
		t.Fatalf("getChat calls = %d, want 1", len(calls))
	}
	if got := stringFromAny(calls[0].Params["chat_id"]); got != "-1001" {
		t.Fatalf("chat_id = %q", got)
	}
}

// 群号为空时不该白打一次接口。
func TestTelegramGroupInfoRejectsEmptyGroupID(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{})
	if _, err := api.channel().GroupInfo(context.Background(), "  "); err == nil {
		t.Fatal("expected an error for an empty group id")
	}
	if calls := api.callsOf("getChat"); len(calls) != 0 {
		t.Fatalf("unexpected getChat calls: %#v", calls)
	}
}

// 平台没给标题（机器人已退群、群号写错）时，返回的名字必须是空，
// 让调用方退回事件里记下的旧名字，而不是把群名显示成空白。
func TestTelegramGroupInfoWithoutTitleYieldsEmptyName(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{
		"getChat": map[string]any{"id": -1002, "type": "supergroup"},
	})
	info, err := api.channel().GroupInfo(context.Background(), "-1002")
	if err != nil {
		t.Fatal(err)
	}
	if info.GroupName != "" {
		t.Fatalf("GroupName = %q, want empty", info.GroupName)
	}
}
