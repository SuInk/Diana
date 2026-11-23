// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func TestAgentToolCallsListPerSessionAndPrune(t *testing.T) {
	store := subtaskStore(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	for _, record := range []assistant.AgentToolCallRecord{
		{Session: "group:10001", ProfileID: "bot", Tool: "image", Input: `{"prompt":"旧图"}`, CreatedAt: old},
		{Session: "group:10001", ProfileID: "bot", MessageID: "m1", UserID: "20001", Tool: "image", Input: `{"prompt":"腿肉一点"}`, DurationMS: 12},
		{Session: "group:10001", ProfileID: "bot", Tool: "web_search", Input: `{"query":"ti-84"}`, Error: "timeout"},
		{Session: "group:10001", ProfileID: "other-bot", Tool: "web_search"},
		{Session: "group:10002", ProfileID: "bot", Tool: "web_search"},
	} {
		if err := store.RecordAgentToolCall(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.ListAgentToolCalls(ctx, assistant.AgentToolCallQuery{Session: "group:10001", ProfileID: "bot", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Tool != "web_search" || all[0].Error != "timeout" || all[1].Input != `{"prompt":"腿肉一点"}` || all[1].MessageID != "m1" || all[1].DurationMS != 12 {
		t.Fatalf("all=%#v", all)
	}
	images, err := store.ListAgentToolCalls(ctx, assistant.AgentToolCallQuery{Session: "group:10001", ProfileID: "bot", Tool: "image", BeforeID: all[1].ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Input != `{"prompt":"旧图"}` {
		t.Fatalf("images before=%#v", images)
	}
	deleted, err := store.PruneAgentToolCalls(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
}

// 后台任务表只挂入站事件 ID，按会话查要连 inbound_events；请求内容后续更新不带时保留。
func TestListSessionSubtasksJoinsInboundEvents(t *testing.T) {
	store := subtaskStore(t)
	ctx := context.Background()
	here := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "10001", UserID: "20001", MessageID: "m1", ProfileID: "bot", RawMessage: "改一下"}
	elsewhere := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "10002", UserID: "20001", MessageID: "m2", ProfileID: "bot", RawMessage: "画只猫"}
	hereID, _, err := store.EnqueueInboundEvent(ctx, "group:10001", here)
	if err != nil {
		t.Fatal(err)
	}
	elsewhereID, _, err := store.EnqueueInboundEvent(ctx, "group:10002", elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Minute).Truncate(time.Second)
	for _, item := range []assistant.InboundEventSubtask{
		{EventID: hereID, TaskID: "img-a", Kind: "image", Name: "图片编辑", Request: "edit：腿肉一点", Phase: "running", StartedAt: started},
		{EventID: elsewhereID, TaskID: "img-b", Kind: "image", Name: "图片生成", Request: "generate：猫", Phase: "running", StartedAt: started},
		{EventID: hereID, TaskID: "img-a", Kind: "image", Name: "图片编辑", Phase: "completed", FinishedAt: &started},
	} {
		if err := store.SaveInboundEventSubtask(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListSessionSubtasks(ctx, assistant.SessionSubtaskQuery{Session: "group:10001", ProfileID: "bot", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].TaskID != "img-a" || items[0].Request != "edit：腿肉一点" || items[0].Phase != "completed" || items[0].MessageID != "m1" || items[0].FinishedAt == nil || items[0].Seq == 0 {
		t.Fatalf("items=%#v", items)
	}
	loaded, err := store.LoadInboundEventSubtasks(ctx, []string{hereID})
	if err != nil || len(loaded[hereID]) != 1 || loaded[hereID][0].Request != "edit：腿肉一点" {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}
