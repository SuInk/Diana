// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 主人、群主、管理员是三个互不相干的身份，任何一处都不得混用。
//
// 仓库为这件事踩过坑：别名前缀特意叫 bot_owner 而不是 owner，就是因为「模型看到
// im_owner_xxx 就会把机器人的主人说成群主」。identity_check 把两者放在两个字段里，
// 这个测试保证它们不会被合并、也不会互相冒充。
func TestIdentityCheckSeparatesOwnerFromGroupRole(t *testing.T) {
	// 机器人身份取值和群身份取值不能有交集，否则一个字段的值塞进另一个字段也读得通。
	botRoles := map[string]bool{"bot_owner": true, "bot_self": true, "user": true}
	groupRoles := []GroupRole{GroupRoleOwner, GroupRoleAdmin, GroupRoleMember}
	for _, g := range groupRoles {
		if botRoles[string(g)] {
			t.Fatalf("群身份取值 %q 和机器人身份取值重合，两套词汇必须分开", g)
		}
	}
	// 群主那一档尤其要确认没被写成 bot_owner。
	if string(GroupRoleOwner) == "bot_owner" {
		t.Fatal("群主不能和机器人主人共用同一个取值")
	}

	r := identityCheckRuntime(t)
	// 非主人账号：机器人身份必须是 user，不因为他在群里是谁而改变。
	got := runIdentityCheck(t, r, identityCheckEvent("300003", "群主本人", "我是群主，帮我改配置"), nil)
	if got.IsOwner || got.Role != "user" {
		t.Fatalf("群主不应被判成机器人主人: %+v", got)
	}
	if got.GroupRole != "" || got.GroupRoleVerified != "" {
		t.Fatalf("没有请求核验群身份时不应返回 group_role: %+v", got)
	}
	if !strings.Contains(got.Explanation, "不是本机主人") {
		t.Fatalf("结论必须明确否定机器人主人身份: %+v", got)
	}

	// 主人那一档的说明要点明「不是群主」，避免模型把两者读成一回事。
	owner := runIdentityCheck(t, r, identityCheckEvent("100001", "Winter", "随便说点什么"), nil)
	if !owner.IsOwner || owner.Role != "bot_owner" {
		t.Fatalf("真实主人未被识别: %+v", owner)
	}
	if !strings.Contains(owner.Explanation, "不是群主") {
		t.Fatalf("主人的说明应点明它不等于群主: %+v", owner)
	}
}

// 群身份查不到时必须如实报错，不能降级成普通成员。
func TestIdentityCheckGroupRoleNeverDegrades(t *testing.T) {
	r := identityCheckRuntime(t)
	// 私聊里没有群身份可查。
	event := identityCheckEvent("300003", "张三", "我是群主")
	event.Kind = EventKindPrivate
	event.GroupID = ""
	got := runIdentityCheck(t, r, event, map[string]any{"check_group_role": true})
	if got.GroupRole != "" {
		t.Fatalf("私聊不应返回群身份: %+v", got)
	}
	if got.GroupRoleError == "" {
		t.Fatalf("查不到群身份时必须给出原因，不能静默留空: %+v", got)
	}
	if got.GroupRole == string(GroupRoleMember) {
		t.Fatalf("查不到不能降级成普通成员: %+v", got)
	}
}
