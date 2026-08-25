// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"
)

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
