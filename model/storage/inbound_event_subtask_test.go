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

// delivery_json 走审计写入、事件详情读出这一整条路。发媒体不发文字时它是「到底
// 发了什么」的唯一记录。
func TestInboundEventDeliveryRoundTrip(t *testing.T) {
	store := subtaskStore(t)
	ctx := context.Background()

	event := assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m-1",
		Time: time.Now().Unix(),
	}
	if _, _, err := store.EnqueueInboundEvent(ctx, "s", event); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInboundEventAudit(ctx, assistant.EventRecord{
		Kind: assistant.EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m-1",
		Decision: "replied", Reason: "命中触发", Handled: true,
		Delivery: assistant.OutboundDelivery{Images: 9, Videos: 1, ForwardCards: 1, ForwardNodes: 10},
	}); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListInboundEventDetails(ctx, time.Time{}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("events = %#v", page.Events)
	}
	got := page.Events[0].Delivery
	if got.Images != 9 || got.Videos != 1 || got.ForwardCards != 1 || got.ForwardNodes != 10 {
		t.Fatalf("delivery 没有原样往返：%#v", got)
	}
}

// 什么都没发时不写占位，读出来也是零值。
func TestInboundEventDeliveryOmittedWhenNothingSent(t *testing.T) {
	store := subtaskStore(t)
	ctx := context.Background()
	event := assistant.MessageEvent{
		Kind: assistant.EventKindPrivate, UserID: "10001", MessageID: "m-2", Time: time.Now().Unix(),
	}
	if _, _, err := store.EnqueueInboundEvent(ctx, "s", event); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInboundEventAudit(ctx, assistant.EventRecord{
		Kind: assistant.EventKindPrivate, UserID: "10001", MessageID: "m-2", Decision: "not_replied",
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListInboundEventDetails(ctx, time.Time{}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || !page.Events[0].Delivery.Empty() {
		t.Fatalf("没发东西却有 delivery：%#v", page.Events)
	}
}
