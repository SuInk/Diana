// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SuInk/diana/model/llm"
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
	catalog := RenderSkillsCatalog(skills, 8000)
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
	page := decodeSkillPage(t, got)
	if page.HasMore || page.NextOffset != nil || page.Offset != 0 {
		t.Fatalf("short skill should remain a complete single read: %+v", page)
	}
}

type skillPageResult struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Content    string `json:"content"`
	Offset     int    `json:"offset"`
	TotalChars int    `json:"total_chars"`
	HasMore    bool   `json:"has_more"`
	NextOffset *int   `json:"next_offset"`
}

func decodeSkillPage(t *testing.T, output string) skillPageResult {
	t.Helper()
	var page skillPageResult
	if err := json.Unmarshal([]byte(output), &page); err != nil {
		t.Fatalf("skill page is not complete JSON: %v", err)
	}
	return page
}

func TestReadSkillPagesKeepJSONAndUnicode(t *testing.T) {
	content := strings.Repeat("甲😀\"\\\n乙{测试}", 40)
	tool := NewSkillTools([]SkillMetadata{{Name: "paged", Path: "skills/paged/SKILL.md", Content: content}}).Read
	const budget = 384
	ctx := WithToolOutputBudget(context.Background(), budget)
	var joined strings.Builder
	offset, pages := 0, 0
	for {
		output, err := tool.Run(ctx, map[string]any{"name": "paged", "offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		if size := utf8.RuneCountInString(output); size > budget {
			t.Fatalf("JSON envelope exceeded the existing output allowance: %d > %d", size, budget)
		}
		page := decodeSkillPage(t, output)
		if page.Offset != offset || page.TotalChars != utf8.RuneCountInString(content) || !utf8.ValidString(page.Content) {
			t.Fatalf("invalid page coordinates: %+v", page)
		}
		joined.WriteString(page.Content)
		pages++
		if !page.HasMore {
			if page.NextOffset != nil {
				t.Fatal("finished page must not advertise another offset")
			}
			break
		}
		if page.NextOffset == nil || *page.NextOffset != offset+utf8.RuneCountInString(page.Content) || *page.NextOffset <= offset {
			t.Fatalf("page did not provide a progressing Unicode offset: %+v", page)
		}
		offset = *page.NextOffset
	}
	if pages < 2 || joined.String() != content {
		t.Fatalf("paged skill lost content: pages=%d", pages)
	}
	output, err := tool.Run(ctx, map[string]any{"name": "paged", "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	if page := decodeSkillPage(t, output); page.Content != "甲😀" || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatalf("limit must count Unicode characters: %+v", page)
	}
	output, err = tool.Run(ctx, map[string]any{"name": "paged", "offset": utf8.RuneCountInString(content)})
	if err != nil {
		t.Fatal(err)
	}
	if page := decodeSkillPage(t, output); page.Content != "" || page.HasMore || page.NextOffset != nil {
		t.Fatalf("reading exactly EOF should be explicit: %+v", page)
	}
}

func TestReadSkillRejectsInvalidPages(t *testing.T) {
	tool := NewSkillTools([]SkillMetadata{{Name: "demo", Content: "甲😀乙"}}).Read
	for _, input := range []map[string]any{
		{"name": "demo", "offset": -1},
		{"name": "demo", "offset": 4},
		{"name": "demo", "offset": 1.5},
		{"name": "demo", "limit": 0},
		{"name": "demo", "limit": -1},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("invalid page was accepted: %#v", input)
		}
	}
	if _, err := tool.Run(WithToolOutputBudget(context.Background(), 10), map[string]any{"name": "demo"}); err == nil {
		t.Fatal("an allowance smaller than the JSON envelope must fail explicitly")
	}
}

// Exercise the actual Runner result cap and native tool messages, not just the
// reader helper. Escaped characters make JSON longer than its decoded content.
func TestRunnerReadsLongSkillThroughTwoValidJSONPages(t *testing.T) {
	content := strings.Repeat("步骤😀\"\\\n", 1100) + "最后的安全约束"
	skills := []SkillMetadata{{Name: "long-skill", Description: "long workflow", Path: "skills/long/SKILL.md", Content: content}}
	registry := NewToolRegistry(NewSkillTools(skills).Read)
	registry.SetSkills(skills)
	client := &dispatchTestClient{}
	var pages []skillPageResult
	client.hook = func(call int) {
		if call == 1 {
			client.replies = []*llm.GenerateResponse{dispatchReply(true, "read_skill", map[string]any{"name": "long-skill"})}
			return
		}
		if call > 3 {
			t.Fatal("the skill should need exactly two reads")
		}
		messages := client.requests[len(client.requests)-1].Messages
		last := messages[len(messages)-1]
		if last.Role != llm.RoleTool || last.ToolName != "read_skill" || last.ToolError {
			t.Fatalf("expected a successful native skill result: %+v", last)
		}
		page := decodeSkillPage(t, extractJSON(last.Content))
		pages = append(pages, page)
		if page.HasMore {
			if page.NextOffset == nil {
				t.Fatal("continuation offset missing from the actual model observation")
			}
			client.replies = []*llm.GenerateResponse{dispatchReply(true, "read_skill", map[string]any{"name": "long-skill", "offset": *page.NextOffset})}
			return
		}
		client.replies = []*llm.GenerateResponse{dispatchReply(true, finalizeToolName, map[string]any{"content": "已读到末尾"})}
	}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2, CoreTools: []string{"read_skill"}}, registry)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "读取完整 long-skill"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Steps) != 2 || len(pages) != 2 || !pages[0].HasMore || pages[1].HasMore || pages[0].Content+pages[1].Content != content {
		t.Fatalf("Runner did not deliver the complete skill in two pages: steps=%d pages=%d", len(result.Steps), len(pages))
	}
	for _, step := range result.Steps {
		if step.Error != "" || !json.Valid([]byte(step.Output)) || utf8.RuneCountInString(step.Output) > DefaultMaxToolOutputChars {
			t.Fatalf("Runner truncated or failed a skill page: error=%q", step.Error)
		}
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
