// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCoreToolsFollowsTheSavedList(t *testing.T) {
	base := []string{"web_search", "image"}
	owners := map[string][]string{
		ToolResidentID("web_search"): {"web_search"},
		ToolResidentID("image"):      {"image"},
		ToolResidentID("poke"):       {"poke"},
		"mcp:gitea":                  {"gitea_issue", "gitea_repo"},
	}
	// 列过名单就完全以名单为准：没列进去的推荐项（image）也不再常驻。
	listed := map[string]bool{
		"residency:list": true,
		ResidentOverrideKey(ToolResidentID("web_search")): true,
		ResidentOverrideKey(ToolResidentID("poke")):       true,
		ResidentOverrideKey("mcp:gitea"):                  true,
	}
	got := ResolveCoreTools(base, owners, listed)
	want := []string{"web_search", "gitea_issue", "gitea_repo", "poke"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("core tools = %#v, want %#v", got, want)
	}
	// 空名单是「一个都不常驻」，不是「没列过」。
	if empty := ResolveCoreTools(base, owners, map[string]bool{"residency:list": true}); len(empty) != 0 {
		t.Fatalf("empty list = %#v", empty)
	}
	// 没列过就逐字等于推荐名单：这个数组决定请求里 tools 的顺序，顺序一抖缓存就断。
	if plain := ResolveCoreTools(base, owners, nil); strings.Join(plain, ",") != strings.Join(base, ",") {
		t.Fatalf("no list changed the result: %#v", plain)
	}
}

// 就地加一个 / 删一个：第一次这么写要把当前生效的推荐名单固定下来，否则「加一个」
// 会顺手把推荐的全清掉。
func TestSaveExtensionResidencyKeepsTheRecommendedListOnFirstEdit(t *testing.T) {
	root := t.TempDir()
	recommended := RecommendedResidencyIDs([]string{"web_search", "image"})
	if err := SaveExtensionResidency(root, "bot-a", "mcp:gitea", boolPointer(true), recommended); err != nil {
		t.Fatal(err)
	}
	values, err := LoadExtensionOverrides(root, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	ids, listed := ResidencyList(values)
	if !listed || strings.Join(ids, ",") != "mcp:gitea,tool:image,tool:web_search" {
		t.Fatalf("list = %#v listed=%v", ids, listed)
	}
	// 再删一个，剩下的原样留着。
	if err := SaveExtensionResidency(root, "bot-a", ToolResidentID("image"), boolPointer(false), recommended); err != nil {
		t.Fatal(err)
	}
	values, _ = LoadExtensionOverrides(root, "bot-a")
	ids, _ = ResidencyList(values)
	if strings.Join(ids, ",") != "mcp:gitea,tool:web_search" {
		t.Fatalf("list after removal = %#v", ids)
	}
	// 退回推荐名单要把名单整个删掉，而不是留一份空的：空名单是「一个都不常驻」。
	if err := SaveResidencyList(root, "bot-a", nil); err != nil {
		t.Fatal(err)
	}
	values, _ = LoadExtensionOverrides(root, "bot-a")
	if _, listed := ResidencyList(values); listed {
		t.Fatalf("reset left a list behind: %#v", values)
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
	if err := SaveExtensionResidency(root, "bot-a", "skill:demo", boolPointer(true), nil); err != nil {
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
	// Skill 不归常驻名单管，它仍是三态：不带 resident 就退回「看触发词」，键要真的删掉。
	if err := SaveExtensionResidency(root, "bot-a", "skill:demo", nil, nil); err != nil {
		t.Fatal(err)
	}
	values, err = LoadExtensionOverrides(root, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	if ResidentOverride(values, "skill:demo") != nil {
		t.Fatalf("skill 档位没有退回默认: %#v", values)
	}
	// 工具名单存在时也一样：编辑过工具名单，不该顺带把带触发词的 Skill 判成永不注入。
	if err := SaveResidencyList(root, "bot-a", []string{ToolResidentID("poke")}); err != nil {
		t.Fatal(err)
	}
	values, _ = LoadExtensionOverrides(root, "bot-a")
	if ResidentOverride(values, "skill:demo") != nil {
		t.Fatalf("工具名单波及了 Skill: %#v", values)
	}
}

func boolPointer(value bool) *bool { return &value }

// 档位界面要把「常驻更贵」说成数字，两档的估算就不能是同一个数：常驻发的是整份
// 声明（描述 + JSON Schema），按需只发目录里的一行。
func TestResidencyCostSeparatesTiers(t *testing.T) {
	registry := NewToolRegistry(&SkillsReadTool{})
	tool, ok := registry.Get("read_skill")
	if !ok {
		t.Fatalf("registry = %#v", registry.Names())
	}
	resident, deferred := ResidencyCost(tool)
	if deferred <= 0 || resident <= deferred {
		t.Fatalf("resident=%d deferred=%d，常驻没有比按需贵", resident, deferred)
	}
	if got, _ := ResidencyCost(nil); got != 0 {
		t.Fatalf("nil 工具应当不计开销，得到 %d", got)
	}
}

// 目录是每轮现攒的，进程重启后就空了，而档位文件还在生效——界面得能把配过的项
// 认出来，否则用户会以为配置丢了。
func TestResidentOverrideIDsListsConfiguredEntries(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{ToolResidentID("poke"), "mcp:gitea"} {
		if err := SaveExtensionResidency(root, "bot-a", id, boolPointer(true), nil); err != nil {
			t.Fatal(err)
		}
	}
	values, err := LoadExtensionOverrides(root, "bot-a")
	if err != nil {
		t.Fatal(err)
	}
	// 普通的启用开关不带 resident: 前缀，不该混进来。
	values["mcp:other"] = true
	if got := strings.Join(ResidentOverrideIDs(values), ","); got != "mcp:gitea,tool:poke" {
		t.Fatalf("ids = %q", got)
	}
}

// 名单的单位可以是整条，也可以是里面的某几个工具：插件整条进名单就是全部带上，
// 想少带一个就改成把其余工具逐个写进名单——不需要「排除」这种反向状态。
func TestResolveCoreToolsMixesWholeEntriesAndSingleTools(t *testing.T) {
	owners := map[string][]string{
		"official.browser":               {"browser_render", "browser_click", "browser_text"},
		ToolResidentID("browser_render"): {"browser_render"},
		ToolResidentID("browser_click"):  {"browser_click"},
		ToolResidentID("browser_text"):   {"browser_text"},
	}
	whole := map[string]bool{"residency:list": true, ResidentOverrideKey("official.browser"): true}
	if got := strings.Join(ResolveCoreTools(nil, owners, whole), ","); got != "browser_click,browser_render,browser_text" {
		t.Fatalf("whole plugin = %q", got)
	}
	picked := map[string]bool{
		"residency:list": true,
		ResidentOverrideKey(ToolResidentID("browser_render")): true,
		ResidentOverrideKey(ToolResidentID("browser_text")):   true,
	}
	if got := strings.Join(ResolveCoreTools(nil, owners, picked), ","); got != "browser_render,browser_text" {
		t.Fatalf("picked tools = %q", got)
	}
}
