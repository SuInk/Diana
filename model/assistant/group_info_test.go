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

// 群头像要经过 getChat 拿 file_id、再 getFile 下载；返回的是字节而不是地址，
// 因为 Telegram 的下载地址里带着 Bot Token。
func TestTelegramGroupAvatarDownloadsPhoto(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	api := newFakeTelegramAPI(t, map[string]any{
		"getChat": map[string]any{
			"id":    -1001,
			"title": "读书会",
			"photo": map[string]any{"small_file_id": "small-1", "big_file_id": "big-1"},
		},
		"getFile":                   map[string]any{"file_path": "photos/small.png"},
		"download:photos/small.png": png,
	})

	avatar, err := api.channel().GroupAvatar(context.Background(), "-1001")
	if err != nil {
		t.Fatal(err)
	}
	if len(avatar.Data) != len(png) {
		t.Fatalf("avatar bytes = %d, want %d", len(avatar.Data), len(png))
	}
	if avatar.ContentType != "image/png" {
		t.Fatalf("content type = %q", avatar.ContentType)
	}
	// 列表里显示的是小图，应该优先取 small_file_id。
	calls := api.callsOf("getFile")
	if len(calls) != 1 || stringFromAny(calls[0].Params["file_id"]) != "small-1" {
		t.Fatalf("getFile calls = %#v", calls)
	}
}

// 群没设头像时要如实报错，让调用方退回占位显示，而不是返回一张空图。
func TestTelegramGroupAvatarWithoutPhoto(t *testing.T) {
	api := newFakeTelegramAPI(t, map[string]any{
		"getChat": map[string]any{"id": -1002, "title": "无头像群"},
	})
	if _, err := api.channel().GroupAvatar(context.Background(), "-1002"); err == nil {
		t.Fatal("expected an error when the chat has no photo")
	}
}
