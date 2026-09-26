// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func governanceToolEvent(platform string) MessageEvent {
	return MessageEvent{Kind: EventKindGroup, UserID: "owner", GroupID: "123", SelfID: "10000", Platform: platform}
}

func governanceToolFor(t *testing.T, platform, selfRole string) (*dianaPlatformTool, *Runtime, *moderationTestChannel) {
	t.Helper()
	channel := newModerationTestChannel(selfRole)
	tool, runtime, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: platform}, channel, governanceToolEvent(platform))
	return tool, runtime, channel
}

func TestPlatformToolGovernanceMapsToOneBotActions(t *testing.T) {
	tool, _, channel := governanceToolFor(t, PlatformOneBotV11, "owner")
	tool.event.Quoted = &QuotedMessage{MessageID: "m-9", UserID: "555"}
	steps := []map[string]any{
		{"operation": "announce", "content": "今晚八点开会"},
		{"operation": "announce_delete", "notice_id": "n-1"},
		{"operation": "essence_set"},
		{"operation": "essence_unset", "message_id": "m-8"},
		{"operation": "set_card", "user_id": "555", "card": "新名片"},
		{"operation": "set_title", "user_id": "555", "title": "头衔"},
		{"operation": "mute_all"},
		{"operation": "unmute_all"},
	}
	for _, input := range steps {
		if _, err := tool.Run(context.Background(), input); err != nil {
			t.Fatalf("%v error = %v", input["operation"], err)
		}
	}
	calls := channel.callsSnapshot()
	want := map[string]func(map[string]any) bool{
		"_send_group_notice": func(p map[string]any) bool { return p["content"] == "今晚八点开会" },
		"_del_group_notice":  func(p map[string]any) bool { return p["notice_id"] == "n-1" },
		"set_essence_msg":    func(p map[string]any) bool { return fmt.Sprint(p["message_id"]) == "m-9" },
		"delete_essence_msg": func(p map[string]any) bool { return fmt.Sprint(p["message_id"]) == "m-8" },
		"set_group_card":     func(p map[string]any) bool { return p["card"] == "新名片" },
		"set_group_special_title": func(p map[string]any) bool {
			return p["special_title"] == "头衔" && p["duration"] == -1
		},
	}
	for action, check := range want {
		got := recordedCallsByAction(calls, action)
		if len(got) != 1 || !check(got[0].params) {
			t.Fatalf("%s calls = %#v", action, got)
		}
	}
	whole := recordedCallsByAction(calls, "set_group_whole_ban")
	if len(whole) != 2 || whole[0].params["enable"] != true || whole[1].params["enable"] != false {
		t.Fatalf("set_group_whole_ban calls = %#v", whole)
	}
}

// QQ 的专属头衔只有群主能发，管理员身份要在门口拒掉，不去碰接口。
func TestPlatformToolSetTitleNeedsGroupOwnerOnQQ(t *testing.T) {
	tool, _, channel := governanceToolFor(t, PlatformOneBotV11, "admin")
	_, err := tool.Run(context.Background(), map[string]any{"operation": "set_title", "user_id": "555", "title": "x"})
	if err == nil || !strings.Contains(err.Error(), "只有群主") {
		t.Fatalf("set_title as admin error = %v", err)
	}
	if got := recordedCallsByAction(channel.callsSnapshot(), "set_group_special_title"); len(got) != 0 {
		t.Fatalf("must not call set_group_special_title: %#v", got)
	}
}

func TestPlatformToolGovernanceOnTelegram(t *testing.T) {
	tool, _, channel := governanceToolFor(t, PlatformTelegram, "administrator")
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "announce", "content": "x"}); err == nil || !strings.Contains(err.Error(), "暂不支持") {
		t.Fatalf("telegram announce error = %v", err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "set_card", "user_id": "555", "card": "x"}); err == nil || !strings.Contains(err.Error(), "暂不支持") {
		t.Fatalf("telegram set_card error = %v", err)
	}
	for _, input := range []map[string]any{
		{"operation": "essence_set", "message_id": "77"},
		{"operation": "mute_all"},
		{"operation": "unmute_all"},
	} {
		if _, err := tool.Run(context.Background(), input); err != nil {
			t.Fatalf("%v error = %v", input["operation"], err)
		}
	}
	calls := channel.callsSnapshot()
	if pins := recordedCallsByAction(calls, "pinChatMessage"); len(pins) != 1 {
		t.Fatalf("pinChatMessage calls = %#v", pins)
	}
	perms := recordedCallsByAction(calls, "setChatPermissions")
	if len(perms) != 2 {
		t.Fatalf("setChatPermissions calls = %#v", perms)
	}
	muted, _ := perms[0].params["permissions"].(map[string]any)
	restored, _ := perms[1].params["permissions"].(map[string]any)
	if muted["can_send_messages"] != false || restored["can_send_messages"] != true {
		t.Fatalf("permissions = %#v / %#v", muted, restored)
	}
	// 解除全员禁言恢复的是群默认权限，不能顺手把改群资料的权力发给所有人。
	if restored["can_change_info"] != false || restored["can_pin_messages"] != false {
		t.Fatalf("unmute_all granted admin-like permissions: %#v", restored)
	}
}

