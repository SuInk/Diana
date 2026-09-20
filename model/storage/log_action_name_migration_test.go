// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
)

func TestLogActionNameMigrationFlattensLegacyNames(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "action-prefix.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	want := map[string]string{
		"diana.proactive_reply_route": "proactive_reply_route",
		"diana.memory.retrieved":      "memory_retrieved",
		"chatbot.agent_run":           "agent_run",
		"qqbot.llm_usage":             "llm_usage",
		"assistant.llm_usage":         "llm_usage",
		"assistant.image.generate":    "image_generate",
		"assistant.config.save":       "config_save",
		"llm_models_list":             "llm_models_list",
		"system_update_pull":          "system_update_pull",
		"image_generate":              "image_generate",
		// 只是以 diana 开头，不是 diana. 前缀：只换点，不去前缀。
		"dianax.custom": "dianax_custom",
		"llm_usage":     "llm_usage",
	}
	for action := range want {
		if err := store.AppendLog(ctx, applog.Entry{Action: action, Target: action}); err != nil {
			t.Fatal(err)
		}
	}
	rerunLogActionNameMigration(t, store)

	rows, err := store.db.QueryContext(ctx, `SELECT target, action FROM app_logs`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var mismatches []string
	seen := 0
	for rows.Next() {
		var original, action string
		if err := rows.Scan(&original, &action); err != nil {
			t.Fatal(err)
		}
		seen++
		if want[original] != action {
			mismatches = append(mismatches, original+" -> "+action+", want "+want[original])
		}
	}
	sort.Strings(mismatches)
	if seen != len(want) || len(mismatches) > 0 {
		t.Fatalf("rows=%d mismatches=%v", seen, mismatches)
	}
}

// 老库重新打开时启动检查不能把迁移标成已完成；改名做完之前清理要按兵不动，否则旧
// 名字的用量记录会被当普通日志删掉。
func TestPruneLogsWaitsForLogActionNameMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "prune-waits.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -90)
	for _, action := range []string{"diana.llm_usage", "diana.agent_run"} {
		if err := store.AppendLog(ctx, applog.Entry{Action: action, Target: action, CreatedAt: old}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`DELETE FROM app_state WHERE key = ?`, logActionNamesMigrationKey); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if done, err := store.logActionNamesMigrated(ctx); err != nil || done {
		t.Fatalf("startup marked a database with legacy rows as migrated: done=%v err=%v", done, err)
	}
	cutoff := time.Now().AddDate(0, 0, -30)
	if deleted, err := store.PruneLogs(ctx, cutoff, cutoff); err != nil || deleted != 0 {
		t.Fatalf("prune before migration deleted=%d err=%v", deleted, err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if done, err := store.MigrateLogActionNames(canceled); done || err == nil {
		t.Fatalf("canceled migration done=%v err=%v", done, err)
	}
	if done, err := store.MigrateLogActionNames(ctx); err != nil || !done {
		t.Fatalf("resumed migration done=%v err=%v", done, err)
	}
	// 迁移后 llm_usage 按新名字豁免，agent_run 过期被删。
	if deleted, err := store.PruneLogs(ctx, cutoff, cutoff); err != nil || deleted != 1 {
		t.Fatalf("prune after migration deleted=%d err=%v", deleted, err)
	}
	var remaining string
	if err := store.db.QueryRow(`SELECT action FROM app_logs`).Scan(&remaining); err != nil || remaining != "llm_usage" {
		t.Fatalf("remaining action=%q err=%v", remaining, err)
	}
}

// 工具改名后日志动作名跟着改，历史行要一起补齐：库里留着旧名字，统计同一个工具就得
// 把每种写法都列一遍。这里同时盯住两步的先后——带前缀的 diana.repository_issue 要先
// 被拍平成 repository_issue，才轮得到按对照表改成 github，顺序反了就会漏掉这一批。
func TestLogActionNameMigrationRenamesRetiredActions(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "action-rename.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	want := map[string]string{
		"diana.repository_issue": "github",
		"repository_issue":       "github",
		"github":                 "github",
		// 同名前缀不能被顺手改掉：订阅是另一个工具。
		"repository_watch_create": "repository_watch_create",
		"agent_tool":              "agent_tool",
	}
	for action := range want {
		if err := store.AppendLog(ctx, applog.Entry{Action: action, Target: action}); err != nil {
			t.Fatal(err)
		}
	}
	rerunLogActionNameMigration(t, store)
	// 再跑一遍必须没有额外改动：改名是幂等的，维护协程会反复调用。
	if done, err := store.MigrateLogActionNames(ctx); err != nil || !done {
		t.Fatalf("second run done=%v err=%v", done, err)
	}

	rows, err := store.db.QueryContext(ctx, `SELECT target, action FROM app_logs`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var mismatches []string
	seen := 0
	for rows.Next() {
		var original, action string
		if err := rows.Scan(&original, &action); err != nil {
			t.Fatal(err)
		}
		seen++
		if want[original] != action {
			mismatches = append(mismatches, original+" -> "+action+", want "+want[original])
		}
	}
	sort.Strings(mismatches)
	if seen != len(want) || len(mismatches) > 0 {
		t.Fatalf("rows=%d mismatches=%v", seen, mismatches)
	}
}
