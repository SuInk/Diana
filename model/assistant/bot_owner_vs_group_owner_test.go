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
	// 群成员角色的取值来自 OneBot，改不了，只能让主人这边让开。
	groupRoles := map[string]bool{"owner": true, "admin": true, "member": true}

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
