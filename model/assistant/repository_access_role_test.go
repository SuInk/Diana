// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// repositoryAccessRoleTestTool 复用 Issue 工具的假 GitHub 夹具，只换发言人：
// 同一条按群授权，群主、群管理员和普通成员应当拿到不同的结果。
func repositoryAccessRoleTestTool(server *httptest.Server, userID, senderRole string, settings SettingValues) *dianaRepositoryIssuesTool {
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	return newDianaRepositoryIssuesTool(
		runtime,
		MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: userID, SenderRole: senderRole, RawMessage: "帮我提个 Issue"},
		newRepositoryPublishPlugin(server.Client(), server.URL),
		settings,
	)
}

func repositoryAccessRoleSettings(managerGroups, draftGroups string) SettingValues {
	return SettingValues{
		repositoryPublishSettingToken:         repositoryPublishTestToken,
		repositoryPublishSettingAllowlist:     "acme/demo",
		repositoryPublishSettingManagerGroups: managerGroups,
		repositoryPublishSettingDraftGroups:   draftGroups,
		repositoryPublishSettingTimeout:       5,
	}
}

func TestRepositoryAccessRoleSuffixParsing(t *testing.T) {
	access, roles, err := repositoryPublishGroupAccessRules("group-1 = acme/demo#group_admin, acme/other\ngroup-2 = acme/demo#group_owner")
	if err != nil {
		t.Fatal(err)
	}
	if !access["group-1"]["acme/demo"] || !access["group-1"]["acme/other"] || !access["group-2"]["acme/demo"] {
		t.Fatalf("access=%#v", access)
	}
	if got := roles.requirement("group-1", "acme/demo"); got != repositoryAccessRoleAdmins {
		t.Fatalf("suffix requirement=%q", got)
	}
	// 不写后缀就是老行为：群里所有人都算数。
	if got := roles.requirement("group-1", "acme/other"); got != repositoryAccessRoleAllMembers {
		t.Fatalf("bare entry requirement=%q", got)
	}
	if got := roles.requirement("group-2", "acme/demo"); got != repositoryAccessRoleGroupOwner {
		t.Fatalf("owner requirement=%q", got)
	}

	// 用户授权没有群身份可言，填了后缀就是填错了地方，必须判错而不是悄悄忽略。
	if _, _, err := repositoryPublishScopedAccessRules("someone = acme/demo#group_admin", "user"); err == nil {
		t.Fatal("user access accepted a group role suffix")
	}
	if _, _, err := repositoryPublishGroupAccessRules("group-1 = acme/demo#president"); err == nil {
		t.Fatal("unknown role requirement accepted")
	}
}

// 「Issue 管理人员（按群）」限定到群管理员之后，普通群成员的调用要被明确拒绝，
// 而且提示里得说清是群身份不够，不是仓库没配。
func TestRepositoryAccessRoleGroupAdminRequirement(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	settings := repositoryAccessRoleSettings("group-1 = acme/demo#group_admin", "")

	denied := runRepositoryPublishToolOnce(t, repositoryAccessRoleTestTool(server, "member", "member", settings), map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "登录失败", "body": "重置密码后无法登录。",
	})
	if denied.OK || denied.FailureCode != "permission_denied" {
		t.Fatalf("ordinary member was not refused: %#v", denied)
	}
	if github.count(http.MethodPost) != 0 {
		t.Fatal("refused call still wrote to GitHub")
	}
	for _, want := range []string{"群主或群管理员", "群成员", "Issue 管理人员"} {
		if !contains(denied.Message, want) {
			t.Fatalf("denial message %q does not mention %q", denied.Message, want)
		}
	}

	admin := runRepositoryPublishToolOnce(t, repositoryAccessRoleTestTool(server, "boss", "admin", settings), map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "登录失败", "body": "重置密码后无法登录。",
	})
	if !admin.OK || admin.Outcome != "draft_pending" {
		t.Fatalf("group admin was refused: %#v", admin)
	}
}

func TestRepositoryAccessRoleGroupOwnerRequirement(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	settings := repositoryAccessRoleSettings("group-1 = acme/demo#group_owner", "")

	admin := runRepositoryPublishToolOnce(t, repositoryAccessRoleTestTool(server, "boss", "admin", settings), map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "标题", "body": "正文",
	})
	if admin.OK || admin.FailureCode != "permission_denied" {
		t.Fatalf("group admin passed an owner-only rule: %#v", admin)
	}
	creator := runRepositoryPublishToolOnce(t, repositoryAccessRoleTestTool(server, "founder", "owner", settings), map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "标题", "body": "正文",
	})
	if !creator.OK {
		t.Fatalf("group owner was refused: %#v", creator)
	}
}

// 平台没给群身份时按最严处理：不能因为查不到就当普通成员放行，也不能沉默地失败。
func TestRepositoryAccessRoleUnknownRoleIsRefusedWithReason(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	result := runRepositoryPublishToolOnce(t, repositoryAccessRoleTestTool(server, "member", "", repositoryAccessRoleSettings("group-1 = acme/demo#group_admin", "")), map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "标题", "body": "正文",
	})
	if result.OK || result.FailureCode != "permission_denied" || !contains(result.Message, "没有提供你的群身份") {
		t.Fatalf("unknown role result=%#v", result)
	}
}

// 管理人员自动也是草稿人。管理侧收窄到群管理员时，不能把本来放给全体成员的
// 草稿权一起收走：两条授权重合的组合按更宽的那条算。
func TestRepositoryAccessRoleManagerRestrictionKeepsLooserDraftGrant(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	settings := repositoryAccessRoleSettings("group-1 = acme/demo#group_admin", "group-1 = acme/demo")

	result := runRepositoryPublishToolOnce(t, repositoryAccessRoleTestTool(server, "member", "member", settings), map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "标题", "body": "正文",
	})
	if !result.OK || !result.RequiresApproval || result.Outcome != "draft_pending" {
		t.Fatalf("ordinary member lost the draft grant: %#v", result)
	}
	if github.count(http.MethodPost) != 0 {
		t.Fatal("draft went straight to GitHub")
	}
}

// 主人不受群身份限制：那份名单本来就不管主人。
func TestRepositoryAccessRoleOwnerBypassesGroupRole(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	result := runRepositoryPublishToolOnce(t, repositoryAccessRoleTestTool(server, "owner", "member", repositoryAccessRoleSettings("group-1 = acme/demo#group_owner", "")), map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "标题", "body": "正文",
	})
	if !result.OK {
		t.Fatalf("bot owner was blocked by a group role requirement: %#v", result)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}