func TestPlatformToolGovernanceRefusesNonOwnerAndNonAdminBot(t *testing.T) {
	// 普通成员：平台实时查到的是 member，哪怕事件里自称 admin 也不作数。
	channel := newRoleTestChannel(map[string]string{"10000": "admin"})
	event := governanceToolEvent(PlatformOneBotV11)
	event.UserID, event.SenderRole = "member", "admin"
	tool, _, _ := platformToolFor(t, BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "announce", "content": "x"}); err == nil || !strings.Contains(err.Error(), "主人、群主或群管理员") {
		t.Fatalf("non-owner announce error = %v", err)
	}

	plain, _, plainChannel := governanceToolFor(t, PlatformOneBotV11, "member")
	if _, err := plain.Run(context.Background(), map[string]any{"operation": "mute_all"}); err == nil || !strings.Contains(err.Error(), "不是这个群的管理员") {
		t.Fatalf("bot-not-admin error = %v", err)
	}
	if len(recordedCallsByAction(channel.callsSnapshot(), "_send_group_notice")) != 0 || len(recordedCallsByAction(plainChannel.callsSnapshot(), "set_group_whole_ban")) != 0 {
		t.Fatal("refused operations must not reach the platform API")
	}
}

func TestPlatformToolRecallMessagesByUser(t *testing.T) {
	tool, runtime, channel := governanceToolFor(t, PlatformOneBotV11, "admin")
	for i := 1; i <= 4; i++ {
		runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11, UserID: "555", MessageID: fmt.Sprintf("spam-%d", i), RawMessage: "刷屏"})
	}
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11, UserID: "666", MessageID: "normal-1", RawMessage: "正常发言"})

	out, err := tool.Run(context.Background(), map[string]any{"operation": "recall_messages", "user_id": "555", "count": 3})
	if err != nil {
		t.Fatalf("recall_messages error = %v", err)
	}
	deleted := recordedCallsByAction(channel.callsSnapshot(), "delete_msg")
	if len(deleted) != 3 {
		t.Fatalf("delete_msg calls = %#v", deleted)
	}
	for i, id := range []string{"spam-4", "spam-3", "spam-2"} {
		if got := fmt.Sprint(deleted[i].params["message_id"]); got != id {
			t.Fatalf("delete #%d = %s, want %s", i, got, id)
		}
	}
	if !strings.Contains(out, "已撤回 3 条") {
		t.Fatalf("result = %s", out)
	}

	// 历史里没有的 ID 不认：模型编一个数字就可能删掉别人的正常发言。
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "recall_messages", "message_id": "made-up"}); err == nil || !strings.Contains(err.Error(), "找不到") {
		t.Fatalf("unknown message_id error = %v", err)
	}
}

// 能力矩阵：每个平台的每个写操作都要表态，新增平台或操作时这里先红。
var expectedPlatformWriteOperations = map[string][]string{
	PlatformOneBotV11: {
		platformOpRecall, platformOpMute, platformOpUnmute, platformOpKick,
		platformOpAnnounce, platformOpAnnounceList, platformOpAnnounceDelete,
		platformOpEssenceSet, platformOpEssenceUnset, platformOpSetCard, platformOpSetTitle,
		platformOpMuteAll, platformOpUnmuteAll, platformOpRecallMessages,
	},
	// 没有群公告、没有群名片；精华对应置顶，头衔对应管理员自定义头衔。
	PlatformTelegram: {
		platformOpRecall, platformOpMute, platformOpUnmute, platformOpKick,
		platformOpEssenceSet, platformOpEssenceUnset, platformOpSetTitle,
		platformOpMuteAll, platformOpUnmuteAll, platformOpRecallMessages,
	},
	// 以下平台只接了只读适配层，群管写操作一律如实回「不支持」。
	PlatformQQOfficial: nil,
	PlatformFeishu:     nil,
	PlatformDingTalk:   nil,
	PlatformWeCom:      nil,
}

func TestPlatformWriteOperationMatrix(t *testing.T) {
	all := append([]string{platformOpRecall, platformOpAnnounceList}, platformModerationOperations...)
	for _, def := range SupportedPlatforms() {
		declared, ok := expectedPlatformWriteOperations[def.ID]
		if !ok {
			t.Errorf("平台 %q 没有在群管能力表里表态", def.ID)
			continue
		}
		want := map[string]bool{}
		for _, op := range declared {
			want[op] = true
		}
		for _, op := range all {
			if got := platformSupportsOperation(def.ID, op); got != want[op] {
				t.Errorf("%s %s：实现 = %v，能力表 = %v", def.ID, op, got, want[op])
			}
		}
	}
}
