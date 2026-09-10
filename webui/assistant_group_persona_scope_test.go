// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func personaScopeProfiles() (assistant.BotConfig, assistant.BotConfig, stubBotProfileSource) {
	alpha := assistant.BotConfig{
		ID: "bot-alpha", Name: "阿尔法", SystemPrompt: "阿尔法的人设正文",
		GroupTriggers: []string{"阿尔法"}, WelcomeMessage: "阿尔法的欢迎语",
	}
	beta := assistant.BotConfig{
		ID: "bot-beta", Name: "贝塔", SystemPrompt: "贝塔的人设正文",
		GroupTriggers: []string{"贝塔"}, WelcomeMessage: "贝塔的欢迎语",
	}
	return alpha, beta, stubBotProfileSource{set: assistant.ProfileSet{
		ActiveID: alpha.ID,
		Profiles: []assistant.BotConfig{alpha, beta},
	}}
}

func newPersonaScopeStore(t *testing.T, dir string, source BotProfileSource) *PersistentBotGroupConfigStore {
	t.Helper()
	db, err := storage.NewSQLiteStore(filepath.Join(dir, "groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewPersistentBotGroupConfigStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	store.SetProfileSource(source)
	return store
}

// 保存一台机器人的群会顺手归一化整份数据，那一遍不能把当前这台的人设和默认值
// 糊到别人的群上。老库里正好躺着一批没补过默认值的行，这里就照那个样子造。
func TestSaveGroupConfigDoesNotTouchOtherProfileGroups(t *testing.T) {
	alpha, beta, source := personaScopeProfiles()
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SaveBotGroupConfigs(context.Background(), assistant.GroupConfigSet{Groups: []assistant.GroupConfig{
		{BotProfileID: beta.ID, GroupID: "200", Enabled: true, EnabledSet: true, ReplyStyle: assistant.ReplyStyleGentle},
	}}); err != nil {
		t.Fatal(err)
	}
	store, err := NewPersistentBotGroupConfigStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	store.SetProfileSource(source)

	// 保存 alpha 的另一个群，触发全量归一化。
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{BotProfileID: alpha.ID, GroupID: "300"}, alpha); err != nil {
		t.Fatal(err)
	}

	after, ok := store.ConfigForGroup(beta.ID, "200")
	if !ok {
		t.Fatal("beta 的群配置丢了")
	}
	if strings.Contains(after.SystemPrompt, "阿尔法") {
		t.Fatalf("beta 的群被写进了 alpha 的人设：%q", after.SystemPrompt)
	}
	if !strings.Contains(after.SystemPrompt, "贝塔的人设正文") {
		t.Fatalf("beta 的群选过老风格，应从 beta 继承人设：%q", after.SystemPrompt)
	}
	if strings.Join(after.GroupTriggers, ",") != "贝塔" {
		t.Fatalf("beta 的群拿到了别人的触发词：%v", after.GroupTriggers)
	}
	if after.WelcomeMessage != "贝塔的欢迎语" {
		t.Fatalf("beta 的群拿到了别人的欢迎语：%q", after.WelcomeMessage)
	}
	if alphaCfg, ok := store.ConfigForGroup(alpha.ID, "300"); !ok || strings.Join(alphaCfg.GroupTriggers, ",") != "阿尔法" {
		t.Fatalf("alpha 的群应跟随 alpha：%+v", alphaCfg)
	}
}

// 群人设留空表示「跟随所属机器人」，存一遍读回来还得是空的。
func TestEmptyGroupPersonaSurvivesSaveAndReload(t *testing.T) {
	alpha, beta, source := personaScopeProfiles()
	dir := t.TempDir()
	db, err := storage.NewSQLiteStore(filepath.Join(dir, "groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewPersistentBotGroupConfigStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	store.SetProfileSource(source)

	if _, err := store.SaveGroupConfig(assistant.GroupConfig{BotProfileID: beta.ID, GroupID: "200"}, beta); err != nil {
		t.Fatal(err)
	}
	// 再保存 alpha 的群，逼一次全量归一化。
	if _, err := store.SaveGroupConfig(assistant.GroupConfig{BotProfileID: alpha.ID, GroupID: "300"}, alpha); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewPersistentBotGroupConfigStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.SetProfileSource(source)
	cfg, ok := reloaded.ConfigForGroup(beta.ID, "200")
	if !ok {
		t.Fatal("重新加载后找不到 beta 的群配置")
	}
	if cfg.SystemPrompt != "" {
		t.Fatalf("空人设应保持为空以便运行时继承机器人，实际 = %q", cfg.SystemPrompt)
	}
}

// 内存版走同一条路径，也不能把别人的人设抄过来。
func TestMemoryGroupStoreResolvesPersonaPerProfile(t *testing.T) {
	alpha, beta, source := personaScopeProfiles()
	store := NewMemoryBotGroupConfigStore()
	store.SetProfileSource(source)

	saved, err := store.SaveGroupConfig(assistant.GroupConfig{
		BotProfileID: beta.ID, GroupID: "200", ReplyStyle: assistant.ReplyStyleGentle,
	}, alpha)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved.SystemPrompt, "阿尔法") {
		t.Fatalf("beta 的群被写进了 alpha 的人设：%q", saved.SystemPrompt)
	}
	if !strings.Contains(saved.SystemPrompt, "贝塔的人设正文") {
		t.Fatalf("beta 的群应继承 beta 的人设：%q", saved.SystemPrompt)
	}
}
