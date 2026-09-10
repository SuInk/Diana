package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLegacyStyleBecomesEditablePersonaOnce(t *testing.T) {
	for _, style := range KnownReplyStyles() {
		t.Run(string(style), func(t *testing.T) {
			original := BotConfig{SystemPrompt: "你叫嘉然，喜欢音乐。", ReplyStyle: style, SelfReference: "咱", SentenceEnders: "呀", ChatInCooldownSeconds: 120}
			cfg := original.WithDefaults()
			if cfg.ReplyStyle != "" || !strings.HasPrefix(cfg.SystemPrompt, original.SystemPrompt+"\n\n") || cfg.SelfReference != "咱" || cfg.SentenceEnders != "呀" || cfg.ChatInCooldownSeconds != 120 {
				t.Fatal("migration lost persona or unrelated preferences")
			}
			data, err := json.Marshal(PayloadFromConfig(cfg))
			if err != nil || strings.Contains(string(data), `"reply_style"`) {
				t.Fatalf("legacy field still exported: %v", err)
			}
			var payload ConfigPayload
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatal(err)
			}
			if restored := ConfigFromPayload(payload, BotConfig{}).WithDefaults(); restored.SystemPrompt != cfg.SystemPrompt {
				t.Fatal("reloading appended the style twice")
			}
			cfg.SystemPrompt = "你是冷静的技术同事。"
			r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			prompt := r.systemPrompt(MessageEvent{Kind: EventKindPrivate, UserID: "1"}, nil)
			if !strings.Contains(prompt, cfg.SystemPrompt) || strings.Contains(prompt, style.stylePrompt()) {
				t.Fatal("runtime reintroduced the removed style after editing persona")
			}
		})
	}
}

func TestLegacyStyleMigrationCoversGroupAndCustomSnapshot(t *testing.T) {
	base := BotConfig{SystemPrompt: "你叫嘉然。", CustomPersona: &Persona{SystemPrompt: "我的原文", ReplyStyle: ReplyStyleGentle}}.WithDefaults()
	if base.CustomPersona.ReplyStyle != "" || !strings.Contains(base.CustomPersona.SystemPrompt, "我的原文") || !strings.Contains(base.CustomPersona.SystemPrompt, ReplyStyleGentle.stylePrompt()) {
		t.Fatal("custom snapshot was not migrated")
	}
	for _, own := range []string{"", "本群的人设"} {
		group := (GroupConfig{SystemPrompt: own, ReplyStyle: ReplyStyleCatgirl}).WithDefaults("g", base)
		prefix := own
		if prefix == "" {
			prefix = base.SystemPrompt
		}
		if group.ReplyStyle != "" || !strings.HasPrefix(group.SystemPrompt, prefix+"\n\n") || group.WithDefaults("g", base).SystemPrompt != group.SystemPrompt {
			t.Fatal("group migration lost its persona or was not idempotent")
		}
	}
	inherit := (GroupConfig{}).WithDefaults("g", base)
	if inherit.SystemPrompt != "" {
		t.Fatal("ordinary group inheritance became an override")
	}
}

func TestGroupStyleMigrationUsesOnlyItsOwnRobot(t *testing.T) {
	a := BotConfig{ID: "a", SystemPrompt: "机器人 A", ReplyStyle: ReplyStyleAssistant}.WithDefaults()
	b := BotConfig{ID: "b", SystemPrompt: "机器人 B", ReplyStyle: ReplyStyleLively}.WithDefaults()
	set := (GroupConfigSet{Groups: []GroupConfig{{BotProfileID: "b", GroupID: "g", ReplyStyle: ReplyStyleGentle}}}).WithDefaults(a)
	deferred := set.Groups[0]
	if deferred.SystemPrompt != "" || deferred.ReplyStyle != ReplyStyleGentle {
		t.Fatal("another robot consumed this group style during bulk normalization")
	}
	migrated := deferred.WithDefaults("g", b)
	if !strings.HasPrefix(migrated.SystemPrompt, "机器人 B\n\n") || !strings.Contains(migrated.SystemPrompt, ReplyStyleGentle.stylePrompt()) || strings.Contains(migrated.SystemPrompt, ReplyStyleLively.stylePrompt()) {
		t.Fatal("group did not preserve its own identity and override its inherited style")
	}
}

