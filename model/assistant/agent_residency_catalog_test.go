// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
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
	// Skill 不归这份名单管，它的档位单写，也不该跑到这一页来。
	resident := true
	if err := agent.SaveExtensionResidency(workspace, "bot-a", "skill:demo", &resident, nil); err != nil {
		t.Fatal(err)
	}
	entries, listed := (&Runtime{}).AgentResidency("bot-a")
	if !listed {
		t.Fatal("列过名单却没报告成列过")
	}
	got := map[string]AgentResidencyEntry{}
	for _, entry := range entries {
		got[entry.ID] = entry
	}
	// skill 的档位归 Skills 标签管，不该跑到这一页来。
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
