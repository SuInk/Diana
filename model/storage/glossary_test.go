// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func newGlossaryStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "glossary.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func upsertGlossary(t *testing.T, store *SQLiteStore, request assistant.GlossaryUpsertRequest) assistant.GlossaryEntry {
	t.Helper()
	entry, _, err := store.UpsertGlossaryEntry(context.Background(), request)
	if err != nil {
		t.Fatalf("upsert %q: %v", request.Term, err)
	}
	return entry
}

// 词典的重点不是「能写进去」，而是「能改」：同一个词第二次写入必须是修订，
// 版本递增、旧释义进修订史，而不是攒出第二条互相矛盾的解释。
func TestGlossaryUpsertRevisesInsteadOfDuplicating(t *testing.T) {
	ctx := context.Background()
	store := newGlossaryStore(t)
	base := time.Unix(1700000000, 0)

	first, created, err := store.UpsertGlossaryEntry(ctx, assistant.GlossaryUpsertRequest{
		ScopeKey: "group:1", Term: "typo姐", Meaning: "群里打错字最多的人，调侃用",
		EditorUserID: "10001", EditorName: "Alice", Now: base,
	})
	if err != nil || !created {
		t.Fatalf("first upsert created=%v err=%v", created, err)
	}
	if first.Version != 1 || first.Status != assistant.GlossaryStatusActive {
		t.Fatalf("first = %+v", first)
	}

	second, created, err := store.UpsertGlossaryEntry(ctx, assistant.GlossaryUpsertRequest{
		ScopeKey: "group:1", Term: "Typo姐", Meaning: "现在成了褒义，指眼神好会挑错的人",
		Note: "用法变了，从调侃变成夸人", EditorUserID: "10002", EditorName: "Bob", Now: base.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("大小写不同的同一个词不该被当成新词条")
	}
	if second.Version != 2 {
		t.Fatalf("version = %d, want 2", second.Version)
	}
	if second.EditorUserID != "10002" || second.AuthorUserID != "10001" {
		t.Fatalf("作者应保持首次记录人，编辑者应更新: %+v", second)
	}

	entries, err := store.ListGlossaryEntries(ctx, assistant.GlossaryQuery{ScopeKeys: []string{"group:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}

	detail, found, err := store.GlossaryEntryDetail(ctx, "group:1", "typo姐")
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

// 自动命中靠「这段话里出现了哪个词条」，别名和词条本身一样要能命中。
func TestGlossaryLookupMatchesTermsAndAliases(t *testing.T) {
	ctx := context.Background()
	store := newGlossaryStore(t)
	now := time.Unix(1700000000, 0)

	upsertGlossary(t, store, assistant.GlossaryUpsertRequest{
		ScopeKey: "group:1", Term: "带薪拉屎", Aliases: []string{"DXLS", "带薪"},
		Meaning: "上班时间摸鱼", Now: now,
	})
	upsertGlossary(t, store, assistant.GlossaryUpsertRequest{
		ScopeKey: "group:1", Term: "无关词", Meaning: "不该被命中", Now: now,
	})

	hits, err := store.LookupGlossaryEntries(ctx, assistant.GlossaryQuery{
		ScopeKeys: []string{"group:1", assistant.GlossaryScopeGlobal},
		Text:      "今天又 dxls 了半小时",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Term != "带薪拉屎" {
		t.Fatalf("hits = %+v", hits)
	}

	hits, err = store.LookupGlossaryEntries(ctx, assistant.GlossaryQuery{
		ScopeKeys: []string{"group:1"}, Terms: []string{"带薪拉屎"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("按词精确检索失败: %+v", hits)
	}
}

// 同一个词在本群和全局都有释义时，本群那条说了算：全局词典是兜底，不是覆盖。
func TestGlossaryLookupPrefersSessionScope(t *testing.T) {
	ctx := context.Background()
	store := newGlossaryStore(t)
	now := time.Unix(1700000000, 0)

	upsertGlossary(t, store, assistant.GlossaryUpsertRequest{
		ScopeKey: assistant.GlossaryScopeGlobal, Term: "鸽", Meaning: "放人鸽子", Now: now,
	})
	upsertGlossary(t, store, assistant.GlossaryUpsertRequest{
		ScopeKey: "group:1", Term: "鸽", Meaning: "本群指某位群友的头像", Now: now,
	})

	hits, err := store.LookupGlossaryEntries(ctx, assistant.GlossaryQuery{
		ScopeKeys: []string{"group:1", assistant.GlossaryScopeGlobal},
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
func TestGlossaryDeleteIsReversible(t *testing.T) {
	ctx := context.Background()
	store := newGlossaryStore(t)
	now := time.Unix(1700000000, 0)

	upsertGlossary(t, store, assistant.GlossaryUpsertRequest{
		ScopeKey: "group:1", Term: "老梗", Meaning: "已经没人用了", Now: now,
	})
	deleted, found, err := store.DeleteGlossaryEntry(ctx, "group:1", "老梗", "10001", "Alice", "过时了", now.Add(time.Hour))
	if err != nil || !found {
		t.Fatalf("delete found=%v err=%v", found, err)
	}
	if deleted.Status != assistant.GlossaryStatusDeleted || deleted.Version != 2 {
		t.Fatalf("deleted = %+v", deleted)
	}

	hits, err := store.LookupGlossaryEntries(ctx, assistant.GlossaryQuery{
		ScopeKeys: []string{"group:1"}, Text: "还有人说老梗吗",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("作废的词条不该再自动命中: %+v", hits)
	}

	listed, err := store.ListGlossaryEntries(ctx, assistant.GlossaryQuery{ScopeKeys: []string{"group:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("默认列表不该带作废词条: %+v", listed)
	}
	listed, err = store.ListGlossaryEntries(ctx, assistant.GlossaryQuery{ScopeKeys: []string{"group:1"}, IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("include_deleted 应带出作废词条: %+v", listed)
	}

	restored, found, err := store.RestoreGlossaryEntry(ctx, "group:1", "老梗", "10001", "Alice", now.Add(2*time.Hour))
	if err != nil || !found {
		t.Fatalf("restore found=%v err=%v", found, err)
	}
	if restored.Status != assistant.GlossaryStatusActive || restored.Meaning != "已经没人用了" {
		t.Fatalf("restored = %+v", restored)
	}
	detail, _, err := store.GlossaryEntryDetail(ctx, "group:1", "老梗")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Revisions) != 3 {
		t.Fatalf("新建、作废、恢复都该留痕: %+v", detail.Revisions)
	}
}

// 作废过的词又被重新解释时应当复活成同一条，而不是另起一条——修订史是同一条线索。
func TestGlossaryUpsertRevivesDeletedEntry(t *testing.T) {
	ctx := context.Background()
	store := newGlossaryStore(t)
	now := time.Unix(1700000000, 0)

	upsertGlossary(t, store, assistant.GlossaryUpsertRequest{
		ScopeKey: "group:1", Term: "梗", Meaning: "旧释义", Now: now,
	})
	if _, _, err := store.DeleteGlossaryEntry(ctx, "group:1", "梗", "10001", "Alice", "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	entry, created, err := store.UpsertGlossaryEntry(ctx, assistant.GlossaryUpsertRequest{
		ScopeKey: "group:1", Term: "梗", Meaning: "又有人用起来了", Now: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("复活不该被报告成新建")
	}
	if entry.Status != assistant.GlossaryStatusActive || entry.Version != 3 {
		t.Fatalf("entry = %+v", entry)
	}
}

// 命中要能回写：用得多的排前面，长期没人用的自然沉底。
func TestGlossaryTouchOrdersByUsage(t *testing.T) {
	ctx := context.Background()
	store := newGlossaryStore(t)
	now := time.Unix(1700000000, 0)

	cold := upsertGlossary(t, store, assistant.GlossaryUpsertRequest{
		ScopeKey: "group:1", Term: "冷词", Meaning: "冷", Now: now,
	})
	hot := upsertGlossary(t, store, assistant.GlossaryUpsertRequest{
		ScopeKey: "group:1", Term: "热词", Meaning: "热", Now: now.Add(time.Minute),
	})
	// 一次命中记一次；同一条词条在两轮对话里各命中一次才是两次。
	for _, at := range []time.Time{now.Add(time.Hour), now.Add(2 * time.Hour)} {
		if err := store.TouchGlossaryEntries(ctx, []string{hot.ID}, at); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.ListGlossaryEntries(ctx, assistant.GlossaryQuery{ScopeKeys: []string{"group:1"}})
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
