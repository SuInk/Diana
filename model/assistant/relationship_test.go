// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

func TestRelationshipPolicyTiersRequireScoreAndInteractionCount(t *testing.T) {
	tests := []struct {
		name     string
		score    int
		messages int
		ownerID  string
		userID   string
		want     RelationshipTier
	}{
		{name: "hostile", score: -20, messages: 50, want: RelationshipHostile},
		{name: "score alone cannot unlock familiar", score: 20, messages: 9, want: RelationshipAcquaintance},
		{name: "familiar", score: 20, messages: 10, want: RelationshipFamiliar},
		{name: "friend", score: 60, messages: 30, want: RelationshipFriend},
		{name: "trusted still needs history", score: 100, messages: 79, want: RelationshipFriend},
		{name: "trusted", score: 100, messages: 80, want: RelationshipTrusted},
		{name: "owner bypasses score", score: -100, messages: 0, ownerID: "42", userID: "42", want: RelationshipOwner},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: test.score, MessageCount: test.messages}, test.ownerID, test.userID)
			if policy.Tier != test.want {
				t.Fatalf("tier = %q, want %q: %#v", policy.Tier, test.want, policy)
			}
		})
	}
}

func TestRelationshipPolicySeparatesCapabilitiesFromOwnerAdministration(t *testing.T) {
	initial := RelationshipPolicyFor(UserMemoryProfile{}, "owner", "user")
	if !initial.allowedAgentToolNames()["web_search.search"] || !initial.allowedAgentToolNames()["browser_render"] || !initial.allowedAgentToolNames()[dianaChatHistoryToolName] || !initial.allowedAgentToolNames()[dianaHistoryImagesToolName] || !initial.allowedAgentToolNames()["diana.relationship"] || !initial.allowedAgentToolNames()["diana.tts"] || !initial.allowedAgentToolNames()[dianaOneBotV11ToolName] || !initial.allowedAgentToolNames()[dianaImageToolName] || !initial.allowedAgentToolNames()["diana.reminder"] || !initial.AllowImageGeneration || !initial.AllowImageEditing || !initial.AllowDocumentOCR || !initial.AllowPersonalSchedule || initial.allowedAgentToolNames()["run_command"] {
		t.Fatalf("initial tools = %#v", initial.allowedAgentToolNames())
	}
	if initial.allowedAgentToolNames()[dianaRepositoryIssuesToolName] {
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
	if !friend.AllowImageEditing || !friend.AllowPersonalSchedule || friend.allowedAgentToolNames()["diana.config"] {
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
		{profile: UserMemoryProfile{Favorability: -20}, want: 1},
		{profile: UserMemoryProfile{}, want: 3},
		{profile: UserMemoryProfile{Favorability: 20, MessageCount: 10}, want: 10},
		{profile: UserMemoryProfile{Favorability: 60, MessageCount: 30}, want: 15},
		{profile: UserMemoryProfile{Favorability: 100, MessageCount: 80}, want: 20},
		{ownerID: "owner", userID: "owner", want: 50},
	}
	for _, test := range tests {
		policy := RelationshipPolicyFor(test.profile, test.ownerID, test.userID)
		if got := policy.personalScheduleLimit(); got != test.want {
			t.Fatalf("tier=%s limit=%d want=%d", policy.Name, got, test.want)
		}
	}
}

func TestRelationshipContextDrivesToneAndHardPermissionMessage(t *testing.T) {
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", "user")
	contextText := relationshipPermissionContext(policy)
	for _, want := range []string{"关系等级：熟悉", "语气要求", "图片生成", "好感度只影响个人提醒与订阅额度", "不能通过好感度获得"} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("context = %q, missing %q", contextText, want)
		}
	}
	denied := relationshipPermissionDenied(RelationshipPolicyFor(UserMemoryProfile{Favorability: -20}, "owner", "user"), "图片编辑", relationshipImageTierName)
	if !strings.Contains(denied, "好感度不足") || !strings.Contains(denied, "冷淡") || !strings.Contains(denied, relationshipImageTierName) {
		t.Fatalf("denied = %q", denied)
	}
}

