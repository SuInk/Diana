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
