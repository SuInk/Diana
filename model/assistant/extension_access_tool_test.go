// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

type writableGroupStore struct {
	configs map[string]GroupConfig
}

func (s *writableGroupStore) ConfigForGroup(_, groupID string) (GroupConfig, bool) {
	cfg, ok := s.configs[groupID]
	return cfg, ok
}

func (s *writableGroupStore) SaveGroupConfig(cfg GroupConfig, _ BotConfig) (GroupConfig, error) {
	if s.configs == nil {
		s.configs = map[string]GroupConfig{}
	}
	s.configs[cfg.GroupID] = cfg
	return cfg, nil
}

func newExtensionAccessFixture(t *testing.T) (*Runtime, BotConfig, *writableGroupStore) {
	t.Helper()
	dbDir := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(dbDir, "diana.db"))
	workDir := AgentWorkspaceDir()
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(dbDir, "mcp.json")
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"notes":{"url":"https://example.com/mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultBotConfig()
	cfg.ID = "bot-a"
	cfg.OwnerID = "owner"
	cfg.AgentSkillRoots = []string{filepath.Join(dbDir, "skills")}
	cfg.AgentMCPConfigPath = mcpPath
	runtime := NewRuntime(cfg.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &writableGroupStore{configs: map[string]GroupConfig{}}
	runtime.SetGroupConfigStore(store)
	return runtime, cfg.WithDefaults(), store
}

func runAccessTool(t *testing.T, runtime *Runtime, event MessageEvent, input map[string]any) (map[string]any, error) {
	t.Helper()
	body, err := newDianaExtensionAccessTool(runtime, event).Run(context.Background(), input)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("工具输出不是 JSON: %v\n%s", err, body)
	}
	return payload, nil
}

func TestExtensionAccessToolIsOwnerOnlyAndConfirmsWrites(t *testing.T) {
	runtime, _, _ := newExtensionAccessFixture(t)
	ownerEvent := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "owner", ProfileID: "bot-a"}
	memberEvent := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "member", ProfileID: "bot-a"}

	tool := newDianaExtensionAccessTool(runtime, memberEvent)
	if _, err := tool.Run(context.Background(), map[string]any{"action": "list"}); err == nil {
		t.Fatal("非主人读到了扩展权限")
	}
	// 读不要确认码，写要。
	owner := newDianaExtensionAccessTool(runtime, ownerEvent)
	if kind := owner.ExplicitUserRequestKind(map[string]any{"action": "list"}); kind != "" {
		t.Fatalf("list 被要求确认码: %q", kind)
	}
	if kind := owner.ExplicitUserRequestKind(map[string]any{"action": "bot_tier"}); kind == "" {
		t.Fatal("改档位没有要求确认码")
	}
}

func TestExtensionAccessToolEditsBotAndGroupScopes(t *testing.T) {
	runtime, _, store := newExtensionAccessFixture(t)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "owner", ProfileID: "bot-a"}

	listed, err := runAccessTool(t, runtime, event, map[string]any{"action": "list"})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := listed["extensions"].([]any)
	if len(items) == 0 {
		t.Fatalf("没列出扩展: %#v", listed)
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != "mcp:notes" || first["bot_tier"] != agent.ExtensionTierOwner {
		t.Fatalf("默认档不对: %#v", first)
	}

	// 机器人默认档改成群管。
	if _, err := runAccessTool(t, runtime, event, map[string]any{"action": "bot_tier", "id": "mcp:notes", "tier": "admins"}); err != nil {
		t.Fatal(err)
	}
	overrides, err := agent.LoadExtensionOverrides(AgentWorkspaceDir(), "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	audiences, err := agent.LoadExtensionAudiences(AgentWorkspaceDir(), "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	if tier := agent.BotExtensionTier(overrides, audiences, "mcp:notes"); tier != agent.ExtensionTierAdmins {
		t.Fatalf("机器人默认档 = %s", tier)
	}

	// 名单要先给这个群选档位。
	if _, err := runAccessTool(t, runtime, event, map[string]any{"action": "allow", "id": "mcp:notes", "user_id": "1001"}); err == nil {
		t.Fatal("没设本群档位就能加白名单")
	}
	if _, err := runAccessTool(t, runtime, event, map[string]any{"action": "group_tier", "id": "mcp:notes", "tier": "members"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runAccessTool(t, runtime, event, map[string]any{"action": "allow", "id": "mcp:notes", "user_id": "1001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runAccessTool(t, runtime, event, map[string]any{"action": "deny", "id": "mcp:notes", "user_id": "1002"}); err != nil {
		t.Fatal(err)
	}
	saved := store.configs["g1"].ExtensionAccess["mcp:notes"]
	if saved.Tier != "members" || strings.Join(saved.Allow, ",") != "1001" || strings.Join(saved.Deny, ",") != "1002" {
		t.Fatalf("本群配置 = %#v", saved)
	}

	// 移出名单。
	if _, err := runAccessTool(t, runtime, event, map[string]any{"action": "deny", "id": "mcp:notes", "user_id": "1002", "remove": true}); err != nil {
		t.Fatal(err)
	}
	if left := store.configs["g1"].ExtensionAccess["mcp:notes"].Deny; len(left) != 0 {
		t.Fatalf("黑名单没删掉: %v", left)
	}

	// 改回跟随会把本群名单一起清掉。
	if _, err := runAccessTool(t, runtime, event, map[string]any{"action": "group_tier", "id": "mcp:notes", "tier": ""}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.configs["g1"].ExtensionAccess["mcp:notes"]; ok {
		t.Fatal("改回跟随后本群还留着配置")
	}

	// 不存在的扩展和昵称都要挡住。
	if _, err := runAccessTool(t, runtime, event, map[string]any{"action": "group_tier", "id": "mcp:nope", "tier": "members"}); err == nil {
		t.Fatal("接受了不存在的扩展")
	}
	if _, err := runAccessTool(t, runtime, event, map[string]any{"action": "bot_tier", "id": "notes", "tier": "members"}); err == nil {
		t.Fatal("接受了没有前缀的扩展 ID")
	}
}
