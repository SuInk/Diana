// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestSelfNotePersistsReviseAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "self-notes.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Unix(1_788_247_000, 0)
	created, err := store.WriteSelfNote(ctx, assistant.SelfNoteWriteRequest{
		ProfileID: "bot-1", Topic: "说话方式", Content: "我老把话说太长，先给结论再补理由",
		SourceSession: "bot-1:group:1", SourceGroupID: "1", SourceMessageID: "m1",
		SourceUserID: "user-a", SourceUserName: "阿狸", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.Status != assistant.SelfNoteStatusActive {
		t.Fatalf("created = %#v", created)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// 重启后仍在册：自述跨会话、跨重启生效，只活在内存里就等于没有。
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	notes, err := store.ListSelfNotes(ctx, "bot-1", false, 0)
	if err != nil || len(notes) != 1 || notes[0].SourceUserID != "user-a" {
		t.Fatalf("reloaded = %#v, err=%v", notes, err)
	}

	revised, err := store.WriteSelfNote(ctx, assistant.SelfNoteWriteRequest{
		ProfileID: "bot-1", Topic: "说话方式", Content: "一句说完就不铺三句",
		SupersedesID: created.ID, Now: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if revised.Version != 2 || revised.SupersedesID != created.ID || revised.ID == created.ID {
		t.Fatalf("revised = %#v", revised)
	}
	// 改写是滚动版本，不是原地覆盖：在册只剩新的那条，旧的仍然查得到。
	notes, err = store.ListSelfNotes(ctx, "bot-1", false, 0)
	if err != nil || len(notes) != 1 || notes[0].ID != revised.ID {
		t.Fatalf("active after revise = %#v, err=%v", notes, err)
	}
	history, err := store.ListSelfNotes(ctx, "bot-1", true, 0)
	if err != nil || len(history) != 2 {
		t.Fatalf("history = %#v, err=%v", history, err)
	}
	if history[0].ID != created.ID || history[0].Status != assistant.SelfNoteStatusSuperseded {
		t.Fatalf("superseded row lost: %#v", history[0])
	}

	// 改写只认在册条目：拿已经被顶掉的旧 ID 再改一次必须失败，否则同一条会分叉
	// 成两支各自往上加版本号的历史。
	if _, err := store.WriteSelfNote(ctx, assistant.SelfNoteWriteRequest{
		ProfileID: "bot-1", Content: "又改一次", SupersedesID: created.ID, Now: now.Add(2 * time.Minute),
	}); err == nil {
		t.Fatal("revising a superseded note should fail")
	}

	deleted, found, err := store.DeleteSelfNote(ctx, "bot-1", revised.ID, "owner-1", "主人", now.Add(3*time.Minute))
	if err != nil || !found || deleted.Status != assistant.SelfNoteStatusDeleted || deleted.EditorUserID != "owner-1" {
		t.Fatalf("deleted = %#v, found=%v, err=%v", deleted, found, err)
	}
	// 重复删除是模型常见的重试，不该报错。
	if _, found, err := store.DeleteSelfNote(ctx, "bot-1", revised.ID, "owner-1", "主人", now.Add(4*time.Minute)); err != nil || found {
		t.Fatalf("second delete found=%v err=%v", found, err)
	}
	notes, err = store.ListSelfNotes(ctx, "bot-1", false, 0)
	if err != nil || len(notes) != 0 {
		t.Fatalf("active after delete = %#v, err=%v", notes, err)
	}
}

func TestSelfNoteCapacityAndProfileIsolation(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "self-notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	now := time.Unix(1_788_247_000, 0)
	for index := 0; index < assistant.MaximumActiveSelfNotes; index++ {
		if _, err := store.WriteSelfNote(ctx, assistant.SelfNoteWriteRequest{
			ProfileID: "bot-1", Content: fmt.Sprintf("第 %d 条观察", index), Now: now.Add(time.Duration(index) * time.Second),
		}); err != nil {
			t.Fatalf("write %d: %v", index, err)
		}
	}
	// 满了之后必须报容量错误：静默淘汰最旧的一条会悄悄丢掉最根本的那条自我描述。
	_, err = store.WriteSelfNote(ctx, assistant.SelfNoteWriteRequest{ProfileID: "bot-1", Content: "再来一条", Now: now})
	if !errors.Is(err, assistant.ErrSelfNoteCapacity) {
		t.Fatalf("capacity err = %v", err)
	}
	// 改写不占新名额：上限卡的是在册条数，不是写入次数。
	notes, err := store.ListSelfNotes(ctx, "bot-1", false, assistant.MaximumActiveSelfNotes)
	if err != nil || len(notes) != assistant.MaximumActiveSelfNotes {
		t.Fatalf("notes = %d, err=%v", len(notes), err)
	}
	if _, err := store.WriteSelfNote(ctx, assistant.SelfNoteWriteRequest{
		ProfileID: "bot-1", Content: "改掉第一条", SupersedesID: notes[0].ID, Now: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("revise at capacity: %v", err)
	}

	// 另一台机器人的自述互不可见，也各算各的名额。
	if _, err := store.WriteSelfNote(ctx, assistant.SelfNoteWriteRequest{ProfileID: "bot-2", Content: "我是另一台", Now: now}); err != nil {
		t.Fatal(err)
	}
	other, err := store.ListSelfNotes(ctx, "bot-2", false, 0)
	if err != nil || len(other) != 1 || other[0].Content != "我是另一台" {
		t.Fatalf("bot-2 notes = %#v, err=%v", other, err)
	}
	removed, err := store.PurgeSelfNotes(ctx, "bot-1", "owner-1", "主人", now.Add(2*time.Hour))
	if err != nil || removed != assistant.MaximumActiveSelfNotes {
		t.Fatalf("purged = %d, err=%v", removed, err)
	}
	if other, err = store.ListSelfNotes(ctx, "bot-2", false, 0); err != nil || len(other) != 1 {
		t.Fatalf("bot-2 after purge = %#v, err=%v", other, err)
	}
}

// ListSelfNotesAfter 按 (created_at, id) 键集翻页：同一纳秒写入的也不重不漏，
// 读过的条目被改写（新版本追加在末尾）后，后面的页照样接得上；计数和列表同口径。
func TestSelfNotesKeysetPaging(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "self-notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	same := time.Unix(1_788_247_000, 0)
	var written []assistant.SelfNote
	for i := 0; i < 6; i++ {
		note, err := store.WriteSelfNote(ctx, assistant.SelfNoteWriteRequest{ProfileID: "bot-1", Content: fmt.Sprintf("第 %d 条", i), Now: same})
		if err != nil {
			t.Fatal(err)
		}
		written = append(written, note)
	}
	query := assistant.SelfNoteListQuery{ProfileID: "bot-1", Limit: 4}
	first, err := store.ListSelfNotesAfter(ctx, query)
	if err != nil || len(first) != 4 {
		t.Fatalf("first page = %#v, err=%v", first, err)
	}
	// 改写第一页里的一条：旧版本转 superseded，新版本排到最后。
	revised, err := store.WriteSelfNote(ctx, assistant.SelfNoteWriteRequest{ProfileID: "bot-1", Content: "改写", SupersedesID: first[0].ID, Now: same.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	query.After, query.AfterID = first[3].CreatedAt, first[3].ID
	if left, err := store.CountSelfNotes(ctx, query); err != nil || left != 3 {
		t.Fatalf("count after cursor = %d, %v", left, err)
	}
	rest, err := store.ListSelfNotesAfter(ctx, query)
	if err != nil || len(rest) != 3 || rest[2].ID != revised.ID {
		t.Fatalf("rest = %#v, err=%v", rest, err)
	}
	seen := map[string]bool{}
	for _, note := range append(first, rest...) {
		if seen[note.ID] {
			t.Fatalf("note %s returned twice", note.ID)
		}
		seen[note.ID] = true
	}
	for _, note := range written {
		if !seen[note.ID] {
			t.Fatalf("note %s skipped", note.ID)
		}
	}
	all := assistant.SelfNoteListQuery{ProfileID: "bot-1", IncludeInactive: true}
	if total, err := store.CountSelfNotes(ctx, all); err != nil || total != 7 {
		t.Fatalf("history count = %d, %v", total, err)
	}
	if active, err := store.CountSelfNotes(ctx, assistant.SelfNoteListQuery{ProfileID: "bot-1"}); err != nil || active != 6 {
		t.Fatalf("active count = %d, %v", active, err)
	}
}