func TestLegacyPersonaImportIsIdempotent(t *testing.T) {
	legacy := Persona{Name: "猫娘", ReplyStyle: ReplyStyleCatgirl, ActionDescriptionEnabled: boolPointer(true)}
	set, saved, err := (PersonaSet{}).Save(legacy, time.Now())
	if err != nil || saved.ReplyStyle != "" || strings.Contains(saved.SystemPrompt, catgirlNoActionRule) {
		t.Fatalf("legacy import failed or conflicts with action toggle: %v", err)
	}
	if again := set.WithDefaults(); again.Personas[0].SystemPrompt != saved.SystemPrompt {
		t.Fatal("normalizing library duplicated persona content")
	}
	long := Persona{Name: "长人设", SystemPrompt: strings.Repeat("字", personaPromptMaxRunes), ReplyStyle: ReplyStyleHuman}
	if first := long.Normalized(); first.Normalized().SystemPrompt != first.SystemPrompt {
		t.Fatal("normalizing a long persona changed its content again")
	}
}

// 继承来的人设里那段旧风格模板，不能只靠逐字节后缀来摘。
//
// 追加进去的文案会落到用户手上的编辑框里，改一个标点就再也匹配不上；模板本身也
// 会随版本改写，老配置里存的是上一版的正文。两种情况都会让群人设叠出两段
// 「默认表达风格为……」——线上真的见到过。
func TestInheritedPersonaStripsEditedAndStackedStyleTemplates(t *testing.T) {
	const base = "你叫嘉然，喜欢音乐。"
	template := func(style ReplyStyle) string {
		return strings.ReplaceAll(style.stylePrompt(), catgirlNoActionRule+"\n", "")
	}
	edited := strings.Split(template(ReplyStyleHuman), "\n")
	edited[1] += "（这句是我自己加的）"
	editedHuman := strings.Join(edited, "\n")

	// 逐字节没动过：老路径照旧。
	if got := inheritedPersonaForStyleMigration(base + "\n\n" + template(ReplyStyleGentle)); got != base {
		t.Fatalf("原样追加的模板没被摘掉：%q", got)
	}
	// 正文被改过一个字：首行还在，照样要摘掉。
	if got := inheritedPersonaForStyleMigration(base + "\n\n" + editedHuman); got != base {
		t.Fatalf("改过正文就摘不掉了：%q", got)
	}
	// 已经叠了两段的老配置：一趟清干净，否则修完还是两段。
	for name, stacked := range map[string]string{
		"改过的在里、原样的在外": base + "\n\n" + editedHuman + "\n\n" + template(ReplyStyleCatgirl),
		"两段都原样":       base + "\n\n" + template(ReplyStyleLively) + "\n\n" + template(ReplyStyleConcise),
	} {
		if got := inheritedPersonaForStyleMigration(stacked); got != base {
			t.Fatalf("%s 的两段模板没清干净：%q", name, got)
		}
	}

	// 用户自己写的段落不能误伤，哪怕它谈的也是表达风格。
	own := base + "\n\n默认表达风格为我自己定的那种：想怎么说就怎么说。"
	if got := inheritedPersonaForStyleMigration(own); got != own {
		t.Fatalf("误删了用户自己写的段落：%q", got)
	}
	// 整份人设就是一段模板时宁可原样返回：删干净只会留下空人设。
	if only := template(ReplyStyleConcise); inheritedPersonaForStyleMigration(only) != only {
		t.Fatalf("把整份人设删空了：%q", only)
	}
}
