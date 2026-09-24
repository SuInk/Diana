// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 导出的 YAML 读回来要是同一套人设：改过的提示词原样回来，没改过的不变成覆盖。
func TestPersonaYAMLRoundTripKeepsPrompts(t *testing.T) {
	persona := Persona{
		Name:          "猫娘",
		SystemPrompt:  "第一行\n第二行",
		SelfReference: "咱",
		Prompts:       PromptOverrides{promptWakeOnlySpec.Key: "叫我就接着说\n别只报到"},
	}
	out, err := RenderPersonaYAML([]Persona{persona})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, spec := range PromptSpecs() {
		if !strings.Contains(text, "\n  "+spec.Key+":") {
			t.Fatalf("rendered YAML misses prompt %s", spec.Key)
		}
	}
	if !strings.Contains(text, "system_prompt: |-") || strings.Contains(text, "updated_at") {
		t.Fatalf("persona fields not rendered as block YAML:\n%s", text[:400])
	}
	document, err := ParsePersonaDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	got := document.Personas[0]
	if got.SystemPrompt != persona.SystemPrompt || got.SelfReference != "咱" {
		t.Fatalf("persona fields changed: %#v", got)
	}
	if len(got.Prompts) != 1 || got.Prompts[promptWakeOnlySpec.Key] != "叫我就接着说\n别只报到" {
		t.Fatalf("prompts = %#v, want only the edited one", got.Prompts)
	}
}

func TestPersonaYAMLLibraryRoundTrip(t *testing.T) {
	out, err := RenderPersonaYAML([]Persona{{ID: "a", Name: "甲", SystemPrompt: "甲"}, {ID: "b", Name: "乙", SystemPrompt: "乙", Prompts: PromptOverrides{promptImageOnlySpec.Key: "看图"}}})
	if err != nil {
		t.Fatal(err)
	}
	document, err := ParsePersonaDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Personas) != 2 || document.Personas[1].Prompts[promptImageOnlySpec.Key] != "看图" || document.Personas[0].Name != "甲" {
		t.Fatalf("library round trip = %#v", document.Personas)
	}
}

// 人设文件就是全部提示词配置：少一段不能悄悄回落默认值，多一段拼错的键也不能被丢掉。
func TestPersonaYAMLRejectsIncompletePrompts(t *testing.T) {
	out, err := RenderPersonaYAML([]Persona{{Name: "猫娘", SystemPrompt: "喵"}})
	if err != nil {
		t.Fatal(err)
	}
	missing := strings.Replace(string(out), "\n  "+promptWakeOnlySpec.Key+": |-", "\n  removed_marker: |-", 1)
	_, err = ParsePersonaDocument([]byte(missing))
	if err == nil || !strings.Contains(err.Error(), "removed_marker") {
		t.Fatalf("unknown key accepted: %v", err)
	}
	// 整段删掉：键和它的正文都没了。
	lines := strings.Split(string(out), "\n")
	var kept []string
	for index := 0; index < len(lines); index++ {
		if strings.HasPrefix(lines[index], "  "+promptWakeOnlySpec.Key+":") {
			index++ // 跳过正文那一行
			continue
		}
		kept = append(kept, lines[index])
	}
	_, err = ParsePersonaDocument([]byte(strings.Join(kept, "\n")))
	if err == nil || !strings.Contains(err.Error(), promptWakeOnlySpec.Key) || !strings.Contains(err.Error(), "缺少 1 段") {
		t.Fatalf("missing prompt accepted: %v", err)
	}
}

