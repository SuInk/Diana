// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestLegacyRoleplayPersonaMigratesToAssistantWithActions(t *testing.T) {
	persona := (Persona{Name: "旧扮演", ReplyStyle: ReplyStyleRoleplay}).Normalized()
	if persona.ReplyStyle != ReplyStyleAssistant {
		t.Fatalf("旧人设迁移后的表达风格 = %q", persona.ReplyStyle)
	}
	if !boolValue(persona.ActionDescriptionEnabled, false) {
		t.Fatal("旧人设没有迁移为动作描写开关")
	}
}

func TestPersonaSetSaveAddsAndUpdates(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var set PersonaSet

	set, saved, err := set.Save(Persona{Name: " 猫娘 ", ReplyStyle: ReplyStyleCatgirl, SentenceEnders: "喵,喵~"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.Name != "猫娘" {
		t.Fatalf("saved = %#v", saved)
	}
	if len(set.Personas) != 1 {
		t.Fatalf("personas = %#v", set.Personas)
	}

	// 带同一个 ID 是改，不是再加一条。
	set, updated, err := set.Save(Persona{ID: saved.ID, Name: "猫娘", SystemPrompt: "你是一只猫", ReplyStyle: ReplyStyleCatgirl}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Personas) != 1 {
		t.Fatalf("update created a duplicate: %#v", set.Personas)
	}
	if updated.SystemPrompt != "你是一只猫" || !updated.UpdatedAt.After(saved.UpdatedAt) {
		t.Fatalf("updated = %#v", updated)
	}
}

func TestPersonaSetSaveRejectsNamelessAndEmpty(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var set PersonaSet

	if _, _, err := set.Save(Persona{SystemPrompt: "有正文但没名字"}, now); err == nil {
		t.Fatal("nameless persona was accepted")
	}
	// 只有名字的空壳留着没意义：列表里点开是空的，还占一格。
	if _, _, err := set.Save(Persona{Name: "空壳"}, now); err == nil {
		t.Fatal("empty persona was accepted")
	}
}

func TestPersonaSetEnforcesLimitAndRecencyOrder(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var set PersonaSet
	var err error
	for index := 0; index < PersonaLibraryMaxEntries; index++ {
		set, _, err = set.Save(Persona{Name: "人设" + strings.Repeat("x", index%3) + itoaPersona(index), SystemPrompt: "正文"}, now.Add(time.Duration(index)*time.Minute))
		if err != nil {
			t.Fatalf("save %d: %v", index, err)
		}
	}
	if _, _, err := set.Save(Persona{Name: "多出来的一套", SystemPrompt: "正文"}, now); err == nil {
		t.Fatal("library limit was not enforced")
	}
	// 最近改过的排最前，方便再点开。
	if set.Personas[0].UpdatedAt.Before(set.Personas[1].UpdatedAt) {
		t.Fatalf("not sorted by recency: %v %v", set.Personas[0].UpdatedAt, set.Personas[1].UpdatedAt)
	}
}

func TestPersonaSetDeleteIsIdempotent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var set PersonaSet
	set, saved, err := set.Save(Persona{Name: "毒舌", SystemPrompt: "正文"}, now)
	if err != nil {
		t.Fatal(err)
	}
	set = set.Delete(saved.ID)
	if len(set.Personas) != 0 {
		t.Fatalf("delete left %#v", set.Personas)
	}
	// 重复删不该报错，也不该把别的删掉。
	if set = set.Delete(saved.ID); len(set.Personas) != 0 {
		t.Fatalf("second delete returned %#v", set.Personas)
	}
	if _, ok := set.Find(saved.ID); ok {
		t.Fatal("deleted persona is still findable")
	}
}

func TestPersonaWithDefaultsDropsBrokenEntries(t *testing.T) {
	shared := "same-id"
	set := PersonaSet{Personas: []Persona{
		{ID: shared, Name: "一号", SystemPrompt: "正文"},
		{ID: shared, Name: "重复 ID", SystemPrompt: "正文"},
		{Name: "  ", SystemPrompt: "没名字"},
		{ID: "long", Name: strings.Repeat("名", personaNameMaxRunes+10), SystemPrompt: strings.Repeat("字", personaPromptMaxRunes+10)},
	}}.WithDefaults()

	if len(set.Personas) != 2 {
		t.Fatalf("personas = %#v", set.Personas)
	}
	for _, persona := range set.Personas {
		if len([]rune(persona.Name)) > personaNameMaxRunes {
			t.Fatalf("name not truncated: %d", len([]rune(persona.Name)))
		}
		if len([]rune(persona.SystemPrompt)) > personaPromptMaxRunes {
			t.Fatalf("prompt not truncated: %d", len([]rune(persona.SystemPrompt)))
		}
	}
}

func itoaPersona(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// 导入只增不减：同名不覆盖，一律分配新 ID。文件里那些 ID 来自别人的机器，
// 复用它们就等于让一次导入把本地调好的人设静默冲掉。
func TestPersonaImportNeverOverwritesExisting(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var set PersonaSet
	set, mine, err := set.Save(Persona{Name: "猫娘", SystemPrompt: "我自己调的这一版"}, now)
	if err != nil {
		t.Fatal(err)
	}

	set, result := set.Import([]Persona{
		// 同名不同内容：改名，本地那份原封不动。
		{ID: mine.ID, Name: "猫娘", SystemPrompt: "别人机器上的那一版"},
	}, now.Add(time.Minute))

	if result.Renamed != 1 || len(result.Imported) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(set.Personas) != 2 {
		t.Fatalf("personas = %#v", set.Personas)
	}
	original, ok := findPersonaByName(set.Personas, "猫娘")
	if !ok || original.SystemPrompt != "我自己调的这一版" || original.ID != mine.ID {
		t.Fatalf("本地那份被动了：%#v", original)
	}
	imported := result.Imported[0]
	if imported.Name != "猫娘 (2)" {
		t.Fatalf("imported name = %q", imported.Name)
	}
	if imported.ID == mine.ID {
		t.Fatal("导入复用了文件里的 ID，会撞上已有条目")
	}
}

// 同一个文件导两次不该攒出一堆副本：四项完全一样就跳过。
func TestPersonaImportSkipsIdenticalEntries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	file := []Persona{{Name: "值班助理", SystemPrompt: "先给结论", ReplyStyle: ReplyStyleConcise}}

	set, first := PersonaSet{}.Import(file, now)
	if len(first.Imported) != 1 || first.Skipped != 0 {
		t.Fatalf("first import = %#v", first)
	}
	set, second := set.Import(file, now.Add(time.Minute))
	if second.Skipped != 1 || len(second.Imported) != 0 {
		t.Fatalf("second import = %#v", second)
	}
	if len(set.Personas) != 1 {
		t.Fatalf("重复导入攒出了副本：%#v", set.Personas)
	}
}

func TestPersonaImportDropsJunkAndRespectsLimit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	_, result := PersonaSet{}.Import([]Persona{
		{Name: "", SystemPrompt: "没名字"},
		{Name: "空壳"},
		{Name: "正常的", SystemPrompt: "正文"},
	}, now)
	if result.Dropped != 2 || len(result.Imported) != 1 {
		t.Fatalf("result = %#v", result)
	}

	// 装满之后多出来的算 dropped，不是悄悄丢掉不报。
	full := PersonaSet{}
	var err error
	for index := 0; index < PersonaLibraryMaxEntries; index++ {
		full, _, err = full.Save(Persona{Name: "已有" + itoaPersona(index), SystemPrompt: "正文"}, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, overflow := full.Import([]Persona{{Name: "装不下的", SystemPrompt: "正文"}}, now)
	if overflow.Dropped != 1 || len(overflow.Imported) != 0 {
		t.Fatalf("overflow = %#v", overflow)
	}
}

// 名字有长度上限，加后缀前要先把本体裁短，否则裁剪会把后缀吃掉又撞回同一个名字。
func TestPersonaImportRenamesOverlongNameWithoutCollision(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	long := strings.Repeat("名", personaNameMaxRunes)
	set, _, err := PersonaSet{}.Save(Persona{Name: long, SystemPrompt: "本地"}, now)
	if err != nil {
		t.Fatal(err)
	}
	set, result := set.Import([]Persona{{Name: long, SystemPrompt: "导入的"}}, now)
	if result.Renamed != 1 || len(result.Imported) != 1 {
		t.Fatalf("result = %#v", result)
	}
	renamed := result.Imported[0].Name
	if renamed == long {
		t.Fatal("改名后又撞回了同一个名字")
	}
	if len([]rune(renamed)) > personaNameMaxRunes {
		t.Fatalf("改名后超长：%d", len([]rune(renamed)))
	}
	if len(set.Personas) != 2 {
		t.Fatalf("personas = %#v", set.Personas)
	}
}
