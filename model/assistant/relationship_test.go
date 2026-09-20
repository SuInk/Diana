// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 关系等级已删除，语气改由好感度连续驱动。这个测试守住两件事：
// 主人身份不随分数变化，以及负分确实会收敛语气（惩罚落在「愿不愿意主动搭理」上）。
func TestFavorabilityStanceReplacesTiers(t *testing.T) {
	owner := RelationshipPolicyFor(UserMemoryProfile{Favorability: -100}, "42", "42")
	if !owner.Owner {
		t.Fatalf("主人身份由账号决定，不该被分数影响: %#v", owner)
	}
	if strings.Contains(owner.Tone, "疏离") || strings.Contains(owner.Tone, "只回答") {
		t.Fatalf("主人不该被负分收敛语气: %q", owner.Tone)
	}

	for _, tc := range []struct {
		name    string
		score   int
		wantHas string
	}{
		{"极低分只答必要内容", -60, "只回答被直接问到的必要内容"},
		{"负分不主动接话", -1, "不主动接话题"},
		{"新人默认分随和", 10, "刚认识"},
		{"分高更放松", 60, "朋友"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: tc.score}, "owner", "user")
			if !strings.Contains(policy.Tone, tc.wantHas) {
				t.Fatalf("好感度 %d 的语气指引里没有 %q: %q", tc.score, tc.wantHas, policy.Tone)
			}
		})
	}
}

// 负分的惩罚只作用于主动性，绝不关闭任何能力：否则任何人都能靠激怒机器人
// 把自己的功能弄坏。
func TestNegativeFavorabilityNeverDisablesCapabilities(t *testing.T) {
	for _, score := range []int{-100, -50, -1, 0, 10, 200} {
		policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: score}, "owner", "user")
		if !policy.AllowImageGeneration || !policy.AllowImageEditing ||
			!policy.AllowDocumentOCR || !policy.AllowPersonalSchedule {
			t.Fatalf("好感度 %d 关掉了能力: %#v", score, policy)
		}
		if !policy.allowedAgentToolNames()[agent.WebSearchToolName] {
			t.Fatalf("好感度 %d 丢了联网搜索", score)
		}
		if policy.personalScheduleLimit() <= 0 {
			t.Fatalf("好感度 %d 的提醒额度归零了，等于禁用功能", score)
		}
	}
}

func TestRelationshipPolicySeparatesCapabilitiesFromOwnerAdministration(t *testing.T) {
	initial := RelationshipPolicyFor(UserMemoryProfile{}, "owner", "user")
	if !initial.allowedAgentToolNames()["web_search"] || !initial.allowedAgentToolNames()["browser_render"] || !initial.allowedAgentToolNames()[dianaChatHistoryToolName] || !initial.allowedAgentToolNames()[dianaHistoryImagesToolName] || !initial.allowedAgentToolNames()["relationship"] || !initial.allowedAgentToolNames()["tts"] || !initial.allowedAgentToolNames()[dianaPlatformToolName] || !initial.allowedAgentToolNames()[dianaImageToolName] || !initial.allowedAgentToolNames()["reminder"] || !initial.AllowImageGeneration || !initial.AllowImageEditing || !initial.AllowDocumentOCR || !initial.AllowPersonalSchedule || initial.allowedAgentToolNames()["run_command"] {
		t.Fatalf("initial tools = %#v", initial.allowedAgentToolNames())
	}
	if initial.allowedAgentToolNames()[dianaGitHubToolName] {
		t.Fatal("non-owner relationship unexpectedly received GitHub Issue write access")
	}
	familiar := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", "user")
	if !familiar.AllowImageGeneration || !familiar.AllowImageEditing || !familiar.AllowDocumentOCR {
		t.Fatalf("familiar policy = %#v", familiar)
	}
	hostile := RelationshipPolicyFor(UserMemoryProfile{Favorability: -20, MessageCount: 10}, "owner", "user")
	if !hostile.AllowImageGeneration || !hostile.AllowImageEditing || !hostile.AllowDocumentOCR || !hostile.allowedAgentToolNames()["browser_render"] {
		t.Fatalf("hostile policy = %#v", hostile)
	}
	if !hostile.allowedAgentToolNames()[agent.WebSearchToolName] {
		t.Fatalf("hostile relationship lost mandatory web search tool: %#v", hostile.allowedAgentToolNames())
	}
	friend := RelationshipPolicyFor(UserMemoryProfile{Favorability: 60, MessageCount: 30}, "owner", "user")
	if !friend.AllowImageEditing || !friend.AllowPersonalSchedule || friend.allowedAgentToolNames()["config"] {
		t.Fatalf("friend policy = %#v tools=%#v", friend, friend.allowedAgentToolNames())
	}
	owner := RelationshipPolicyFor(UserMemoryProfile{}, "owner", "owner")
	if !owner.Owner || owner.allowedAgentToolNames() != nil {
		t.Fatalf("owner policy = %#v", owner)
	}
}

