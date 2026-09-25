// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func newEchoMCPHTTPServer(t *testing.T) string {
	t.Helper()
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	server := newEchoMCPServer()
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}

func writeOverrides(t *testing.T, root string, profile string, values map[string]bool) {
	t.Helper()
	for key, value := range values {
		if err := saveExtensionOverride(root, profile, key, value); err != nil {
			t.Fatal(err)
		}
	}
}

// 旧版全局停用是硬停用，那时留在文件里的机器人 true 早已作废。升级后不能让它们把
// 主人关掉的服务重新拉起来；清过一次之后，新写进来的 true 才算数。
func TestLegacyGlobalDisableDropsStaleBotOptIns(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir(), ExtensionManagement: true}
	disabled := false
	servers := map[string]mcpServerConfig{
		"off": {URL: "https://off.example.com/mcp", Enabled: &disabled},
		"on":  {URL: "https://on.example.com/mcp"},
	}
	writeMCPServers(t, cfg, servers)
	writeOverrides(t, cfg.WorkDir, "bot-a", map[string]bool{"mcp:off": true, "mcp:on": false, MemberOverrideKey("mcp:off"): true})

	if err := migrateLegacyMCPDisable(cfg.WorkDir, servers); err != nil {
		t.Fatal(err)
	}
	values, err := LoadExtensionOverrides(cfg.WorkDir, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, stale := values["mcp:off"]; stale {
		t.Fatalf("全局停用时残留的机器人 true 没清掉：%#v", values)
	}
	if enabled, ok := values["mcp:on"]; !ok || enabled {
		t.Fatalf("机器人自己的停用不该动：%#v", values)
	}
	if !values[MemberOverrideKey("mcp:off")] {
		t.Fatalf("群成员档位不归这次迁移管：%#v", values)
	}

	// 迁移只做一次：之后单独打开的机器人要留着。
	writeOverrides(t, cfg.WorkDir, "bot-a", map[string]bool{"mcp:off": true})
	if err := migrateLegacyMCPDisable(cfg.WorkDir, servers); err != nil {
		t.Fatal(err)
	}
	values, _ = LoadExtensionOverrides(cfg.WorkDir, "bot-a")
	if !values["mcp:off"] {
		t.Fatalf("迁移之后新写的机器人 true 被当成残留清掉了：%#v", values)
	}
}

// 全局关着，某台机器人单独打开：服务要起进程给它用，没打开的机器人看不到它的工具。
func TestGlobalOffMCPRunsForOptedInBotOnly(t *testing.T) {
	url := newEchoMCPHTTPServer(t)
	cfg := Config{WorkDir: t.TempDir(), ExtensionManagement: true, MCPStartupTimeoutMS: 5000}
	disabled := false
	writeMCPServers(t, cfg, map[string]mcpServerConfig{"echo": {URL: url, Enabled: &disabled}})
	// 先让迁移落标记，模拟升级之后才打开的机器人。
	if err := migrateLegacyMCPDisable(cfg.WorkDir, map[string]mcpServerConfig{}); err != nil {
		t.Fatal(err)
	}
	writeOverrides(t, cfg.WorkDir, "bot-a", map[string]bool{"mcp:echo": true})

	base, err := NewAgentToolRegistry(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if _, ok := base.Get("mcp__echo__echo"); !ok {
		t.Fatal("全局关着但有机器人单独打开，服务应当起来")
	}

	viewFor := func(profile string) *ToolRegistry {
		t.Helper()
		view, err := base.NewView(cfg)
		if err != nil {
			t.Fatal(err)
		}
		values, err := LoadExtensionOverrides(cfg.WorkDir, profile)
		if err != nil {
			t.Fatal(err)
		}
		view.ApplyExtensionOverrides(values)
		return view
	}
	if _, ok := viewFor("bot-a").Get("mcp__echo__echo"); !ok {
		t.Fatal("单独打开的机器人应当能用")
	}
	if _, ok := viewFor("bot-b").Get("mcp__echo__echo"); ok {
		t.Fatal("没打开的机器人跟随全局，应当看不到")
	}
}

// 全局开关覆盖各台机器人：拨一下全部回到跟随全局；之后单独打开的照样算数。
func TestGlobalMCPToggleOverridesBots(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir(), ExtensionManagement: true}
	writeMCPServers(t, cfg, map[string]mcpServerConfig{"echo": {URL: "https://echo.example.com/mcp"}})
	ctx := context.Background()
	list := func(profile string) ExtensionState {
		t.Helper()
		result, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "list", ProfileID: profile})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range result.(map[string]any)["items"].([]ExtensionState) {
			if item.ID == "mcp:echo" {
				return item
			}
		}
		t.Fatal("列表里没有 echo")
		return ExtensionState{}
	}
	writeOverrides(t, cfg.WorkDir, "bot-a", map[string]bool{"mcp:echo": false})
	writeOverrides(t, cfg.WorkDir, "bot-b", map[string]bool{"mcp:echo": true, MemberOverrideKey("mcp:echo"): true})

	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "enabled", Kind: "mcp", Name: "echo", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if list("").Enabled || list("bot-a").Enabled || list("bot-b").Enabled {
		t.Fatal("全局停用之后所有机器人都该跟着停用")
	}
	if values, _ := LoadExtensionOverrides(cfg.WorkDir, "bot-b"); !values[MemberOverrideKey("mcp:echo")] {
		t.Fatalf("全局开关只清启用设置，群成员档位要留着：%#v", values)
	}

	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "enabled", Kind: "mcp", Name: "echo", ProfileID: "bot-a", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	a := list("bot-a")
	if !a.Enabled || a.Available == nil || !*a.Available {
		t.Fatalf("全局关着也要能在单台机器人上打开：%#v", a)
	}
	if list("bot-b").Enabled {
		t.Fatal("别的机器人仍然跟随全局")
	}

	if _, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "enabled", Kind: "mcp", Name: "echo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if !list("bot-a").Enabled || !list("bot-b").Enabled {
		t.Fatal("全局启用之后所有机器人都该跟着启用")
	}
	if !ExtensionRequestChangesDefinition(ExtensionAdminRequest{Operation: "enabled", Kind: "mcp"}) {
		t.Fatal("MCP 开关会改哪些服务要起进程，底座必须重建")
	}
}
