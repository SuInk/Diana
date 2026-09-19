package webui

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

// 聊天里「群 禁用」只落到下指令的那台机器人，重启后仍在，别的机器人不受影响。
func TestSaveDisabledGroupsPersistsPerProfile(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := NewPersistentBotProfileStore(ctx, db, assistant.BotConfig{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveProfiles(assistant.ProfileSet{ActiveID: "a", Profiles: []assistant.BotConfig{{ID: "a"}, {ID: "b"}}}); err != nil {
		t.Fatal(err)
	}
	if err = NewRuntimePersistor(s).SaveDisabledGroups("b", []string{"100"}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewPersistentBotProfileStore(ctx, db, assistant.BotConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range reopened.Profiles().Profiles {
		disabled := slices.Contains(profile.DisabledGroups, "100")
		if disabled != (profile.ID == "b") {
			t.Fatalf("机器人 %s 的禁用群 = %v", profile.ID, profile.DisabledGroups)
		}
	}
	if err = NewRuntimePersistor(s).SaveDisabledGroups("missing", nil); err == nil {
		t.Fatal("不存在的机器人也保存成功了")
	}
}
