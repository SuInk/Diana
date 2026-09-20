// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"strings"
	"testing"
)

func TestGroupAdminTierFiltersByVerifiedRole(t *testing.T) {
	audiences := map[string]ExtensionAudience{
		"mcp:staff":        {MinRole: MemberRoleAdmin},
		"mcp:staff-listed": {MinRole: MemberRoleAdmin, Users: []string{"1001"}},
	}
	overrides := map[string]bool{
		"members:mcp:staff": true, "members:mcp:staff-listed": true, "members:mcp:open": true,
	}
	ids := MemberAllowedExtensionIDsFor(overrides, audiences, "1001", "g1")
	if strings.Join(ids, ",") != "mcp:open,mcp:staff,mcp:staff-listed" {
		t.Fatalf("名单过滤阶段就把身份门槛算进去了：%v", ids)
	}
	if !AnyRequiresGroupAdmin(audiences, ids) {
		t.Fatal("没认出需要核验群身份")
	}
	if !AnyRequiresGroupAdmin(audiences, []string{"mcp:staff"}) || AnyRequiresGroupAdmin(audiences, []string{"mcp:open"}) {
		t.Fatal("AnyRequiresGroupAdmin 判断有误")
	}
	cases := map[string]string{
		"owner":  "mcp:open,mcp:staff,mcp:staff-listed",
		"admin":  "mcp:open,mcp:staff,mcp:staff-listed",
		"member": "mcp:open",
		// 平台给不出身份时按普通成员处理，不放行。
		"": "mcp:open",
	}
	for role, want := range cases {
		if got := FilterByGroupRole(audiences, ids, role); strings.Join(got, ",") != want {
			t.Fatalf("role=%q allowed=%v want=%s", role, got, want)
		}
	}
	// 名单和身份门槛叠加：名单外的群管也进不来。
	outsider := MemberAllowedExtensionIDsFor(overrides, audiences, "2002", "g1")
	if got := FilterByGroupRole(audiences, outsider, "admin"); strings.Join(got, ",") != "mcp:open,mcp:staff" {
		t.Fatalf("名单外的群管拿到了限定项：%v", got)
	}
	if _, err := NormalizeExtensionAudience(ExtensionAudience{MinRole: "superuser"}); err == nil {
		t.Fatal("接受了不支持的身份门槛")
	}
	normalized, err := NormalizeExtensionAudience(ExtensionAudience{MinRole: " Admin "})
	if err != nil || normalized.MinRole != MemberRoleAdmin || normalized.Empty() {
		t.Fatalf("normalized=%#v err=%v", normalized, err)
	}
}
