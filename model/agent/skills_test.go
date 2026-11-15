// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadSkillsAndReadTool 验证本地 SKILL.md skill 发现和完整读取。
func TestLoadSkillsAndReadTool(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "demo/SKILL.md", "---\nname: demo-skill\ndescription: Use this demo skill.\n---\n\nDo the demo workflow.")
	skills, err := LoadSkills([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Name != "demo-skill" {
		t.Fatalf("skills = %#v", skills)
	}
	catalog := RenderSkillsCatalog(skills)
	if !strings.Contains(catalog, "demo-skill") || !strings.Contains(catalog, "read_skill") {
		t.Fatalf("catalog did not include skill guidance: %s", catalog)
	}
	// 目录只给名称和用途:磁盘路径既是噪音也是泄露,正文路径由 read_skill 自己返回。
	if strings.Contains(catalog, skills[0].Path) {
		t.Fatalf("catalog leaked the skill path: %s", catalog)
	}
	tools := NewSkillTools(skills)
	got, err := tools.Read.Run(context.Background(), map[string]any{"name": "demo-skill"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Do the demo workflow.") {
		t.Fatalf("read output = %s", got)
	}
}

// TestSelectExplicitSkillsRequiresBoundary 验证 $skill 显式触发边界。
func TestSelectExplicitSkillsRequiresBoundary(t *testing.T) {
	skills := []SkillMetadata{{Name: "demo-skill", Description: "demo", Path: "/tmp/SKILL.md"}}
	if got := SelectExplicitSkills(skills, "run $demo-skill please"); len(got) != 1 {
		t.Fatalf("selected = %#v, want one", got)
	}
	if got := SelectExplicitSkills(skills, "run $demo-skill-extra please"); len(got) != 0 {
		t.Fatalf("selected = %#v, want none", got)
	}
}

func TestConfigDefaultsUseWorkDirSkillsAndMCPConfig(t *testing.T) {
	home := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("HOME", home)
	writeTestFile(t, home, ".codex/skills/demo/SKILL.md", "---\nname: demo\ndescription: demo\n---\n")
	writeTestFile(t, home, ".codex/config.toml", "[mcp_servers]\n")

	cfg := Config{WorkDir: workDir}.WithDefaults()
	if containsString(cfg.SkillRoots, filepath.Join(home, ".codex", "skills")) {
		t.Fatalf("SkillRoots = %#v, should not include home codex skills by default", cfg.SkillRoots)
	}
	wantSkills := filepath.Join(workDir, "skills")
	if !containsString(cfg.SkillRoots, wantSkills) {
		t.Fatalf("SkillRoots = %#v, missing %q", cfg.SkillRoots, wantSkills)
	}
	// MCP 配置默认在工作目录外面：里面存着令牌，放在目录里就落进文件工具的可达范围。
	wantMCP := defaultMCPConfigPath(workDir)
	if cfg.MCPConfigPath != wantMCP {
		t.Fatalf("MCPConfigPath = %q, want %q", cfg.MCPConfigPath, wantMCP)
	}
}

func TestConfigRelativeExtensionPathsResolveOnceFromWorkDir(t *testing.T) {
	base, err := filepath.Abs("relative-agent-work")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{WorkDir: "relative-agent-work"}.WithDefaults()
	if want := filepath.Join(base, ".agents", "skills"); cfg.ManagedSkillRoot != want {
		t.Fatalf("ManagedSkillRoot = %q, want %q", cfg.ManagedSkillRoot, want)
	}
	if want := defaultMCPConfigPath(base); resolveMCPConfigPath(cfg) != want {
		t.Fatalf("resolved MCPConfigPath = %q, want %q", resolveMCPConfigPath(cfg), want)
	}

	custom := Config{
		WorkDir:          "relative-agent-work",
		ManagedSkillRoot: "managed-skills",
		MCPConfigPath:    filepath.Join("config", "mcp.json"),
	}.WithDefaults()
	if want := filepath.Join(base, "managed-skills"); custom.ManagedSkillRoot != want {
		t.Fatalf("custom ManagedSkillRoot = %q, want %q", custom.ManagedSkillRoot, want)
	}
	if want := filepath.Join(base, "config", "mcp.json"); resolveMCPConfigPath(custom) != want {
		t.Fatalf("custom resolved MCPConfigPath = %q, want %q", resolveMCPConfigPath(custom), want)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// 目录和常驻正文全部带上：skill 再多、正文再长也不截、不改成「请先 read_skill」。
func TestRenderSkillsCatalogKeepsEveryEntryAndResidentBody(t *testing.T) {
	resident := true
	body := strings.Repeat("resident body line\n", 3000)
	var skills []SkillMetadata
	for i := range 300 {
		skills = append(skills, SkillMetadata{Name: fmt.Sprintf("skill-%03d", i), Description: strings.Repeat("d", 100)})
	}
	skills = append(skills,
		SkillMetadata{Name: "big-a", Description: "a", Content: body, Resident: &resident, IncludeBody: true},
		SkillMetadata{Name: "big-b", Description: "b", Content: body, Resident: &resident, IncludeBody: true},
	)
	catalog, delivered := renderSkillsCatalog(skills)
	if !strings.Contains(catalog, "skill-299") {
		t.Fatal("目录被截断")
	}
	if strings.Count(catalog, strings.TrimSpace(body)) != 2 || !delivered["big-a"] || !delivered["big-b"] {
		t.Fatalf("常驻正文没有全部带上：delivered=%v", delivered)
	}
	triggered := renderTriggeredSkillBodies([]SkillMetadata{
		{Name: "hit-a", Content: body, IncludeBody: true},
		{Name: "hit-b", Content: body, IncludeBody: true},
	}, map[string]bool{})
	if strings.Count(triggered, strings.TrimSpace(body)) != 2 {
		t.Fatal("命中的 skill 正文没有全部带上")
	}
}
