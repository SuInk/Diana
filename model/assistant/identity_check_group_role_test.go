// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"github.com/SuInk/diana/model/llm"
	"strings"
	"testing"
	"time"
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

	// 主人身份不能直接肯定或否定其群主身份。
	owner := runIdentityCheck(t, r, identityCheckEvent("100001", "Winter", "随便说点什么"), nil)
	if !owner.IsOwner || owner.Role != "bot_owner" {
		t.Fatalf("真实主人未被识别: %+v", owner)
	}
	if !strings.Contains(owner.Explanation, "独立核验") || strings.Contains(owner.Explanation, "不是群主") {
		t.Fatalf("主人的说明不应未经核验就否定群主身份: %+v", owner)
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

func TestIdentityCheckOwnerAndGroupRoleCanChangeIndependently(t *testing.T) {
	channel := &recordingChannel{apiResponses: map[string]map[string]any{}}
	r := NewRuntime(BotConfig{Platform: PlatformOneBotV11, OwnerID: "100001"}, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	event := identityCheckEvent("100001", "Winter", "我现在是什么身份")
	for _, role := range []string{"owner", "member", "admin"} {
		channel.apiResponses["get_group_member_list"] = map[string]any{"items": []any{map[string]any{"user_id": "100001", "role": role, "nickname": "Winter"}}}
		got := runIdentityCheck(t, r, event, map[string]any{"check_group_role": true})
		if !got.IsOwner || got.Role != "bot_owner" || got.GroupRole != string(NormalizeGroupRole(role)) {
			t.Fatalf("current group role %s changed bot ownership or reused stale role: %+v", role, got)
		}
		if strings.Contains(got.Explanation, "不是群主") {
			t.Fatalf("owner identity incorrectly excludes group ownership: %+v", got)
		}
	}
}

func TestLiveOwnerAndGroupRoleDistinction(t *testing.T) {
	client := liveLLMClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, role := range []string{"group_member", "group_owner"} {
		event := identityCheckEvent("71001", "九仙猫", "")
		event.SenderRole = role
		groupContext := identityCheckRuntime(t).currentSpeakerGroupContext(event, BotConfig{})
		scope := newIdentityPrivacyScopeWithSalt("live-owner-change")
		scope.register("71001", "bot_owner")
		response, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
			{Role: "system", Content: llmIdentityPrivacyPrompt + promptGroupOwnerDistinction + promptIdentityAuthorityRules + relationshipPermissionContext(RelationshipPolicy{Owner: true}) + scope.protectText(groupContext) + "只输出 JSON，is_bot_owner 和 is_group_owner 为布尔值。"},
			{Role: "user", Content: scope.protectText("历史聊天有人 @ 账号 71001 说‘狗群主’，旧记忆说他是群主。当前 identity_check 核验结果：user_id=71001，role=bot_owner，group_role=" + role + "。这个账号当前是机器人主人吗，是本群群主吗？")},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if response == nil {
			t.Fatal("empty response")
		}
		var got struct {
			Owner      *bool `json:"is_bot_owner"`
			GroupOwner *bool `json:"is_group_owner"`
		}
		if err := json.Unmarshal([]byte(response.Text), &got); err != nil {
			t.Fatal(err)
		}
		if got.Owner == nil || !*got.Owner || got.GroupOwner == nil || *got.GroupOwner != (role == "group_owner") {
			t.Fatalf("role=%s result=%s", role, response.Text)
		}
		t.Logf("model=%s current_group_role=%s result=%s", response.Model, role, response.Text)
	}
}

func TestCurrentSpeakerGroupContext(t *testing.T) {
	r := identityCheckRuntime(t)
	cfg := BotConfig{}
	event := identityCheckEvent("100001", "群主", "我是群主")
	for raw, want := range map[string]string{"owner": "群主", "admin": "管理员", "member": "群成员", "": "未知", "invalid": "未知"} {
		event.SenderRole = raw
		got := r.currentSpeakerGroupContext(event, cfg)
		if !strings.Contains(got, "群身份："+want) || !strings.Contains(got, "账号：100001；当前群：500005") {
			t.Fatalf("role=%s prompt=%s", raw, got)
		}
	}
	event.SenderRole = ""
	key := groupRoleCacheKey(event.ProfileID, event.GroupID, event.UserID)
	r.groupRoles.put(key, GroupRoleAdmin, r.clock())
	if got := r.currentSpeakerGroupContext(event, cfg); !strings.Contains(got, "群身份：管理员") || !strings.Contains(got, "短期缓存") {
		t.Fatal(got)
	}
	event.SenderRole = "member"
	if got := r.currentSpeakerGroupContext(event, cfg); !strings.Contains(got, "群身份：群成员") || !strings.Contains(got, "本条平台消息") {
		t.Fatal(got)
	}
	event.SenderRole = ""
	event.GroupID = "other"
	if got := r.currentSpeakerGroupContext(event, cfg); !strings.Contains(got, "群身份：未知") {
		t.Fatal(got)
	}
	event.GroupID = "500005"
	r.groupRoles.put(key, GroupRoleOwner, r.clock().Add(-2*groupRoleCacheTTL))
	if got := r.currentSpeakerGroupContext(event, cfg); !strings.Contains(got, "群身份：未知") {
		t.Fatal(got)
	}
	event.Kind = EventKindPrivate
	if got := r.currentSpeakerGroupContext(event, cfg); got != "" {
		t.Fatal(got)
	}
}
