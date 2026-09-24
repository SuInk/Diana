// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/storage"
)

// startedStores 是一次启动后按 main.go 的顺序建好的两份存储。
type startedStores struct {
	llm *PersistentLLMProfileStore
	bot *PersistentBotProfileStore
}

// restartWithSeeds 模拟一次进程重启：重新打开同一个数据库，按 main.go 的顺序建提供商
// 存储、修旧引用、再建机器人存储。
func restartWithSeeds(t *testing.T, path string, llmSeed llm.ProviderConfig, botSeed assistant.BotConfig) startedStores {
	t.Helper()
	ctx := context.Background()
	db, err := storage.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	llmStore, err := NewPersistentLLMProfileStore(ctx, db, llmSeed)
	if err != nil {
		t.Fatal(err)
	}
	if err := RepairSeedLLMProfileRefs(ctx, db, llmStore.SeedProfileID()); err != nil {
		t.Fatal(err)
	}
	botStore, err := NewPersistentBotProfileStore(ctx, db, botSeed)
	if err != nil {
		t.Fatal(err)
	}
	return startedStores{llm: llmStore, bot: botStore}
}

func testLLMSeed(model string) llm.ProviderConfig {
	return llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, BaseURL: "https://llm.example.test/v1", APIKey: "test-key", Model: model}
}

func onlyLLMProfile(t *testing.T, store *PersistentLLMProfileStore) llm.Profile {
	t.Helper()
	profiles := store.Profiles().Profiles
	if len(profiles) != 1 {
		t.Fatalf("应该只有一份提供商配置，实际 %d 份", len(profiles))
	}
	return profiles[0]
}

func registryProviderIDs(t *testing.T, store *PersistentLLMProfileStore) []string {
	t.Helper()
	registry, err := store.ProviderRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, provider := range registry.Document().Providers {
		ids = append(ids, provider.ID)
	}
	return ids
}

// 只靠 config.yaml 播种的提供商配置，重启不换 ID；config.yaml 改了模型，重启后
// 生效、ID 不变，注册表也跟着同一个 ID 和新配置走。
func TestSeedOnlyLLMProfileIDStableAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	first := restartWithSeeds(t, path, testLLMSeed("model-a"), assistant.BotConfig{})
	id := onlyLLMProfile(t, first.llm).ID
	if id == "" || first.llm.SeedProfileID() != id {
		t.Fatalf("种子 ID = %q, SeedProfileID = %q", id, first.llm.SeedProfileID())
	}

	second := restartWithSeeds(t, path, testLLMSeed("model-a"), assistant.BotConfig{})
	if got := onlyLLMProfile(t, second.llm).ID; got != id {
		t.Fatalf("第二次启动换了 ID：%s → %s", id, got)
	}

	third := restartWithSeeds(t, path, testLLMSeed("model-b"), assistant.BotConfig{})
	profile := onlyLLMProfile(t, third.llm)
	if profile.ID != id || profile.Config.Model != "model-b" {
		t.Fatalf("改了 config.yaml 后：id=%s（原 %s）model=%s", profile.ID, id, profile.Config.Model)
	}
	if got := registryProviderIDs(t, third.llm); len(got) != 1 || got[0] != id {
		t.Fatalf("注册表还停在旧 ID 上：%v", got)
	}
}

// WebUI 保存之后以库里为准，ID 仍是播种时那一个。
func TestSeedLLMProfileIDSurvivesWebUISave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	first := restartWithSeeds(t, path, testLLMSeed("model-a"), assistant.BotConfig{})
	set := first.llm.Profiles()
	id := set.Profiles[0].ID
	set.Profiles[0].Config.Model = "saved-in-webui"
	if err := first.llm.SaveProfiles(set); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		reopened := restartWithSeeds(t, path, testLLMSeed("from-config"), assistant.BotConfig{})
		profile := onlyLLMProfile(t, reopened.llm)
		if profile.ID != id || profile.Config.Model != "saved-in-webui" {
			t.Fatalf("第 %d 次重启：id=%s（原 %s）model=%s", round+1, profile.ID, id, profile.Config.Model)
		}
		if reopened.llm.SeedProfileID() != "" {
			t.Fatal("保存过之后不该再当成播种的配置集")
		}
	}
}

// 几份提供商配置各自的 ID 和配置对得上，重启不串号。
func TestMultipleLLMProfilesKeepTheirIDsAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	first := restartWithSeeds(t, path, testLLMSeed("model-a"), assistant.BotConfig{})
	set := first.llm.Profiles()
	set.Profiles = append(set.Profiles, llm.Profile{Name: "第二家", Config: testLLMSeed("model-b")})
	if err := first.llm.SaveProfiles(set); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, profile := range first.llm.Profiles().Profiles {
		want[profile.ID] = profile.Config.Model
	}
	if len(want) != 2 {
		t.Fatalf("保存后的配置不对：%v", want)
	}
	for round := 0; round < 2; round++ {
		reopened := restartWithSeeds(t, path, testLLMSeed("from-config"), assistant.BotConfig{})
		got := map[string]string{}
		for _, profile := range reopened.llm.Profiles().Profiles {
			got[profile.ID] = profile.Config.Model
		}
		if len(got) != len(want) {
			t.Fatalf("第 %d 次重启数量不对：%v", round+1, got)
		}
		for id, model := range want {
			if got[id] != model {
				t.Fatalf("第 %d 次重启串号了：want %v, got %v", round+1, want, got)
			}
		}
	}
}

