// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestPersonaTemplateUsesConfiguredNameWithoutMutatingSource(t *testing.T) {
	const source = "# {{name}}\n{{name}}喜欢星空，{{other}}保持原样。"
	cfg := BotConfig{Name: "星河", GroupTriggers: []string{"小助手"}, SystemPrompt: source}
	if got := cfg.personaPrompt(); got != "# 星河\n星河喜欢星空，{{other}}保持原样。" {
		t.Fatal(got)
	}
	oldKey := stickerPersonaKey(cfg)
	cfg.Name = "月光"
	if got := cfg.personaPrompt(); !strings.HasPrefix(got, "# 月光\n月光") {
		t.Fatal(got)
	}
	if stickerPersonaKey(cfg) == oldKey {
		t.Fatal("rename must invalidate the resolved persona cache")
	}
	if cfg.SystemPrompt != source {
		t.Fatal("stored template changed")
	}
	cfg.Name = "{{name}}的朋友"
	if got := cfg.personaPrompt(); !strings.HasPrefix(got, "# {{name}}的朋友\n") {
		t.Fatal(got)
	}
	cfg.Name = " "
	if got := cfg.personaPrompt(); !strings.HasPrefix(got, "# "+DefaultProfileName) {
		t.Fatal(got)
	}
	cfg.SystemPrompt = "固定名字"
	if cfg.personaPrompt() != cfg.SystemPrompt {
		t.Fatal("plain persona changed")
	}
}

func TestPersonaTemplateRuntimeAndResidentPreviewFollowRename(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for _, name := range []string{"星河", "月光"} {
		r.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "bot-a", Name: name, Platform: PlatformOneBotV11, BotAccount: "42", GroupTriggers: []string{"小助手"}, SystemPrompt: "# {{name}}\n{{name}}喜欢星空。"}}})
		prompt := r.systemPrompt(MessageEvent{ProfileID: "bot-a", Kind: EventKindGroup, GroupID: "123"}, nil)
		if !strings.HasPrefix(prompt, "# "+name) || strings.Contains(prompt, "{{name}}") {
			t.Fatalf("unresolved runtime persona: %.200s", prompt)
		}
		snapshot := r.ResidentContextForGroup(context.Background(), "bot-a", "123")
		persona := residentBlock(snapshot, ResidentBlockPersona)
		if persona.Content != "# "+name+"\n"+name+"喜欢星空。" {
			t.Fatal(persona.Content)
		}
		if strings.Contains(residentBlock(snapshot, ResidentBlockPromptRules).Content, "喜欢星空") {
			t.Fatal("persona counted twice")
		}
	}
}

func TestLivePersonaNameTemplate(t *testing.T) {
	client := liveLLMClient(t)
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for _, name := range []string{"星河", "月光"} {
		r.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "bot-a", Name: name, Platform: PlatformOneBotV11, BotAccount: "42", GroupTriggers: []string{"小助手"}, SystemPrompt: "# {{name}}\n{{name}}喜欢星空，说话简短。"}}})
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		resp, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: r.systemPrompt(MessageEvent{ProfileID: "bot-a", Kind: EventKindPrivate}, nil)},
			{Role: llm.RoleUser, Content: "你叫什么名字？只说自己的名字。"},
		}})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp.Text, name) || strings.Contains(resp.Text, "{{name}}") || strings.Contains(resp.Text, "小助手") {
			t.Fatalf("name=%s reply=%s", name, resp.Text)
		}
		t.Logf("name=%s reply=%s", name, resp.Text)
	}
}

func TestPersonaTemplateGroupOverrideAndLibraryPreserveToken(t *testing.T) {
	const source = "# {{name}}\n{{name}}喜欢星空。"
	r := NewRuntime(BotConfig{Name: "星河", SystemPrompt: "机器人默认人设"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"123": {GroupID: "123", SystemPrompt: source}}})
	if prompt := r.systemPrompt(MessageEvent{Kind: EventKindGroup, GroupID: "123"}, nil); !strings.HasPrefix(prompt, "# 星河\n星河喜欢星空。") {
		t.Fatalf("group override unresolved: %.200s", prompt)
	}
	_, result := (PersonaSet{}).Import([]Persona{{Name: "星空人设", SystemPrompt: source}}, time.Now())
	if len(result.Imported) != 1 || result.Imported[0].SystemPrompt != source {
		t.Fatalf("library changed template: %+v", result)
	}
}

func TestDefaultPersonaUsesNameThroughout(t *testing.T) {
	cfg := BotConfig{Name: "星河", SystemPrompt: defaultSystemPrompt}
	if !strings.HasPrefix(defaultSystemPrompt, "# 内置默认") || !strings.Contains(defaultSystemPrompt, "社会情况：{{name}}") {
		t.Fatal("default persona should retain name tokens")
	}
	prompt := cfg.personaPrompt()
	if !strings.Contains(prompt, "社会情况：星河") || strings.Contains(prompt, "Diana") || strings.Contains(prompt, "{{name}}") {
		t.Fatal("default persona should follow the configured identity")
	}
}

func TestEveryBuiltinPersonaUsesNameVariable(t *testing.T) {
	wantNames := []string{"内置默认", "真人感", "猫娘", "助手", "女友", "男友"}
	personas := BuiltinPersonas()
	if len(personas) != len(wantNames) {
		t.Fatal("unexpected builtin catalog size")
	}
	for i, persona := range personas {
		if persona.Name != wantNames[i] {
			t.Fatalf("unexpected label: %q", persona.Name)
		}
		if !strings.HasPrefix(persona.SystemPrompt, "# "+wantNames[i]+"\n") || !strings.Contains(persona.SystemPrompt, "名字：{{name}}") {
			t.Fatalf("%s missing name token", persona.Name)
		}
		cfg := BotConfig{Name: "星河", SystemPrompt: persona.SystemPrompt}
		if !strings.HasPrefix(cfg.personaPrompt(), "# "+wantNames[i]+"\n") || !strings.Contains(cfg.personaPrompt(), "名字：星河") {
			t.Fatalf("%s did not resolve configured name", persona.Name)
		}
	}
}
