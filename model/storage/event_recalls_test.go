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

// 线上：管理员撤回了机器人的一句回复，事件页多出一行「160867498 撤回了 Diana 的
// 消息」，撤的是哪句、回的是谁全看不出。撤回要能挂回发出那句话的事件上。
func TestListInboundEventDetailsAttachesRecallToOriginalReply(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "event-recall-attach.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().Truncate(time.Second)
	source := assistant.MessageEvent{
		Kind: assistant.EventKindGroup, GroupID: "group-1", UserID: "user-1",
		MessageID: "source-message", Time: now.Add(-time.Minute).Unix(),
	}
	if _, inserted, err := store.EnqueueInboundEvent(ctx, "group:group-1", source); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	for _, id := range []string{"outbound-1", "outbound-2"} {
		if err := store.RecordInboundEventDelivery(ctx, source, assistant.OutboundDeliveryAcknowledged, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	notice := func(groupID, messageID, operatorID string) assistant.MessageEvent {
		return assistant.MessageEvent{
			Kind: assistant.EventKindNotice, SubType: "group_recall", Time: now.Unix(),
			GroupID: groupID, UserID: "bot", SenderName: "Diana",
			OperatorID: operatorID, OperatorName: "Carol", OperatorRole: "group_admin",
			MessageID: messageID,
		}
	}
	for _, recall := range []assistant.MessageEvent{
		notice("group-1", "outbound-2", "admin-1"),
		// 别的群里同号的撤回不能挂到这条上。
		notice("group-2", "outbound-1", "admin-2"),
	} {
		if err := store.AppendMessageEvent(ctx, "group:"+recall.GroupID, recall); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordNoticeEvent(ctx, "group:"+recall.GroupID, recall); err != nil {
			t.Fatal(err)
		}
	}

	page, err := store.ListInboundEventDetails(ctx, InboundEventQuery{Since: now.Add(-time.Hour), Limit: 10, Result: InboundEventResultAll})
	if err != nil {
		t.Fatal(err)
	}
	var original *InboundEventDetail
	for index := range page.Events {
		if page.Events[index].MessageID == "source-message" {
			original = &page.Events[index]
		}
	}
	if original == nil {
		t.Fatalf("original event missing: %#v", page.Events)
	}
	if len(original.Recalls) != 1 {
		t.Fatalf("recalls = %#v, want exactly the same-group recall", original.Recalls)
	}
	got := original.Recalls[0]
	if got.MessageID != "outbound-2" || got.OperatorID != "admin-1" || got.OperatorName != "Carol" ||
		got.OperatorRole != "group_admin" || got.SelfRecall || !got.At.Equal(now) {
		t.Fatalf("recall = %#v", got)
	}
}