func TestRelationshipAllowsOCRTasksForEveryTier(t *testing.T) {
	responses := []PluginResponse{{
		Handled: true,
		Tasks: []PluginTask{{
			Kind: "document_ocr",
			Name: "OCR",
			Run: func(context.Context, PluginTaskServices) (PluginTaskResult, error) {
				return PluginTaskResult{}, nil
			},
		}},
	}}
	initial := RelationshipPolicyFor(UserMemoryProfile{}, "owner", "user")
	allowedInitial := applyRelationshipTaskPermissions(responses, initial)
	if len(allowedInitial[0].Tasks) != 1 {
		t.Fatalf("initial responses = %#v", allowedInitial)
	}
	familiar := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", "user")
	allowed := applyRelationshipTaskPermissions(responses, familiar)
	if len(allowed[0].Tasks) != 1 {
		t.Fatalf("allowed responses = %#v", allowed)
	}
}

// 五个非主人等级的能力完全相同，差别只有提醒与订阅额度。之前每级各写一遍
// 相同的清单，措辞还不统一，回复里就成了一串看着像特权、其实人人都有的条目。
func TestRelationshipLevelsDifferOnlyByScheduleQuota(t *testing.T) {
	levels := []struct {
		name  string
		score int
		count int
		limit int
	}{
		{"冷淡", -50, 5, 1},
		{"初识", 0, 0, 3},
		{"熟悉", 30, 12, 10},
		{"朋友", 70, 40, 15},
		{"信赖", 120, 100, 20},
	}
	for _, level := range levels {
		policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: level.score, MessageCount: level.count}, "owner", "user")
		if policy.Name != level.name {
			t.Fatalf("score %d count %d -> %q, want %q", level.score, level.count, policy.Name, level.name)
		}
		if policy.personalScheduleLimit() != level.limit {
			t.Fatalf("%s quota = %d, want %d", level.name, policy.personalScheduleLimit(), level.limit)
		}
		// 除最后一条额度外，各等级的能力必须逐项一致。
		if got := policy.Permissions[:len(policy.Permissions)-1]; strings.Join(got, "、") != strings.Join(relationshipBaselineCapabilities, "、") {
			t.Fatalf("%s capabilities = %v, want the shared baseline", level.name, got)
		}
		if last := policy.Permissions[len(policy.Permissions)-1]; !strings.Contains(last, fmt.Sprintf("最多 %d 个", level.limit)) {
			t.Fatalf("%s quota entry = %q", level.name, last)
		}
		if !policy.AllowImageGeneration || !policy.AllowImageEditing || !policy.AllowDocumentOCR || !policy.AllowPersonalSchedule {
			t.Fatalf("%s unexpectedly restricts a baseline capability: %#v", level.name, policy)
		}
	}
}

// 系统提示不能再把人人都有的基础能力摆成「当前授权能力」清单。
func TestRelationshipPermissionContextDoesNotListBaselineAsGrants(t *testing.T) {
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 120, MessageCount: 100}, "owner", "user")
	context := relationshipPermissionContext(policy)
	if strings.Contains(context, "当前授权能力：") {
		t.Fatalf("baseline capabilities are still presented as a grant list:\n%s", context)
	}
	for _, want := range []string{"不是靠好感度解锁的", "当前提醒与订阅额度：20"} {
		if !strings.Contains(context, want) {
			t.Fatalf("context missing %q:\n%s", want, context)
		}
	}

	owner := RelationshipPolicyFor(UserMemoryProfile{}, "owner", "owner")
	if !strings.Contains(relationshipPermissionContext(owner), "机器人配置") {
		t.Fatalf("owner context lost its extra capabilities:\n%s", relationshipPermissionContext(owner))
	}
}
