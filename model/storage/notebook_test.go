// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func newNotebookStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "notebook.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func upsertNotebook(t *testing.T, store *SQLiteStore, request assistant.NotebookUpsertRequest) assistant.NotebookEntry {
	t.Helper()
	entry, _, err := store.UpsertNotebookEntry(context.Background(), request)
	if err != nil {
		t.Fatalf("upsert %q: %v", request.Term, err)
	}
	return entry
}

// 笔记本的重点不是「能写进去」，而是「能改」：同一个词第二次写入必须是修订，
// 版本递增、旧释义进修订史，而不是攒出第二条互相矛盾的解释。
func TestNotebookUpsertRevisesInsteadOfDuplicating(t *testing.T) {
	ctx := context.Background()
	store := newNotebookStore(t)
	base := time.Unix(1700000000, 0)

	first, created, err := store.UpsertNotebookEntry(ctx, assistant.NotebookUpsertRequest{
		ScopeKey: "group:1", Term: "typo姐", Meaning: "群里打错字最多的人，调侃用",
		EditorUserID: "10001", EditorName: "Alice", Now: base,
	})
	if err != nil || !created {
		t.Fatalf("first upsert created=%v err=%v", created, err)
	}
	if first.Version != 1 || first.Status != assistant.NotebookStatusActive {
		t.Fatalf("first = %+v", first)
	}

	second, created, err := store.UpsertNotebookEntry(ctx, assistant.NotebookUpsertRequest{
		ScopeKey: "group:1", Term: "Typo姐", Meaning: "现在成了褒义，指眼神好会挑错的人",
		Note: "用法变了，从调侃变成夸人", EditorUserID: "10002", EditorName: "Bob", Now: base.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("大小写不同的同一个词不该被当成新条目")
	}
	if second.Version != 2 {
		t.Fatalf("version = %d, want 2", second.Version)
	}
	if second.EditorUserID != "10002" || second.AuthorUserID != "10001" {
		t.Fatalf("作者应保持首次记录人，编辑者应更新: %+v", second)
	}

	entries, err := store.ListNotebookEntries(ctx, assistant.NotebookQuery{ScopeKeys: []string{"group:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}

	detail, found, err := store.NotebookEntryDetail(ctx, "group:1", "typo姐")
	if err != nil || !found {
		t.Fatalf("detail found=%v err=%v", found, err)
	}
	if len(detail.Revisions) != 2 {
		t.Fatalf("revisions = %d, want 2", len(detail.Revisions))
	}
	if detail.Revisions[0].Version != 2 || detail.Revisions[0].Note != "更新：用法变了，从调侃变成夸人" {
		t.Fatalf("latest revision = %+v", detail.Revisions[0])
	}
	if detail.Revisions[1].Meaning != "群里打错字最多的人，调侃用" {
		t.Fatalf("旧释义没有留在修订史里: %+v", detail.Revisions[1])
	}
}

// 自动命中靠「这段话里出现了哪个条目」，别名和条目本身一样要能命中。
func TestNotebookLookupMatchesTermsAndAliases(t *testing.T) {
	ctx := context.Background()
	store := newNotebookStore(t)
	now := time.Unix(1700000000, 0)

	upsertNotebook(t, store, assistant.NotebookUpsertRequest{
		ScopeKey: "group:1", Term: "带薪拉屎", Aliases: []string{"DXLS", "带薪"},
		Meaning: "上班时间摸鱼", Now: now,
	})
	upsertNotebook(t, store, assistant.NotebookUpsertRequest{
		ScopeKey: "group:1", Term: "无关词", Meaning: "不该被命中", Now: now,
	})

	hits, err := store.LookupNotebookEntries(ctx, assistant.NotebookQuery{
		ScopeKeys: []string{"group:1", assistant.NotebookScopeGlobal},
		Text:      "今天又 dxls 了半小时",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Term != "带薪拉屎" {
		t.Fatalf("hits = %+v", hits)
	}

	hits, err = store.LookupNotebookEntries(ctx, assistant.NotebookQuery{
		ScopeKeys: []string{"group:1"}, Terms: []string{"带薪拉屎"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("按词精确检索失败: %+v", hits)
	}
}

// 同一个词在本群和全局都有释义时，本群那条说了算：全局笔记本是兜底，不是覆盖。
func TestNotebookLookupPrefersSessionScope(t *testing.T) {
	ctx := context.Background()
	store := newNotebookStore(t)
	now := time.Unix(1700000000, 0)

	upsertNotebook(t, store, assistant.NotebookUpsertRequest{
		ScopeKey: assistant.NotebookScopeGlobal, Term: "鸽", Meaning: "放人鸽子", Now: now,
	})
	upsertNotebook(t, store, assistant.NotebookUpsertRequest{
		ScopeKey: "group:1", Term: "鸽", Meaning: "本群指某位群友的头像", Now: now,
	})

	hits, err := store.LookupNotebookEntries(ctx, assistant.NotebookQuery{
		ScopeKeys: []string{"group:1", assistant.NotebookScopeGlobal},
		Text:      "他又鸽了",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].ScopeKey != "group:1" {
		t.Fatalf("本群释义应排在全局之前: %+v", hits)
	}
}

// 删除是软删除：命中不到、列表默认看不见，但修订史还在，restore 能原地救回来。
func TestNotebookDeleteIsReversible(t *testing.T) {
	ctx := context.Background()
	store := newNotebookStore(t)
	now := time.Unix(1700000000, 0)

	upsertNotebook(t, store, assistant.NotebookUpsertRequest{
		ScopeKey: "group:1", Term: "老梗", Meaning: "已经没人用了", Now: now,
	})
	deleted, found, err := store.DeleteNotebookEntry(ctx, "group:1", "老梗", "10001", "Alice", "过时了", now.Add(time.Hour))
	if err != nil || !found {
		t.Fatalf("delete found=%v err=%v", found, err)
	}
	if deleted.Status != assistant.NotebookStatusDeleted || deleted.Version != 2 {
		t.Fatalf("deleted = %+v", deleted)
	}

	hits, err := store.LookupNotebookEntries(ctx, assistant.NotebookQuery{
		ScopeKeys: []string{"group:1"}, Text: "还有人说老梗吗",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("作废的条目不该再自动命中: %+v", hits)
	}

	listed, err := store.ListNotebookEntries(ctx, assistant.NotebookQuery{ScopeKeys: []string{"group:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("默认列表不该带作废条目: %+v", listed)
	}
	listed, err = store.ListNotebookEntries(ctx, assistant.NotebookQuery{ScopeKeys: []string{"group:1"}, IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("include_deleted 应带出作废条目: %+v", listed)
	}

	restored, found, err := store.RestoreNotebookEntry(ctx, "group:1", "老梗", "10001", "Alice", now.Add(2*time.Hour))
	if err != nil || !found {
		t.Fatalf("restore found=%v err=%v", found, err)
	}
	if restored.Status != assistant.NotebookStatusActive || restored.Meaning != "已经没人用了" {
		t.Fatalf("restored = %+v", restored)
	}
	detail, _, err := store.NotebookEntryDetail(ctx, "group:1", "老梗")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Revisions) != 3 {
		t.Fatalf("新建、作废、恢复都该留痕: %+v", detail.Revisions)
	}
}

// 作废过的词又被重新解释时应当复活成同一条，而不是另起一条——修订史是同一条线索。
func TestNotebookUpsertRevivesDeletedEntry(t *testing.T) {
	ctx := context.Background()
	store := newNotebookStore(t)
	now := time.Unix(1700000000, 0)

	upsertNotebook(t, store, assistant.NotebookUpsertRequest{
		ScopeKey: "group:1", Term: "梗", Meaning: "旧释义", Now: now,
	})
	if _, _, err := store.DeleteNotebookEntry(ctx, "group:1", "梗", "10001", "Alice", "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	entry, created, err := store.UpsertNotebookEntry(ctx, assistant.NotebookUpsertRequest{
		ScopeKey: "group:1", Term: "梗", Meaning: "又有人用起来了", Now: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("复活不该被报告成新建")
	}
	if entry.Status != assistant.NotebookStatusActive || entry.Version != 3 {
		t.Fatalf("entry = %+v", entry)
	}
}

// 命中要能回写：用得多的排前面，长期没人用的自然沉底。
func TestNotebookTouchOrdersByUsage(t *testing.T) {
	ctx := context.Background()
	store := newNotebookStore(t)
	now := time.Unix(1700000000, 0)

	cold := upsertNotebook(t, store, assistant.NotebookUpsertRequest{
		ScopeKey: "group:1", Term: "冷词", Meaning: "冷", Now: now,
	})
	hot := upsertNotebook(t, store, assistant.NotebookUpsertRequest{
		ScopeKey: "group:1", Term: "热词", Meaning: "热", Now: now.Add(time.Minute),
	})
	// 一次命中记一次；同一条条目在两轮对话里各命中一次才是两次。
	for _, at := range []time.Time{now.Add(time.Hour), now.Add(2 * time.Hour)} {
		if err := store.TouchNotebookEntries(ctx, []string{hot.ID}, at); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.ListNotebookEntries(ctx, assistant.NotebookQuery{ScopeKeys: []string{"group:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != hot.ID {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].UsageCount != 2 {
		t.Fatalf("usage = %d, want 2", entries[0].UsageCount)
	}
	if entries[1].ID != cold.ID || entries[1].UsageCount != 0 {
		t.Fatalf("cold = %+v", entries[1])
	}
	if entries[0].LastUsedAt.IsZero() {
		t.Fatal("命中时间没有回写")
	}
}

// 老库升级：那本所有机器人共用的全局笔记本归给迁移时的当前配置档，其余机器人从
// 空本开始——和画像、群配置一样，不复制。
func TestNotebookGlobalScopeMigratesToCurrentBot(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "notebook-migrate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.SaveBotProfiles(ctx, assistant.ProfileSet{
		ActiveID: "bot-onebot",
		Profiles: []assistant.BotConfig{{ID: "bot-onebot", Name: "OneBot"}, {ID: "bot-telegram", Name: "Telegram"}},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, _, err := store.UpsertNotebookEntry(ctx, assistant.NotebookUpsertRequest{
		ScopeKey: assistant.NotebookScopeGlobal, Term: "老梗", Meaning: "升级前记下的", Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateNotebookGlobalScopeToBot(); err != nil {
		t.Fatal(err)
	}

	owned, err := store.ListNotebookEntries(ctx, assistant.NotebookQuery{
		ScopeKeys: []string{assistant.NotebookScopeBotPrefix + "bot-onebot"},
	})
	if err != nil || len(owned) != 1 || owned[0].Term != "老梗" {
		t.Fatalf("当前档没有继承共用笔记本: %+v err=%v", owned, err)
	}
	other, err := store.ListNotebookEntries(ctx, assistant.NotebookQuery{
		ScopeKeys: []string{assistant.NotebookScopeBotPrefix + "bot-telegram"},
	})
	if err != nil || len(other) != 0 {
		t.Fatalf("另一台机器人不该凭空拿到条目: %+v err=%v", other, err)
	}
	// 迁移是幂等的：再跑一次不会把已经搬过去的条目重复处理。
	if err := store.migrateNotebookGlobalScopeToBot(); err != nil {
		t.Fatal(err)
	}
	again, err := store.ListNotebookEntries(ctx, assistant.NotebookQuery{
		ScopeKeys: []string{assistant.NotebookScopeBotPrefix + "bot-onebot"},
	})
	if err != nil || len(again) != 1 {
		t.Fatalf("重复迁移后条目数 = %d err=%v", len(again), err)
	}
}

// 笔记本升级成笔记本是一次改名，不是一次重建：条目、别名、修订史都要跟过来。
// 换个 CREATE TABLE 名字了事的话，老库升级后表现是「笔记本空了」。
func TestMigrateRenamesLegacyGlossaryTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	// 造一个笔记本时代的库：三张 glossary_* 表，各放一行。
	if _, err := db.Exec(`
CREATE TABLE app_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO app_state (key, value)
VALUES ('bot_profiles', '{"active_id":"bot-onebot","profiles":[{"id":"bot-onebot","name":"OneBot"}]}');

CREATE TABLE glossary_entries (
  id TEXT PRIMARY KEY, scope_key TEXT NOT NULL, term TEXT NOT NULL, normalized_term TEXT NOT NULL,
  meaning TEXT NOT NULL, example TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
  aliases TEXT NOT NULL DEFAULT '[]', author_user_id TEXT NOT NULL DEFAULT '', author_name TEXT NOT NULL DEFAULT '',
  editor_user_id TEXT NOT NULL DEFAULT '', editor_name TEXT NOT NULL DEFAULT '',
  source_session TEXT NOT NULL DEFAULT '', source_message_id TEXT NOT NULL DEFAULT '',
  usage_count INTEGER NOT NULL DEFAULT 0, last_used_at INTEGER, version INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL CHECK (status IN ('active', 'deleted')), created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE glossary_aliases (entry_id TEXT NOT NULL, normalized_alias TEXT NOT NULL, PRIMARY KEY (entry_id, normalized_alias));
CREATE TABLE glossary_revisions (
  id TEXT PRIMARY KEY, entry_id TEXT NOT NULL, version INTEGER NOT NULL, action TEXT NOT NULL,
  meaning TEXT NOT NULL DEFAULT '', example TEXT NOT NULL DEFAULT '', aliases TEXT NOT NULL DEFAULT '[]',
  note TEXT NOT NULL DEFAULT '', editor_user_id TEXT NOT NULL DEFAULT '', editor_name TEXT NOT NULL DEFAULT '',
  recorded_at INTEGER NOT NULL
);
INSERT INTO glossary_entries (id, scope_key, term, normalized_term, meaning, status, created_at, updated_at)
VALUES ('e1', 'group:1', '带薪拉屎', '带薪拉屎', '上班摸鱼', 'active', 1, 1);
INSERT INTO glossary_aliases (entry_id, normalized_alias) VALUES ('e1', 'dxls');
INSERT INTO glossary_revisions (id, entry_id, version, action, meaning, recorded_at)
VALUES ('r1', 'e1', 1, 'created', '上班摸鱼', 1);
`); err != nil {
		t.Fatalf("seed legacy schema error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	entry, found, err := store.NotebookEntryDetail(context.Background(), "group:1", "带薪拉屎")
	if err != nil || !found {
		t.Fatalf("NotebookEntryDetail() = %#v, %v, %v", entry, found, err)
	}
	if entry.Meaning != "上班摸鱼" {
		t.Fatalf("entry = %#v", entry)
	}
	// 老数据没有类型，升级后必须落在条目上，读起来和以前一模一样。
	if entry.Kind != assistant.NotebookKindTerm {
		t.Fatalf("legacy entry kind = %q, want term", entry.Kind)
	}
	if len(entry.Revisions) == 0 {
		t.Fatal("修订史没有跟过来")
	}
	// 别名也要跟过来，否则升级之后老条目命不中了。
	entries, err := store.LookupNotebookEntries(context.Background(), assistant.NotebookQuery{
		ScopeKeys: []string{"group:1"}, Text: "今天 dxls 了半小时", Limit: 5,
	})
	if err != nil {
		t.Fatalf("LookupNotebookEntries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("别名没有跟过来，命中结果 = %#v", entries)
	}

	// 迁移必须可重入：再开一次不能报错，也不能把数据搬丢。
	again, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("second NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
	if _, found, err := again.NotebookEntryDetail(context.Background(), "group:1", "带薪拉屎"); err != nil || !found {
		t.Fatalf("reopening lost the entry: %v %v", found, err)
	}
}

// 类型要能存下来、读回来，并且能按类型筛。
func TestNotebookEntryKindRoundTripsAndFilters(t *testing.T) {
	store := newNotebookStore(t)
	ctx := context.Background()
	for _, item := range []struct {
		kind assistant.NotebookKind
		term string
	}{
		{assistant.NotebookKindTerm, "带薪拉屎"},
		{assistant.NotebookKindFact, "群规：十点后不刷屏"},
		{assistant.NotebookKindTodo, "给群里买蛋糕"},
	} {
		if _, _, err := store.UpsertNotebookEntry(ctx, assistant.NotebookUpsertRequest{
			ScopeKey: "group:1", Kind: item.kind, Term: item.term, Meaning: "正文",
		}); err != nil {
			t.Fatalf("UpsertNotebookEntry(%s) error = %v", item.kind, err)
		}
	}

	all, err := store.ListNotebookEntries(ctx, assistant.NotebookQuery{ScopeKeys: []string{"group:1"}, Limit: 20})
	if err != nil {
		t.Fatalf("ListNotebookEntries() error = %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("entries = %d, want 3", len(all))
	}
	kinds := map[assistant.NotebookKind]bool{}
	for _, entry := range all {
		kinds[entry.Kind] = true
	}
	if !kinds[assistant.NotebookKindFact] || !kinds[assistant.NotebookKindTodo] {
		t.Fatalf("kinds did not survive the round trip: %#v", kinds)
	}

	filtered, err := store.ListNotebookEntries(ctx, assistant.NotebookQuery{
		ScopeKeys: []string{"group:1"}, Kinds: []assistant.NotebookKind{assistant.NotebookKindTodo}, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ListNotebookEntries(kinds) error = %v", err)
	}
	if len(filtered) != 1 || filtered[0].Kind != assistant.NotebookKindTodo {
		t.Fatalf("filtered = %#v", filtered)
	}

	// 改类型也是一次修订，版本要涨，旧内容进修订史。
	updated, created, err := store.UpsertNotebookEntry(ctx, assistant.NotebookUpsertRequest{
		ScopeKey: "group:1", Kind: assistant.NotebookKindEvent, Term: "给群里买蛋糕", Meaning: "已经买了",
	})
	if err != nil || created {
		t.Fatalf("UpsertNotebookEntry() = %#v, created=%v, err=%v", updated, created, err)
	}
	if updated.Kind != assistant.NotebookKindEvent || updated.Version != 2 {
		t.Fatalf("updated = %#v", updated)
	}
}
