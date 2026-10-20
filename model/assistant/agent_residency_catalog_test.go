// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 工具目录是每轮对话现攒的，进程重启后为空。配过的档位仍然写在文件里、仍然生效，
// 界面这时候不能显示成「没配过」——用户会以为配置丢了，于是再配一遍。
func TestAgentResidencyKeepsConfiguredEntriesWithoutCatalog(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(root, "diana.db"))
	workspace := AgentWorkspaceDir()
	if err := agent.SaveResidencyList(workspace, "bot-a", []string{agent.ToolResidentID("poke"), "official.music"}); err != nil {
		t.Fatal(err)
	}
	// 旧版写下的 Skill 常驻值不能复活；已有工具和插件的名单仍须保留。
	overridesPath := filepath.Join(agent.DianaStateDir(workspace), "extension-overrides.json")
	data, err := os.ReadFile(overridesPath)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]map[string]bool
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["bot-a"][agent.ResidentOverrideKey("skill:demo")] = true
	data, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overridesPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	resident := true
	if err := agent.SaveExtensionResidency(workspace, "bot-a", "skill:demo", &resident, nil); err == nil {
		t.Fatal("Skill 常驻入口应拒绝旧配置")
	}
	values, err := agent.LoadExtensionOverrides(workspace, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := values[agent.ResidentOverrideKey("skill:demo")]; exists || agent.ResidentOverride(values, "skill:demo") != nil {
		t.Fatal("旧 Skill 常驻值仍在生效")
	}
	entries, listed := (&Runtime{}).AgentResidency("bot-a")
	if !listed {
		t.Fatal("列过名单却没报告成列过")
	}
	got := map[string]AgentResidencyEntry{}
	for _, entry := range entries {
		got[entry.ID] = entry
	}
	// Skill 正文改为 read_skill 按需读取，旧常驻值不能出现在管理页中。
	if len(got) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	poke := got[agent.ToolResidentID("poke")]
	if poke.Kind != "tool" || poke.Name != "poke" || !poke.Stale || poke.Resident == nil || !*poke.Resident {
		t.Fatalf("poke = %#v", poke)
	}
	// 插件 ID 没有 kind 前缀，认不出前缀也得当插件显示出来，不能默默藏掉。
	music := got["official.music"]
	if music.Kind != "plugin" || music.Name != "official.music" || !music.Stale || music.Resident == nil || !*music.Resident {
		t.Fatalf("music = %#v", music)
	}
}
