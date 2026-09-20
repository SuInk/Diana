// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestGroupAdminCanOnlyTightenExtensionAccess(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(dbDir, "diana.db"))
	workDir := assistant.AgentWorkspaceDir()
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// 机器人给的档位：probe 开到群管，open 开到全体成员。
	overrides := `{"bot-a":{"members:mcp:probe":true,"members:mcp:open":true}}`
	if err := os.WriteFile(filepath.Join(workDir, ".extension-overrides.json"), []byte(overrides), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".extension-audience.json"), []byte(`{"bot-a":{"mcp:probe":{"min_role":"admin"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := &BotHandler{}

	// 收紧：允许。
	for _, tier := range []string{"off", "owner", "admins"} {
		if err := handler.groupExtensionAccessWithinBotLimits("bot-a", nil, map[string]assistant.GroupExtensionAccess{"mcp:probe": {Tier: tier}}); err != nil {
			t.Fatalf("收紧到 %s 被拒: %v", tier, err)
		}
	}
	// 放宽：拒绝，并把机器人那一档说清楚。
	err := handler.groupExtensionAccessWithinBotLimits("bot-a", nil, map[string]assistant.GroupExtensionAccess{"mcp:probe": {Tier: "members"}})
	if err == nil || !strings.Contains(err.Error(), "群管") {
		t.Fatalf("群管理员放宽到全体成员没有被拦: %v", err)
	}
	// 撤掉本群的收紧同样是放宽。
	err = handler.groupExtensionAccessWithinBotLimits("bot-a", map[string]assistant.GroupExtensionAccess{"mcp:open": {Tier: "owner"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "撤掉") {
		t.Fatalf("撤掉限制没有被拦: %v", err)
	}
	// 白名单是放行：群管理员只能删不能加。
	allowList := map[string]assistant.GroupExtensionAccess{"mcp:open": {Allow: []string{"1001"}}}
	if err := handler.groupExtensionAccessWithinBotLimits("bot-a", allowList, map[string]assistant.GroupExtensionAccess{"mcp:open": {Allow: []string{"1001", "1002"}}}); err == nil {
		t.Fatal("群管理员往白名单里加人没有被拦")
	}
	if err := handler.groupExtensionAccessWithinBotLimits("bot-a", allowList, nil); err != nil {
		t.Fatalf("群管理员删掉白名单被拒: %v", err)
	}
	// 黑名单是拦截：只能加不能删。
	denyList := map[string]assistant.GroupExtensionAccess{"mcp:open": {Deny: []string{"1001"}}}
	if err := handler.groupExtensionAccessWithinBotLimits("bot-a", denyList, map[string]assistant.GroupExtensionAccess{"mcp:open": {Deny: []string{"1001", "1002"}}}); err != nil {
		t.Fatalf("群管理员往黑名单里加人被拒: %v", err)
	}
	if err := handler.groupExtensionAccessWithinBotLimits("bot-a", denyList, nil); err == nil {
		t.Fatal("群管理员撤掉黑名单没有被拦")
	}
	// 同档不算改动。
	if err := handler.groupExtensionAccessWithinBotLimits("bot-a", map[string]assistant.GroupExtensionAccess{"mcp:open": {Tier: "owner"}}, map[string]assistant.GroupExtensionAccess{"mcp:open": {Tier: "owner"}}); err != nil {
		t.Fatalf("没改动却被拒: %v", err)
	}
	// 机器人自己就是仅主人时，群里不能开到成员。
	if err := handler.groupExtensionAccessWithinBotLimits("bot-a", nil, map[string]assistant.GroupExtensionAccess{"mcp:quiet": {Tier: "members"}}); err == nil {
		t.Fatal("未配置的扩展被群管理员开给了全体成员")
	}
}

func TestNormalizeGroupExtensionAccessDropsFollowAndRejectsUnknown(t *testing.T) {
	got, err := normalizeGroupExtensionAccess(map[string]assistant.GroupExtensionAccess{
		" mcp:probe ": {Tier: "Members"},
		"skill:x":     {},
		"":            {Tier: "off"},
		"skill:y":     {Tier: "members", Deny: []string{" 1001 ", "1001", ""}},
		// 跟随档不留本群配置，名单一起丢掉。
		"skill:z": {Deny: []string{"1002"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["mcp:probe"].Tier != "members" {
		t.Fatalf("normalized = %#v", got)
	}
	if listed := got["skill:y"]; listed.Tier != "members" || strings.Join(listed.Deny, ",") != "1001" {
		t.Fatalf("normalized skill:y = %#v", listed)
	}
	if _, ok := got["skill:z"]; ok {
		t.Fatal("跟随档还留下了本群名单")
	}
	if _, err := normalizeGroupExtensionAccess(map[string]assistant.GroupExtensionAccess{"mcp:probe": {Tier: "everyone"}}); err == nil {
		t.Fatal("接受了不支持的档位")
	}
}
