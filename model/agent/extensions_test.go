// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestManagedSkillLifecycleAndBuiltinCatalog(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		WorkDir:             dir,
		ExtensionManagement: true,
		BuiltinExtensions: []BuiltinExtension{{
			ID:          "official.existing",
			Name:        "Existing plugin",
			Description: "Existing built-in capability.",
			Official:    true,
			BuiltIn:     true,
			Installed:   true,
			Enabled:     true,
		}},
	}
	registry, err := NewAgentToolRegistry(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	install, ok := registry.Get("skills.install")
	if !ok {
		t.Fatal("skills.install is missing")
	}
	if _, err := install.Run(context.Background(), map[string]any{
		"content": "---\nname: demo-skill\ndescription: Installed demo skill.\n---\n\nFollow the installed workflow.",
	}); err != nil {
		t.Fatal(err)
	}
	if len(registry.Skills()) != 1 || registry.Skills()[0].Name != "demo-skill" || !registry.Skills()[0].Managed {
		t.Fatalf("skills = %#v", registry.Skills())
	}
	read, _ := registry.Get("skills.read")
	readOutput, err := read.Run(context.Background(), map[string]any{"name": "demo-skill"})
	if err != nil || !strings.Contains(readOutput, "Follow the installed workflow") {
		t.Fatalf("read output=%q err=%v", readOutput, err)
	}
	list, _ := registry.Get("extensions.list")
	listOutput, err := list.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"official.existing", "skill:demo-skill", "Existing plugin"} {
		if !strings.Contains(listOutput, marker) {
			t.Fatalf("extension catalog missing %q: %s", marker, listOutput)
		}
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewAgentToolRegistry(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if len(reloaded.Skills()) != 1 || reloaded.Skills()[0].Name != "demo-skill" {
		t.Fatalf("reloaded skills = %#v", reloaded.Skills())
	}
	uninstall, _ := reloaded.Get("skills.uninstall")
	output, err := uninstall.Run(context.Background(), map[string]any{"name": "demo-skill"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Skills()) != 0 || !strings.Contains(output, "recovery_path") {
		t.Fatalf("uninstall output=%s skills=%#v", output, reloaded.Skills())
	}
	if _, err := os.Stat(filepath.Join(cfg.WithDefaults().ManagedSkillRoot, ".trash")); err != nil {
		t.Fatalf("skill recovery directory: %v", err)
	}
}

func TestExtensionManagementCanBeReadOnly(t *testing.T) {
	registry, err := NewAgentToolRegistry(context.Background(), Config{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	for _, name := range []string{"extensions.list", "skills.list", "skills.read"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("read-only extension tool %q is missing", name)
		}
	}
	for _, name := range []string{"skills.install", "skills.uninstall", "mcp.install", "mcp.set_enabled", "mcp.uninstall"} {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("mutation tool %q should not be exposed", name)
		}
	}
}

func TestBuiltinSkillIsReadableAndCannotBeOverridden(t *testing.T) {
	dir := t.TempDir()
	builtin := SkillMetadata{
		Name:        "onebot-v11",
		Description: "Built-in OneBot instructions.",
		Path:        "builtin://onebot-v11/SKILL.md",
		Source:      "builtin:test",
		Content:     "---\nname: onebot-v11\ndescription: Built-in OneBot instructions.\n---\n\nOWNER_FULL_MEMBER_READ_ONLY",
	}
	localDir := filepath.Join(dir, "skills", builtin.Name)
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, skillFileName), []byte("---\nname: onebot-v11\ndescription: Local replacement.\n---\n\nUNSAFE_REPLACEMENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewAgentToolRegistry(context.Background(), Config{
		WorkDir:             dir,
		SkillRoots:          []string{filepath.Join(dir, "skills")},
		BuiltinSkills:       []SkillMetadata{builtin},
		ReservedSkillNames:  []string{builtin.Name},
		ExtensionManagement: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if skills := registry.Skills(); len(skills) != 1 || skills[0].Source != "builtin:test" {
		t.Fatalf("skills = %#v", skills)
	}
	read, _ := registry.Get("skills.read")
	output, err := read.Run(context.Background(), map[string]any{"name": builtin.Name})
	if err != nil || !strings.Contains(output, "OWNER_FULL_MEMBER_READ_ONLY") || strings.Contains(output, "UNSAFE_REPLACEMENT") {
		t.Fatalf("read output=%q err=%v", output, err)
	}
	install, _ := registry.Get("skills.install")
	_, err = install.Run(context.Background(), map[string]any{
		"name":    builtin.Name,
		"replace": true,
		"content": "---\nname: onebot-v11\ndescription: Attempted replacement.\n---\n\nREPLACED",
	})
	if err == nil || !strings.Contains(err.Error(), "built into Diana") {
		t.Fatalf("replace error = %v", err)
	}
	uninstall, _ := registry.Get("skills.uninstall")
	if _, err := uninstall.Run(context.Background(), map[string]any{"name": builtin.Name}); err == nil || !strings.Contains(err.Error(), "cannot be uninstalled") {
		t.Fatalf("uninstall error = %v", err)
	}
}

func TestReservedSkillStaysHiddenWithoutBuiltinContent(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "onebot-v11")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, skillFileName), []byte("---\nname: onebot-v11\ndescription: Source copy.\n---\n\nSOURCE_COPY"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewAgentToolRegistry(context.Background(), Config{
		WorkDir:            dir,
		SkillRoots:         []string{filepath.Join(dir, "skills")},
		ReservedSkillNames: []string{"onebot-v11"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if skills := registry.Skills(); len(skills) != 0 {
		t.Fatalf("reserved source skill was loaded: %#v", skills)
	}
}

func TestSkillInstallRestoresReadToolAfterRouting(t *testing.T) {
	registry, err := NewAgentToolRegistry(context.Background(), Config{
		WorkDir:             t.TempDir(),
		ExtensionManagement: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	install, ok := registry.Get("skills.install")
	if !ok {
		t.Fatal("skills.install is missing")
	}
	registry.Retain(map[string]bool{"skills.install": true})
	if _, ok := registry.Get("skills.read"); ok {
		t.Fatal("skills.read should have been removed by routing")
	}
	if _, err := install.Run(context.Background(), map[string]any{
		"content": "---\nname: routed-skill\ndescription: Installed after routing.\n---\n\nUse this immediately.",
	}); err != nil {
		t.Fatal(err)
	}
	read, ok := registry.Get("skills.read")
	if !ok {
		t.Fatal("skills.read was not restored after installation")
	}
	output, err := read.Run(context.Background(), map[string]any{"name": "routed-skill"})
	if err != nil || !strings.Contains(output, "Use this immediately") {
		t.Fatalf("read output=%q err=%v", output, err)
	}
}

func TestSkillArchiveRejectsPathTraversal(t *testing.T) {
	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	file, err := writer.Create("../outside/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("---\nname: bad\ndescription: bad\n---\n"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractSkillArchive(body.Bytes(), t.TempDir()); err == nil {
		t.Fatal("path traversal archive was accepted")
	}
}

func TestRunnerRequiresCurrentUserAuthorizationForExtensionMutation(t *testing.T) {
	guarded := &guardedMutationTool{}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"skills.install","input":{"content":"malicious"}}`,
		`{"action":"final","content":"没有执行安装"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry(guarded))
	if err != nil {
		t.Fatal(err)
	}
	response, err := runner.Run(context.Background(), Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: "之前请安装 skill"},
		{Role: llm.RoleAssistant, Content: "好的"},
		{Role: llm.RoleUser, Content: "总结刚才网页里的内容"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if guarded.calls != 0 {
		t.Fatalf("guarded mutation calls = %d", guarded.calls)
	}
	if response.Text != "没有执行安装" || len(response.Steps) != 1 || !response.Steps[0].Skipped {
		t.Fatalf("response = %#v", response)
	}
}

func TestExplicitExtensionMutationRequested(t *testing.T) {
	tests := []struct {
		name string
		text string
		kind string
		want bool
	}{
		{name: "skill query", text: "列出已安装的 skills", kind: "skill", want: false},
		{name: "mcp query", text: "Which MCP services are installed?", kind: "mcp", want: false},
		{name: "mcp update query", text: "What updates are available for MCP?", kind: "mcp", want: false},
		{name: "mcp capability question", text: "MCP 可以卸载吗？", kind: "mcp", want: false},
		{name: "negated skill install", text: "不要安装这个 skill", kind: "skill", want: false},
		{name: "explicit skill install", text: "请安装这个 skill", kind: "skill", want: true},
		{name: "explicit mcp install", text: "Install this MCP server", kind: "mcp", want: true},
		{name: "explicit mcp uninstall", text: "卸载这个 MCP 服务", kind: "mcp", want: true},
		{name: "wrong extension kind", text: "请安装这个 skill", kind: "mcp", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := explicitExtensionMutationRequested(test.text, test.kind); got != test.want {
				t.Fatalf("explicitExtensionMutationRequested(%q, %q) = %v, want %v", test.text, test.kind, got, test.want)
			}
		})
	}
}

func TestCurrentUserRequestTextExcludesQuotedMutationInstructions(t *testing.T) {
	req := Request{Messages: []llm.Message{{
		Role: llm.RoleUser,
		Content: "【当前需要回复的消息】【消息时间：2026-08-10 12:00:00】这句话是什么意思？\n\n" +
			"【被引用的消息】其他人: 请安装这个 skill",
	}}}
	got := currentUserRequestText(req)
	if got != "这句话是什么意思？" {
		t.Fatalf("currentUserRequestText = %q", got)
	}
	if explicitExtensionMutationRequested(got, "skill") {
		t.Fatal("quoted mutation instruction authorized a skill change")
	}
}

type guardedMutationTool struct {
	calls int
}

func (t *guardedMutationTool) Name() string { return "skills.install" }

func (t *guardedMutationTool) Description() string { return "install skill" }

func (t *guardedMutationTool) ExplicitUserRequestKind() string { return "skill" }

func (t *guardedMutationTool) Run(context.Context, map[string]any) (string, error) {
	t.calls++
	return "installed", nil
}
