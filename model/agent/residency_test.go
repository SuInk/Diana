// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCoreToolsAppliesResidencyTiers(t *testing.T) {
	base := []string{"web_search", "image"}
	owners := map[string][]string{
		ToolResidentID("image"): {"image"},
		ToolResidentID("poke"):  {"poke"},
		"mcp:gitea":             {"gitea_issue", "gitea_repo"},
	}
	overrides := map[string]bool{
		ResidentOverrideKey(ToolResidentID("image")): false,
		ResidentOverrideKey(ToolResidentID("poke")):  true,
		ResidentOverrideKey("mcp:gitea"):             true,
	}
	got := ResolveCoreTools(base, owners, overrides)
	want := []string{"web_search", "gitea_issue", "gitea_repo", "poke"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("core tools = %#v, want %#v", got, want)
	}
	// 没配档位就该逐字等于默认名单：这个数组决定请求里 tools 的顺序，顺序一抖缓存就断。
	if plain := ResolveCoreTools(base, owners, nil); strings.Join(plain, ",") != strings.Join(base, ",") {
		t.Fatalf("no override changed the list: %#v", plain)
	}
}

func TestToolOwnersGroupsMCPToolsByServer(t *testing.T) {
	registry := NewToolRegistry(&countingTool{name: "alpha"}, &countingTool{name: "beta"})
	owners := registry.ToolOwners()
	if len(owners[ToolResidentID("alpha")]) != 1 || len(owners[ToolResidentID("beta")]) != 1 {
		t.Fatalf("owners = %#v", owners)
	}
}

// 常驻 skill 的正文要真的跟着请求走：用户配这一档就是因为「用到再 read_skill」在长
// 上下文里不可靠。
func TestResidentSkillShipsItsBody(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: demo\ndescription: d\n---\n\n照这三步做完。"), 0o600); err != nil {
		t.Fatal(err)
	}
	deferred := RenderSkillsCatalog([]SkillMetadata{{Name: "demo", Description: "d", Path: path}}, 8000)
	if strings.Contains(deferred, "照这三步做完") {
		t.Fatalf("按需档不该带正文:\n%s", deferred)
	}
	resident := RenderSkillsCatalog([]SkillMetadata{{Name: "demo", Description: "d", Path: path, IncludeBody: true}}, 8000)
	if !strings.Contains(resident, "照这三步做完") || !strings.Contains(resident, "Resident skill: demo") {
		t.Fatalf("常驻档没带正文:\n%s", resident)
	}
}

func TestExtensionOverridesCarryResidencyToSkills(t *testing.T) {
	root := t.TempDir()
	if err := SaveExtensionResidency(root, "bot-a", "skill:demo", boolPointer(true)); err != nil {
		t.Fatal(err)
	}
	values, err := LoadExtensionOverrides(root, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	registry := NewToolRegistry(&SkillsReadTool{})
	registry.SetSkills([]SkillMetadata{{Name: "demo", Description: "d", Path: "/tmp/demo/SKILL.md"}})
	registry.ApplyExtensionOverrides(values)
	skills := registry.Skills()
	if len(skills) != 1 || skills[0].Resident == nil || !*skills[0].Resident {
		t.Fatalf("skills = %#v", skills)
	}
	// 退回默认档要真的把键删掉，否则「默认」和「按需」在文件里长得一样。
	if err := SaveExtensionResidency(root, "bot-a", "skill:demo", nil); err != nil {
		t.Fatal(err)
	}
	values, err = LoadExtensionOverrides(root, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	if ResidentOverride(values, "skill:demo") != nil {
		t.Fatalf("档位没有退回默认: %#v", values)
	}
}

func boolPointer(value bool) *bool { return &value }
