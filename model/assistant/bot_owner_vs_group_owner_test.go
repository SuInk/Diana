// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

// 机器人的主人和群主是两个人，给模型看的数据里也不能同名。
//
// OneBot 的群成员角色用 owner 表示群主，历史上主人也叫 owner——关系等级、脱敏
// 别名前缀、工具结果里的布尔字段全是这一个词。模型读到 im_owner_xxx 或
// "owner": true，就顺理成章地把机器人的主人说成群主。现在主人一律叫 bot_owner，
// 光秃秃的 owner 只剩群主一个意思。
func TestBotOwnerLabelsNeverCollideWithGroupOwnerRole(t *testing.T) {
	// 各平台原始说法在入站时已经折成 group_*（见 NormalizeGroupRole），但主人的
	// 标签仍然不能撞上任何一边——库里、日志里、第三方样例里都还有裸值。
	groupRoles := map[string]bool{
		"owner": true, "admin": true, "member": true, "creator": true, "administrator": true,
		string(GroupRoleOwner): true, string(GroupRoleAdmin): true, string(GroupRoleMember): true,
	}

	if groupRoles[string(RelationshipOwner)] {
		t.Fatalf("关系等级 %q 和群成员角色撞名了", RelationshipOwner)
	}
	for _, role := range identityAliasRoles {
		if groupRoles[role] {
			t.Fatalf("脱敏别名角色 %q 和群成员角色撞名了", role)
		}
	}
	if got := normalizeIdentityPrivacyRole("owner"); got != "bot_owner" {
		t.Fatalf("owner_id 归一化成了 %q，应该是 bot_owner", got)
	}
	if prefix := identityAlias(normalizeIdentityPrivacyRole("owner")); prefix != "im_bot_owner_" {
		t.Fatalf("主人的别名前缀 = %q", prefix)
	}

	// 工具结果同理：模型在同一段上下文里既看得到群成员的 role，也看得到这里的
	// 布尔字段，两边不能都叫 owner。
	body, err := json.Marshal(dianaRelationshipSnapshot{UserID: "10001", Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"bot_owner":true`) {
		t.Fatalf("关系快照没有把主人标成 bot_owner: %s", body)
	}
	if strings.Contains(string(body), `"owner":`) {
		t.Fatalf("关系快照里仍有裸的 owner 字段: %s", body)
	}
}

// 区分说明只在群聊里注入：私聊没有群主，说了是白付 token。
func TestGroupPromptExplainsBotOwnerDistinction(t *testing.T) {
	base := BotConfig{}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	relationship := RelationshipPolicyFor(UserMemoryProfile{}, base.OwnerID, "1")

	group := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "1"}
	if prompt := runtime.systemPromptWithRelationshipAndAgentTools(group, nil, false, relationship, true, nil); !strings.Contains(prompt, promptGroupOwnerDistinction) {
		t.Fatalf("群聊提示词缺少主人与群主的区分说明: %q", prompt)
	}
	private := MessageEvent{Kind: EventKindPrivate, UserID: "1"}
	if prompt := runtime.systemPromptWithRelationshipAndAgentTools(private, nil, false, relationship, true, nil); strings.Contains(prompt, promptGroupOwnerDistinction) {
		t.Fatalf("私聊不该注入群主区分说明: %q", prompt)
	}
}

// 群里的身份必须是平台无关的一套词：各平台的原始说法在入站时就折过来，模型和
// 控制台永远只见到 group_* 这一套。
func TestGroupRoleVocabularyIsPlatformNeutral(t *testing.T) {
	tests := []struct {
		platform string
		raw      string
		want     GroupRole
	}{
		{platform: "OneBot v11", raw: "owner", want: GroupRoleOwner},
		{platform: "OneBot v11", raw: "admin", want: GroupRoleAdmin},
		{platform: "OneBot v11", raw: "member", want: GroupRoleMember},
		{platform: "Telegram", raw: "creator", want: GroupRoleOwner},
		{platform: "Telegram", raw: "administrator", want: GroupRoleAdmin},
		// 被禁言但人还在群里，身份上仍是普通成员。
		{platform: "Telegram", raw: "restricted", want: GroupRoleMember},
		// 已经不在群里的不是一种身份。
		{platform: "Telegram", raw: "kicked", want: ""},
		{platform: "Telegram", raw: "left", want: ""},
		{platform: "大小写和空白", raw: "  OWNER ", want: GroupRoleOwner},
		// 自己的输出要能原样读回来：老库里的事件存的是归一化之前的值。
		{platform: "已归一化", raw: "group_owner", want: GroupRoleOwner},
		{platform: "已归一化", raw: "group_member", want: GroupRoleMember},
		{platform: "未知", raw: "whatever", want: ""},
		{platform: "空", raw: "", want: ""},
	}
	for _, test := range tests {
		if got := NormalizeGroupRole(test.raw); got != test.want {
			t.Errorf("%s 的 %q 归一化成 %q，应该是 %q", test.platform, test.raw, got, test.want)
		}
	}

	// 取值里不能出现平台名，也不能和主人撞名。
	for _, role := range []GroupRole{GroupRoleOwner, GroupRoleAdmin, GroupRoleMember} {
		if !strings.HasPrefix(string(role), "group_") {
			t.Errorf("身份取值 %q 没带 group_ 前缀，和主人的 %s 分不开", role, RelationshipOwner)
		}
		for _, platform := range []string{"qq", "onebot", "telegram", "dingtalk", "feishu", "wecom"} {
			if strings.Contains(string(role), platform) {
				t.Errorf("身份取值 %q 里带了平台名 %q", role, platform)
			}
		}
	}

	if !GroupRoleCanConfigure(GroupRoleOwner) || !GroupRoleCanConfigure(GroupRoleAdmin) {
		t.Error("群主和管理员都应该能配置本群")
	}
	if GroupRoleCanConfigure(GroupRoleMember) || GroupRoleCanConfigure("") {
		t.Error("普通成员和未知身份不该能配置本群")
	}
	// 主人不是群身份：他的权限走身份判断，不该从这条路径混进来。
	if GroupRoleCanConfigure(GroupRole(RelationshipOwner)) {
		t.Errorf("%s 被当成了群身份", RelationshipOwner)
	}
}
