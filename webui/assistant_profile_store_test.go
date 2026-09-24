// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

// restartBotProfileStore 模拟一次进程重启：重新打开同一个数据库，再按 seed 播种。
func restartBotProfileStore(t *testing.T, path string, seed assistant.BotConfig) (*PersistentBotProfileStore, *storage.SQLiteStore) {
	t.Helper()
	db, err := storage.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewPersistentBotProfileStore(context.Background(), db, seed)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

func onlyProfile(t *testing.T, store *PersistentBotProfileStore) assistant.BotConfig {
	t.Helper()
	profiles := store.Profiles().Profiles
	if len(profiles) != 1 {
		t.Fatalf("应该只有一台机器人，实际 %d 台", len(profiles))
	}
	return profiles[0]
}

// 只靠 config.yaml 播种、从没在 WebUI 保存过的部署，重启不能换档案 ID；改了
// config.yaml 里的机器人参数，重启后照样生效，ID 仍然不变。
func TestSeedOnlyBotProfileIDStableAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	seed := assistant.BotConfig{Name: "种子", OwnerID: "10001"}

	first, _ := restartBotProfileStore(t, path, seed)
	id := onlyProfile(t, first).ID
	if id == "" {
		t.Fatal("种子机器人没有档案 ID")
	}

	second, _ := restartBotProfileStore(t, path, seed)
	if got := onlyProfile(t, second).ID; got != id {
		t.Fatalf("第二次启动换了档案 ID：%s → %s", id, got)
	}

	seed.Name, seed.OwnerID = "改过名的种子", "10002"
	third, _ := restartBotProfileStore(t, path, seed)
	profile := onlyProfile(t, third)
	if profile.ID != id {
		t.Fatalf("改了 config.yaml 后换了档案 ID：%s → %s", id, profile.ID)
	}
	if profile.Name != "改过名的种子" || profile.OwnerID != "10002" {
		t.Fatalf("config.yaml 里改的参数没生效：name=%q owner=%q", profile.Name, profile.OwnerID)
	}
}

// 在 WebUI 保存过之后，数据库里的配置为准，ID 仍是播种时那一个。
func TestSeedBotProfileIDSurvivesWebUISave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	seed := assistant.BotConfig{Name: "种子"}

	first, _ := restartBotProfileStore(t, path, seed)
	profile := onlyProfile(t, first)
	id := profile.ID
	profile.Name = "界面里改的名字"
	if err := first.SaveProfileConfig(profile); err != nil {
		t.Fatal(err)
	}

	for round := 0; round < 2; round++ {
		reopened, _ := restartBotProfileStore(t, path, assistant.BotConfig{Name: "config.yaml 里的名字"})
		got := onlyProfile(t, reopened)
		if got.ID != id {
			t.Fatalf("第 %d 次重启换了档案 ID：%s → %s", round+1, id, got.ID)
		}
		if got.Name != "界面里改的名字" {
			t.Fatalf("WebUI 保存的配置被 config.yaml 盖掉了：%q", got.Name)
		}
	}
}

// 几台机器人各自的 ID 和配置对得上，重启不串号。
func TestMultipleBotProfilesKeepTheirIDsAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	first, _ := restartBotProfileStore(t, path, assistant.BotConfig{Name: "种子", OwnerID: "1"})
	seeded := onlyProfile(t, first)
	set := first.Profiles()
	set.Profiles = append(set.Profiles, assistant.BotConfig{Name: "第二台", OwnerID: "2"})
	if err := first.SaveProfiles(set); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, profile := range first.Profiles().Profiles {
		want[profile.ID] = profile.Name
	}
	if len(want) != 2 || want[seeded.ID] != "种子" {
		t.Fatalf("保存后的机器人不对：%v", want)
	}

	for round := 0; round < 2; round++ {
		reopened, _ := restartBotProfileStore(t, path, assistant.BotConfig{Name: "种子"})
		got := map[string]string{}
		for _, profile := range reopened.Profiles().Profiles {
			got[profile.ID] = profile.Name
		}
		if len(got) != len(want) {
			t.Fatalf("第 %d 次重启后机器人数量不对：%v", round+1, got)
		}
		for id, name := range want {
			if got[id] != name {
				t.Fatalf("第 %d 次重启串号了：want %v, got %v", round+1, want, got)
			}
		}
	}
}

// 修复前的种子部署每次重启都换 ID。升级后第一次启动接着用最近那个，其余记成旧号，
// 用来认领按旧号记下的编码任务。
func TestSeedOnlyUpgradeAdoptsLatestHistoricalProfileID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := storage.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"boot-1", "boot-2", "boot-3"} {
		event := assistant.MessageEvent{ProfileID: id, UserID: "7", MessageID: id, Time: int64(1000 + i), RawMessage: "hi"}
		if err := db.AppendMessageEvent(ctx, "private:7", event); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	store, _ := restartBotProfileStore(t, path, assistant.BotConfig{Name: "种子"})
	if got := onlyProfile(t, store).ID; got != "boot-3" {
		t.Fatalf("应该接着用最近那次启动的 ID boot-3，实际 %s", got)
	}
	aliases := store.LegacyProfileAliases()
	if len(aliases) != 2 || aliases["boot-1"] != "boot-3" || aliases["boot-2"] != "boot-3" {
		t.Fatalf("旧号映射不对：%v", aliases)
	}

	// WebUI 保存之后，旧号映射仍然在：编码任务可能在保存之后才被接回。
	profile := onlyProfile(t, store)
	profile.Name = "保存过"
	if err := store.SaveProfileConfig(profile); err != nil {
		t.Fatal(err)
	}
	reopened, _ := restartBotProfileStore(t, path, assistant.BotConfig{Name: "种子"})
	if got := onlyProfile(t, reopened).ID; got != "boot-3" {
		t.Fatalf("保存后重启换了 ID：%s", got)
	}
	if got := reopened.LegacyProfileAliases(); got["boot-1"] != "boot-3" {
		t.Fatalf("保存后旧号映射丢了：%v", got)
	}
}

// 存过配置集的库不收集历史 ID：那里的旧号可能属于已经删掉的机器人。
func TestSavedProfilesDoNotAdoptHistoricalIDs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := storage.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveBotProfiles(ctx, assistant.ProfileSet{Profiles: []assistant.BotConfig{{ID: "kept", Name: "保存过的"}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendMessageEvent(ctx, "private:7", assistant.MessageEvent{ProfileID: "deleted-bot", UserID: "7", MessageID: "m1", Time: 1000, RawMessage: "hi"}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	store, _ := restartBotProfileStore(t, path, assistant.BotConfig{Name: "种子"})
	if got := onlyProfile(t, store).ID; got != "kept" {
		t.Fatalf("保存过的机器人 ID 变了：%s", got)
	}
	if aliases := store.LegacyProfileAliases(); len(aliases) != 0 {
		t.Fatalf("存过配置集的库不该收集旧号：%v", aliases)
	}
}