// 机器人在 WebUI 里绑了播种的提供商，重启后绑定仍然指向一份存在的配置。
func TestBotModelRolesSurviveRestartWithSeededLLMProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	first := restartWithSeeds(t, path, testLLMSeed("model-a"), assistant.BotConfig{Name: "种子"})
	llmID := onlyLLMProfile(t, first.llm).ID
	bot := onlyProfile(t, first.bot)
	bot.ModelRoles = map[string]assistant.ModelRole{
		"chat":          {ProfileID: llmID, Model: "model-a"},
		llm.GroupVision: {ProviderID: llmID, ModelID: llmID + ":model-a"},
	}
	if err := first.bot.SaveProfileConfig(bot); err != nil {
		t.Fatal(err)
	}

	for round := 0; round < 2; round++ {
		reopened := restartWithSeeds(t, path, testLLMSeed("model-a"), assistant.BotConfig{Name: "种子"})
		current := onlyLLMProfile(t, reopened.llm).ID
		roles := onlyProfile(t, reopened.bot).ModelRoles
		if roles["chat"].ProfileID != current || roles[llm.GroupVision].ProviderID != current {
			t.Fatalf("第 %d 次重启后绑定指向不存在的配置：current=%s roles=%#v", round+1, current, roles)
		}
	}
}

// 修复前已经漂移的数据：提供商从没保存过，机器人绑着以前某几次启动的旧 ID。升级后
// 接着用注册表里记着的第一次启动的 ID，绑定、备用路线和回复规则全部改回来。
func TestSeedOnlyUpgradeRepointsDriftedLLMRefs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := storage.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	registry, _, err := llm.NewProviderRegistryFromProfiles(llm.ProfileSet{Profiles: []llm.Profile{{ID: "boot-1", Config: testLLMSeed("model-a")}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLLMProviderRegistry(ctx, registry.Document()); err != nil {
		t.Fatal(err)
	}
	drifted := assistant.BotConfig{
		ID:   "bot",
		Name: "WebUI 保存过的机器人",
		ModelRoles: map[string]assistant.ModelRole{
			"chat":          {ProfileID: "boot-2", Model: "model-a", Fallbacks: []assistant.ModelRole{{ProfileID: "boot-3", Model: "model-a"}}},
			llm.GroupVision: {ProviderID: "boot-3", ModelID: "boot-3:model-a"},
			llm.GroupIntent: {Group: llm.GroupIntent, Model: "model-a"},
		},
		ReplyRules: []assistant.ReplyRule{{ID: "r1", Enabled: true, Prompt: "x", LLMProfileID: "boot-2"}},
	}
	if err := db.SaveBotProfiles(ctx, assistant.ProfileSet{Profiles: []assistant.BotConfig{drifted}}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	started := restartWithSeeds(t, path, testLLMSeed("model-a"), assistant.BotConfig{})
	if got := onlyLLMProfile(t, started.llm).ID; got != "boot-1" {
		t.Fatalf("应该接着用注册表里的 boot-1，实际 %s", got)
	}
	var bot assistant.BotConfig
	for _, profile := range started.bot.Profiles().Profiles {
		if profile.ID == "bot" {
			bot = profile
		}
	}
	chat, tool := bot.ModelRoles["chat"], bot.ModelRoles[llm.GroupVision]
	if chat.ProfileID != "boot-1" || len(chat.Fallbacks) != 1 || chat.Fallbacks[0].ProfileID != "boot-1" {
		t.Fatalf("chat 绑定没改回来：%#v", chat)
	}
	if tool.ProviderID != "boot-1" || tool.ModelID != "boot-1:model-a" {
		t.Fatalf("vision 绑定没改回来：%#v", tool)
	}
	if intent := bot.ModelRoles[llm.GroupIntent]; intent.Group != llm.GroupIntent || intent.ProfileID != "" {
		t.Fatalf("按分组绑定的不该被改：%#v", intent)
	}
	if len(bot.ReplyRules) != 1 || bot.ReplyRules[0].LLMProfileID != "boot-1" {
		t.Fatalf("回复规则没改回来：%#v", bot.ReplyRules)
	}
}

// 提供商配置集保存过的库不改：对不上的 ID 可能属于已经删掉的配置档，改指到别家
// 提供商比报错更糟。
func TestSavedLLMProfilesLeaveDanglingRefsAlone(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := storage.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLLMProfiles(ctx, llm.ProfileSet{Profiles: []llm.Profile{{ID: "kept", Config: testLLMSeed("model-a")}}}); err != nil {
		t.Fatal(err)
	}
	bot := assistant.BotConfig{ID: "bot", ModelRoles: map[string]assistant.ModelRole{"chat": {ProfileID: "deleted", Model: "model-a"}}}
	if err := db.SaveBotProfiles(ctx, assistant.ProfileSet{Profiles: []assistant.BotConfig{bot}}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	started := restartWithSeeds(t, path, testLLMSeed("model-a"), assistant.BotConfig{})
	if started.llm.SeedProfileID() != "" {
		t.Fatal("保存过的配置集不该当成播种的")
	}
	if got := onlyProfile(t, started.bot).ModelRoles["chat"].ProfileID; got != "deleted" {
		t.Fatalf("保存过的库里的悬空绑定被改了：%s", got)
	}
}