// 人设文件按 format_version 解析：没写版本号的旧文件、比当前新的版本都要拒绝并说清原因，
// 不能按猜的规则读成一套和作者给的不一样的人设。
func TestPersonaDocumentFormatVersion(t *testing.T) {
	out, err := RenderPersonaYAML([]Persona{{Name: "猫娘", SystemPrompt: "喵"}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.HasPrefix(text, "format_version: 1\n") {
		t.Fatalf("rendered YAML lacks format_version: 1:\n%s", text[:200])
	}
	document, err := ParsePersonaDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	if document.FormatVersion != PersonaFormatVersion {
		t.Fatalf("FormatVersion = %d", document.FormatVersion)
	}

	for name, raw := range map[string]string{
		"v0 single": "name: 老人设\nsystem_prompt: 说话简短\n",
		"v0 list":   "version: 1\npersonas:\n  - name: 老人设\n    system_prompt: 说话简短\n",
		"v0 json":   `{"version":1,"personas":[{"name":"老人设","system_prompt":"说话简短"}]}`,
	} {
		if _, err := ParsePersonaDocument([]byte(raw)); err == nil || !strings.Contains(err.Error(), "format_version") {
			t.Errorf("%s: legacy document accepted or unclear error: %v", name, err)
		}
	}

	newer := strings.Replace(text, "format_version: 1", "format_version: 2", 1)
	if _, err := ParsePersonaDocument([]byte(newer)); err == nil || !strings.Contains(err.Error(), "2") {
		t.Fatalf("format_version 2 accepted or unclear error: %v", err)
	}
	if _, err := ParsePersonaDocument([]byte(strings.Replace(text, "format_version: 1", "format_version: 0", 1))); err == nil {
		t.Fatal("format_version 0 accepted")
	}
}

// 版本 1 严格读：拼错的字段名要报出来，每套都要有名字和 prompts。
func TestPersonaDocumentV1IsStrict(t *testing.T) {
	out, err := RenderPersonaYAML([]Persona{{Name: "猫娘", SystemPrompt: "喵"}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	typo := strings.Replace(text, "\nsystem_prompt:", "\nsystem_promt:", 1)
	if typo == text {
		t.Fatalf("system_prompt not found at top level:\n%s", text[:400])
	}
	if _, err := ParsePersonaDocument([]byte(typo)); err == nil || !strings.Contains(err.Error(), "system_promt") {
		t.Fatalf("unknown field accepted or not named: %v", err)
	}
	if _, err := ParsePersonaDocument([]byte("format_version: 1\nsystem_prompt: 喵\n")); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("nameless persona accepted: %v", err)
	}
	if _, err := ParsePersonaDocument([]byte("format_version: 1\nname: 猫娘\nsystem_prompt: 喵\n")); err == nil || !strings.Contains(err.Error(), "prompts") {
		t.Fatalf("persona without prompts accepted: %v", err)
	}
	library, err := RenderPersonaYAML([]Persona{{Name: "甲", SystemPrompt: "甲"}, {Name: "乙", SystemPrompt: "乙"}})
	if err != nil {
		t.Fatal(err)
	}
	extra := strings.Replace(string(library), "format_version: 1\n", "format_version: 1\nauthor: 某人\n", 1)
	if _, err := ParsePersonaDocument([]byte(extra)); err == nil || !strings.Contains(err.Error(), "author") {
		t.Fatalf("unknown top-level field in a library accepted: %v", err)
	}
}

func TestPersonaWithOnlyPromptsIsNotEmpty(t *testing.T) {
	if (Persona{Name: "只改提示词", Prompts: PromptOverrides{promptImageOnlySpec.Key: "看图"}}).Normalized().Empty() {
		t.Fatal("a persona that only customizes prompts should be savable")
	}
}

// 判据跟着人设走：空着也写进文件，读回来原样；输出格式改过的也能往返。
func TestPersonaYAMLCarriesCriteriaAndFormats(t *testing.T) {
	persona := Persona{
		Name:               "群管",
		SystemPrompt:       "说话短",
		ExtraCriteria:      "群里叫「糖宝」的是另一位群友",
		AccountSafetyRules: "不许发外链",
		Prompts:            PromptOverrides{promptParticipationFormatSpec.Key: "只输出 JSON"},
	}
	out, err := RenderPersonaYAML([]Persona{persona})
	if err != nil {
		t.Fatal(err)
	}
	if blank, err := RenderPersonaYAML([]Persona{{Name: "空"}}); err != nil || !strings.Contains(string(blank), "extra_criteria: \"\"") || !strings.Contains(string(blank), "account_safety_rules: \"\"") {
		t.Fatalf("empty criteria should still be listed: %v", err)
	}
	for _, spec := range PromptSpecs() {
		if spec.FormatKey != "" && !strings.Contains(string(out), "\n  "+spec.FormatKey+":") {
			t.Fatalf("format of %s missing from YAML", spec.Key)
		}
	}
	document, err := ParsePersonaDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	got := document.Personas[0]
	if got.ExtraCriteria != persona.ExtraCriteria || got.AccountSafetyRules != persona.AccountSafetyRules || len(got.Prompts) != 1 {
		t.Fatalf("criteria/prompts changed: %#v", got)
	}
}

// 输出格式能改：覆盖值替换默认格式，分隔符由程序补。
func TestPromptFormatOverride(t *testing.T) {
	spec := firstSpecWithContract()
	custom := PromptOverrides{spec.FormatKey: "只输出 {\"keep\":true}"}
	got := custom.text(spec)
	if !strings.HasSuffix(got, "只输出 {\"keep\":true}") || !strings.HasPrefix(got, spec.Default) {
		t.Fatalf("format override = %q", got)
	}
	if normalizePromptOverrides(PromptOverrides{spec.FormatKey: spec.Contract}) != nil {
		t.Fatal("a format equal to the default should not be stored")
	}
}

func firstSpecWithContract() *PromptSpec {
	for _, spec := range promptRegistry {
		if spec.Contract != "" {
			return spec
		}
	}
	panic("no spec with a contract")
}
