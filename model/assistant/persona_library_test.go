// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPersonaExpressionBundlePreservesDaypartAndExistingEntries(t *testing.T) {
	original := Persona{ID: "diana", Name: "Diana", SystemPrompt: "已写好的 Diana 自定义提示词", ReplyStyle: ReplyStyleHuman, DaypartToneEnabled: boolPointer(true)}
	set := PersonaSet{Personas: []Persona{original}}
	next, saved, err := set.Save(Persona{Name: "Diana（副本）", SystemPrompt: original.SystemPrompt, ReplyStyle: ReplyStyleCatgirl, ActionDescriptionEnabled: boolPointer(true), DaypartToneEnabled: boolPointer(false)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	var restored PersonaSet
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	old, ok := restored.Find("diana")
	if !ok || old.SystemPrompt != original.Normalized().SystemPrompt || old.ReplyStyle != "" || !boolValue(old.DaypartToneEnabled, false) {
		t.Fatal("existing persona changed")
	}
	copy, ok := restored.Find(saved.ID)
	if !ok || copy.DaypartToneEnabled == nil || *copy.DaypartToneEnabled || !boolValue(copy.ActionDescriptionEnabled, false) {
		t.Fatal("expression settings lost")
	}
	legacy := original
	legacy.DaypartToneEnabled = nil
	if legacy.sameContent(original) {
		t.Fatal("import deduplicates different daypart behavior")
	}
	cloned := original.Normalized()
	*cloned.DaypartToneEnabled = false
	if !*original.DaypartToneEnabled {
		t.Fatal("normalization aliases source setting")
	}
}

func TestPersonaSelectionPersistsCustomWithoutRewriting(t *testing.T) {
	custom := &Persona{ID: "custom", Name: "自定义", SystemPrompt: "已写好的自定义正文\n第二行", DaypartToneEnabled: boolPointer(true)}
	cfg := BotConfig{PersonaID: "preset", SystemPrompt: "预设正文", CustomPersona: custom}.WithDefaults()
	data, err := json.Marshal(PayloadFromConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var payload ConfigPayload
	if err = json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	restored := ConfigFromPayload(payload, BotConfig{}).WithDefaults()
	if restored.PersonaID != "preset" || restored.CustomPersona.SystemPrompt != custom.SystemPrompt || restored.SystemPrompt != "预设正文" {
		t.Fatal("persona selection or custom snapshot lost")
	}
	*restored.CustomPersona.DaypartToneEnabled = false
	if !*custom.DaypartToneEnabled {
		t.Fatal("custom snapshot aliases source")
	}
	legacy := BotConfig{SystemPrompt: "历史自定义提示词"}.WithDefaults()
	if legacy.PersonaID != "" || legacy.SystemPrompt != "历史自定义提示词" {
		t.Fatal("legacy config reclassified or rewritten")
	}
}

func TestLegacyRoleplayPersonaMigratesToAssistantWithActions(t *testing.T) {
	persona := (Persona{Name: "旧扮演", ReplyStyle: ReplyStyleRoleplay}).Normalized()
	if persona.ReplyStyle != "" {
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
	if !strings.HasPrefix(updated.SystemPrompt, "你是一只猫\n\n") || !updated.UpdatedAt.After(saved.UpdatedAt) {
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

// 长度上限不能把风格预设切成半截。
//
// 老顺序是「先追加预设、再裁到 4000 字」：3600 字正文加 825 字猫娘预设，一裁正好
// 把预设从中间切开，掉的是最后几条刹车条款——「人设只管语气，不改规则……规则
// 优先，人设让位」和「只对主人称『主人』」。越界防护被长度上限吃掉，比没有预设
// 严重得多。现在的约定是：要么整段预设都在，要么一个字都不追加。
func TestPersonaNormalizedNeverTruncatesStylePresetMidway(t *testing.T) {
	preset := func(style ReplyStyle) string {
		return strings.ReplaceAll(style.stylePrompt(), catgirlNoActionRule+"\n", "")
	}
	catgirl := preset(ReplyStyleCatgirl)
	// 这两条是预设正文的末尾，也正是老实现裁掉的部分。
	const guardRule = "人设只管语气，不改规则"
	const masterRule = "只对主人称「主人」"
	if !strings.Contains(catgirl, guardRule) || !strings.Contains(catgirl, masterRule) {
		t.Fatalf("猫娘预设不再包含安全条款，测试的前提变了：%q", catgirl)
	}

	cases := []struct {
		name       string
		baseRunes  int
		style      ReplyStyle
		wantPreset bool
	}{
		// 3600 + 825：装不下，整段预设不追加。
		{name: "长正文加猫娘预设", baseRunes: 3600, style: ReplyStyleCatgirl, wantPreset: false},
		// 正文本身就顶到上限：同样一个字都追加不了。
		{name: "正文顶格", baseRunes: personaPromptMaxRunes, style: ReplyStyleCatgirl, wantPreset: false},
		// 正文超长时先裁正文，预设依然进不来。
		{name: "正文超长", baseRunes: personaPromptMaxRunes + 500, style: ReplyStyleHuman, wantPreset: false},
		// 装得下就照常追加，安全条款一条不少。
		{name: "短正文加猫娘预设", baseRunes: 200, style: ReplyStyleCatgirl, wantPreset: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := strings.Repeat("字", tc.baseRunes)
			persona := Persona{Name: "长人设", SystemPrompt: base, ReplyStyle: tc.style}.Normalized()

			if persona.ReplyStyle != "" {
				t.Fatalf("旧风格值没被消费掉：%q", persona.ReplyStyle)
			}
			if got := len([]rune(persona.SystemPrompt)); got > personaPromptMaxRunes {
				t.Fatalf("超出上限：%d", got)
			}
			text := preset(tc.style)
			if tc.wantPreset {
				if !strings.HasSuffix(persona.SystemPrompt, "\n\n"+text) {
					t.Fatalf("预设没有被完整追加：%q", persona.SystemPrompt)
				}
				if !strings.Contains(persona.SystemPrompt, guardRule) || !strings.Contains(persona.SystemPrompt, masterRule) {
					t.Fatal("预设末尾的安全条款丢了")
				}
				return
			}
			// 装不下就一个字都不留：既不能出现半段预设，也不能出现被切断的行。
			if persona.SystemPrompt != strings.TrimSpace(truncateRunesPlain(base, personaPromptMaxRunes)) {
				t.Fatalf("正文之外多出了内容：%q", persona.SystemPrompt)
			}
			for _, line := range strings.Split(text, "\n") {
				if line = strings.TrimSpace(line); line == "" {
					continue
				}
				if strings.Contains(persona.SystemPrompt, line) {
					t.Fatalf("预设被追加了一部分：%q", line)
				}
			}
			if head, _, _ := strings.Cut(text, "\n"); strings.Contains(persona.SystemPrompt, head[:12]) {
				t.Fatal("预设首行的残片留在了正文里")
			}
			// 再归一化一次不该有变化：旧风格已经消费掉，不会每次读配置都重试。
			if again := persona.Normalized(); again.SystemPrompt != persona.SystemPrompt {
				t.Fatal("重复归一化改变了正文")
			}
		})
	}
}
