// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 扩展页第一次就地把一条扩展设成常驻时，这台机器人还没有自己的名单：要先把内置推荐
// 名单固定下来再加这一条。以前这条路拿到的推荐名单是空的，点一下之后名单里只剩这一
// 条扩展，推荐的核心工具全被清了出去。
func TestAdministerExtensionsResidencyKeepsRecommendedCoreTools(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	rt := NewRuntime(BotConfig{ID: "bot-a", OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	cfg, err := agent.GlobalExtensionPaths(rt.agentRegistryConfig(rt.profileConfig("bot-a"), MessageEvent{ProfileID: "bot-a"}, true))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.MCPConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.MCPConfigPath, []byte(`{"mcpServers":{"demo":{"command":"true","enabled":false}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resident := true
	if _, err := rt.AdministerExtensions(context.Background(), agent.ExtensionAdminRequest{
		Operation: "residency", Kind: string(agent.ExtensionKindMCP), Name: "demo", ProfileID: "bot-a", Resident: &resident,
	}); err != nil {
		t.Fatalf("residency: %v", err)
	}
	values, err := agent.LoadExtensionOverrides(AgentWorkspaceDir(), "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	ids, listed := agent.ResidencyList(values)
	if !listed {
		t.Fatal("就地增删之后这台机器人应当有了自己的名单")
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	recommended := agent.RecommendedResidencyIDs(replyAgentCoreTools)
	for _, id := range recommended {
		if !got[id] {
			t.Fatalf("推荐的核心工具 %s 被清出了名单：%v", id, ids)
		}
	}
	if len(ids) != len(recommended)+1 {
		t.Fatalf("名单应当是推荐名单加上 demo，实际 %v", ids)
	}
}

// 装、改、卸之后，缓存的共享底座必须扔掉重建。早先从预设装走的是另一个操作名
// preset_save，漏在这张名单外，后果线上出现过：装好瑞幸之后模型在同一个进程里翻遍
// capabilities、list_capabilities、tools_load、extension_access 都找不到它，8 格预算
// 全花在找上，要等下次重启才生效。现在预设不再有自己的操作名，从根上少一处要同步的
// 地方，这个用例继续盯着分类别再漏。
func TestExtensionWriteChangesRegistry(t *testing.T) {
	for _, operation := range []string{"save", "delete"} {
		if !agent.ExtensionOperationChangesDefinition(operation) {
			t.Fatalf("%s 改了扩展定义，应当重建底座", operation)
		}
	}
	// 这些只改按机器人存的开关和名单，每次请求重新读，不必重建底座。
	for _, operation := range []string{"list", "read", "presets", "verify", "enabled", "members", "audience", "residency", "test"} {
		if agent.ExtensionOperationChangesDefinition(operation) {
			t.Fatalf("%s 没改扩展定义，不该把底座整个扔掉", operation)
		}
	}
}