func TestRelationshipMediaToolsArePublic(t *testing.T) {
	tests := []struct {
		name    string
		profile UserMemoryProfile
		ownerID string
		userID  string
	}{
		{name: "score below threshold", profile: UserMemoryProfile{Favorability: 19, MessageCount: 10}, userID: "user"},
		{name: "messages below threshold", profile: UserMemoryProfile{Favorability: 20, MessageCount: 9}, userID: "user"},
		{name: "hostile can generate", profile: UserMemoryProfile{Favorability: -20}, userID: "user"},
		{name: "familiar threshold", profile: UserMemoryProfile{Favorability: 20, MessageCount: 10}, userID: "user"},
		{name: "owner bypass", ownerID: "owner", userID: "owner"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := RelationshipPolicyFor(test.profile, test.ownerID, test.userID)
			if !policy.AllowImageGeneration || !policy.AllowImageEditing || !policy.AllowDocumentOCR {
				t.Fatalf("media permissions = generate:%v edit:%v ocr:%v, want all true: %#v", policy.AllowImageGeneration, policy.AllowImageEditing, policy.AllowDocumentOCR, policy)
			}
		})
	}
}

func TestRelationshipScheduleLimitsIncreaseByTier(t *testing.T) {
	tests := []struct {
		profile UserMemoryProfile
		ownerID string
		userID  string
		want    int
	}{
		// 额度不再分级，只有三段：主人 50，关系为负 1，其余一律 10。
		{profile: UserMemoryProfile{Favorability: -1}, want: 1},
		{profile: UserMemoryProfile{Favorability: 10}, want: 10},
		{profile: UserMemoryProfile{Favorability: 200, MessageCount: 500}, want: 10},
		{ownerID: "owner", userID: "owner", want: 50},
	}
	for _, test := range tests {
		policy := RelationshipPolicyFor(test.profile, test.ownerID, test.userID)
		if got := policy.personalScheduleLimit(); got != test.want {
			t.Fatalf("score=%d limit=%d want=%d", policy.Score, got, test.want)
		}
	}
}

func TestRelationshipContextDrivesToneAndHardPermissionMessage(t *testing.T) {
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", "user")
	// 随发言者变的只有等级名和语气；固定的能力与权限规则在稳定的系统头部。
	contextText := relationshipPermissionContext(policy)
	for _, want := range []string{"当前好感度：20", "语气要求"} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("tier context = %q, missing %q", contextText, want)
		}
	}
	for _, moved := range []string{"图片生成", "不得以好感度不足为由拒绝任何普通能力", "不能通过好感度获得"} {
		if strings.Contains(contextText, moved) {
			t.Fatalf("invariant rule %q must live in the cached head, not the per-speaker tail: %q", moved, contextText)
		}
		if !strings.Contains(promptRelationshipTierRules, moved) {
			t.Fatalf("promptRelationshipTierRules missing %q", moved)
		}
	}
}

// 五个非主人等级的能力完全相同，真正随好感度变化的只有提醒与订阅额度。

// 提示词不能再把人人都有的基础能力摆成「授权能力」清单——那正是回复里冒出
// 一长串「当前权限：…」的来源。
func TestRelationshipContextDoesNotListBaselineAsGrants(t *testing.T) {
	profile := UserMemoryProfile{UserID: "10005", DisplayName: "小林", Favorability: 101, MessageCount: 1128}
	policy := RelationshipPolicyFor(profile, "owner", "user")

	// 模型看到的是「稳定头部的固定规则 + 尾部的本人等级」两段合起来。
	permissionContext := promptRelationshipTierRules + "\n" + relationshipPermissionContext(policy)
	if strings.Contains(permissionContext, "当前授权能力：") {
		t.Fatalf("baseline capabilities are still presented as a grant list:\n%s", permissionContext)
	}
	if !strings.Contains(permissionContext, "不是靠好感度解锁的") {
		t.Fatalf("context missing the shared-capability note:\n%s", permissionContext)
	}
	// 额度也不再提前预告：撞上限时创建工具会当场说明还能建几个。
	for _, unwanted := range []string{"当前提醒与订阅额度", "冷淡 1 个", "最多 20 个"} {
		if strings.Contains(permissionContext, unwanted) {
			t.Fatalf("context still advertises the quota (%q):\n%s", unwanted, permissionContext)
		}
	}
	if !strings.Contains(relationshipPermissionContext(RelationshipPolicyFor(UserMemoryProfile{}, "owner", "owner")), "机器人配置") {
		t.Fatal("owner context lost its extra capabilities")
	}

	// 长期记忆上下文是第二个曾经复述能力清单的地方。
	memoryContext := formatUserMemoryContext(profile, policy)
	if strings.Contains(memoryContext, "已授权能力") {
		t.Fatalf("memory context still lists capabilities:\n%s", memoryContext)
	}
	if strings.Contains(memoryContext, "好感度：") || strings.Contains(memoryContext, "互动次数：") {
		t.Fatalf("记忆段不该再带每轮变化的计数:\n%s", memoryContext)
	}
	// 语气要求只在系统尾部出现一次，记忆块里不再重复同一段话。
	if strings.Contains(memoryContext, "语气要求：") || strings.Contains(memoryContext, policy.Tone) {
		t.Fatalf("memory context duplicates the tone already carried by the system tail:\n%s", memoryContext)
	}
}
