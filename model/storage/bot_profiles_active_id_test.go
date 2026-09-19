// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// 旧库里配置集带着 active_id：重新打开时删掉这个键，机器人配置原样保留。
func TestOpeningStoreDropsLegacyBotProfilesActiveID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `{"active_id":"tg","profiles":[{"id":"qq","name":"QQ","platform":"onebot-v11"},{"id":"tg","name":"TG","platform":"telegram"}]}`
	if _, err := store.db.ExecContext(context.Background(), `INSERT OR REPLACE INTO app_state(key, value) VALUES(?, ?)`, botProfilesKey, legacy); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var raw string
	if err := reopened.db.QueryRowContext(context.Background(), `SELECT value FROM app_state WHERE key = ?`, botProfilesKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "active_id") {
		t.Fatalf("active_id 没有删掉：%s", raw)
	}
	set, ok, err := reopened.LoadBotProfiles(context.Background())
	if err != nil || !ok || len(set.Profiles) != 2 || set.Profiles[0].ID != "qq" || set.Profiles[1].Name != "TG" {
		t.Fatalf("机器人配置被改动：%+v ok=%v err=%v", set, ok, err)
	}
}
