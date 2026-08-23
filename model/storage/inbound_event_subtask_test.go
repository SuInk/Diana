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

func subtaskStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "subtasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestInboundEventSubtaskLifecycle(t *testing.T) {
	store := subtaskStore(t)
	ctx := context.Background()
	started := time.Now().Add(-time.Minute).Truncate(time.Second)

	// 开始
	if err := store.SaveInboundEventSubtask(ctx, assistant.InboundEventSubtask{
		EventID: "evt-1", TaskID: "img-a", Kind: "image", Name: "图片生成",
		Phase: "running", StartedAt: started, UpdatedAt: started,
	}); err != nil {
		t.Fatal(err)
	}
	// 进度
	if err := store.SaveInboundEventSubtask(ctx, assistant.InboundEventSubtask{
		EventID: "evt-1", TaskID: "img-a", Kind: "image", Name: "图片生成",
		Phase: "running", Completed: 1, Total: 3, Detail: "正在编辑第 1/3 张",
		UpdatedAt: started.Add(10 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	// 完成
	finished := started.Add(30 * time.Second)
	if err := store.SaveInboundEventSubtask(ctx, assistant.InboundEventSubtask{
		EventID: "evt-1", TaskID: "img-a", Kind: "image", Name: "图片生成",
		Phase: "completed", UpdatedAt: finished, FinishedAt: &finished,
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadInboundEventSubtasks(ctx, []string{"evt-1", "evt-2"})
	if err != nil {
		t.Fatal(err)
	}
	items := loaded["evt-1"]
	if len(items) != 1 {
		t.Fatalf("subtasks = %#v", items)
	}
	item := items[0]
	if item.Phase != "completed" || item.FinishedAt == nil {
		t.Fatalf("终态没写进去：%#v", item)
	}
	// started_at 不能被后续更新往后推，否则事件页上的耗时会越看越短。
	if !item.StartedAt.Equal(started) {
		t.Fatalf("started_at 被覆盖了：want %v, got %v", started, item.StartedAt)
	}
	// 进度里的说明不该被后一次不带 detail 的更新清空。
	if item.Detail != "正在编辑第 1/3 张" {
		t.Fatalf("detail 被清空了：%q", item.Detail)
	}
	if len(loaded["evt-2"]) != 0 {
		t.Fatalf("查到了不存在的事件：%#v", loaded["evt-2"])
	}
}

func TestInboundEventSubtaskIgnoresEmptyKeys(t *testing.T) {
	store := subtaskStore(t)
	ctx := context.Background()
	if err := store.SaveInboundEventSubtask(ctx, assistant.InboundEventSubtask{TaskID: "img-a"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveInboundEventSubtask(ctx, assistant.InboundEventSubtask{EventID: "evt-1"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadInboundEventSubtasks(ctx, []string{"evt-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 0 {
		t.Fatalf("缺少 event_id 或 task_id 的记录不该落库：%#v", loaded)
	}
}
