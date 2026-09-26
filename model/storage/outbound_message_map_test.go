// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func openOutboundDeliveryTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "outbound-delivery.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func enqueueAckedInbound(t *testing.T, store *SQLiteStore, event assistant.MessageEvent, session, outboundMessageID string) string {
	t.Helper()
	ctx := context.Background()
	id, inserted, err := store.EnqueueInboundEvent(ctx, session, event)
	if err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	if err := store.RecordInboundEventDelivery(ctx, event, assistant.OutboundDeliveryAcknowledged, outboundMessageID, ""); err != nil {
		t.Fatal(err)
	}
	return id
}

type selfEchoRow struct {
	stage, deliveryError string
	echoed               bool
}

func readSelfEchoRow(t *testing.T, store *SQLiteStore, id string) selfEchoRow {
	t.Helper()
	var row selfEchoRow
	var echoAt int64
	if err := store.db.QueryRow(`SELECT COALESCE(delivery_stage, ''), COALESCE(delivery_error, ''), COALESCE(self_echo_at, 0) FROM inbound_events WHERE id = ?`, id).
		Scan(&row.stage, &row.deliveryError, &echoAt); err != nil {
		t.Fatal(err)
	}
	row.echoed = echoAt > 0
	return row
}

// 回推按 (账号, 会话, message_id) 主键命中映射，再按主键改入站事件：两步都不能
// 全表扫描 inbound_events。
func TestSelfEchoLookupUsesIndexesNotFullScan(t *testing.T) {
	store := openOutboundDeliveryTestStore(t)
	rows, err := store.db.Query("EXPLAIN QUERY PLAN "+recordSelfEchoSQL, 1, 1, "bot", "group:20005", "54321", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "\n")
	for _, line := range plan {
		if strings.HasPrefix(line, "SCAN ") {
			t.Fatalf("self echo update scans a table:\n%s", joined)
		}
	}
	if !strings.Contains(joined, "outbound_message_map") || !strings.Contains(joined, "inbound_events") {
		t.Fatalf("unexpected plan:\n%s", joined)
	}
}

// message_id 只在一个账号的一个会话里唯一：别的群、别的机器人、很久以前撞号的
// 那条都不能被这次回推改掉。
func TestSelfEchoOnlyTouchesExactAccountConversationAndRecentSend(t *testing.T) {
	ctx := context.Background()
	store := openOutboundDeliveryTestStore(t)
	now := time.Now().Unix()
	source := assistant.MessageEvent{Kind: assistant.EventKindGroup, ProfileID: "bot-a", GroupID: "20005", UserID: "10001", MessageID: "in-1", Time: now}
	otherGroup := assistant.MessageEvent{Kind: assistant.EventKindGroup, ProfileID: "bot-a", GroupID: "20006", UserID: "10001", MessageID: "in-2", Time: now}
	otherBot := assistant.MessageEvent{Kind: assistant.EventKindGroup, ProfileID: "bot-b", GroupID: "20005", UserID: "10001", MessageID: "in-3", Time: now}
	stale := assistant.MessageEvent{Kind: assistant.EventKindGroup, ProfileID: "bot-a", GroupID: "20007", UserID: "10001", MessageID: "in-4", Time: now}

	sourceID := enqueueAckedInbound(t, store, source, "group:20005", "54321")
	otherGroupID := enqueueAckedInbound(t, store, otherGroup, "group:20006", "54321")
	otherBotID := enqueueAckedInbound(t, store, otherBot, "bot-b:group:20005", "54321")
	staleID := enqueueAckedInbound(t, store, stale, "group:20007", "54321")
	// 这一条的映射是很久以前写的：同一个号再出现只能是撞号。
	if _, err := store.db.Exec(`UPDATE outbound_message_map SET sent_at = ? WHERE conversation = 'group:20007'`,
		time.Now().Add(-2*outboundEchoLinkWindow).UTC().UnixNano()); err != nil {
		t.Fatal(err)
	}

	for _, echo := range []assistant.MessageEvent{
		{Kind: assistant.EventKindGroup, ProfileID: "bot-a", GroupID: "20005", MessageID: "54321"},
		{Kind: assistant.EventKindGroup, ProfileID: "bot-a", GroupID: "20007", MessageID: "54321"},
	} {
		if err := store.RecordInboundEventSelfEcho(ctx, echo, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if row := readSelfEchoRow(t, store, sourceID); !row.echoed || row.stage != string(assistant.OutboundDeliveryEchoPersisted) {
		t.Fatalf("matching row = %+v", row)
	}
	for name, id := range map[string]string{"other group": otherGroupID, "other bot": otherBotID, "stale collision": staleID} {
		if row := readSelfEchoRow(t, store, id); row.echoed || row.stage != string(assistant.OutboundDeliveryAcknowledged) {
			t.Fatalf("%s was touched: %+v", name, row)
		}
	}
}

// 私聊回推按 target_id 找会话；没有 target_id 的回推分不出发给了谁，不关联。
func TestPrivateSelfEchoLinksByTargetID(t *testing.T) {
	ctx := context.Background()
	store := openOutboundDeliveryTestStore(t)
	source := assistant.MessageEvent{Kind: assistant.EventKindPrivate, UserID: "10001", MessageID: "in-p", Time: time.Now().Unix()}
	id := enqueueAckedInbound(t, store, source, "private:10001", "54400")

	if err := store.RecordInboundEventSelfEcho(ctx, assistant.MessageEvent{Kind: assistant.EventKindPrivate, UserID: "10000", MessageID: "54400"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if row := readSelfEchoRow(t, store, id); row.echoed {
		t.Fatal("echo without target_id was linked")
	}
	if err := store.RecordInboundEventSelfEcho(ctx, assistant.MessageEvent{Kind: assistant.EventKindPrivate, UserID: "10000", TargetID: "10001", MessageID: "54400"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if row := readSelfEchoRow(t, store, id); !row.echoed {
		t.Fatal("private echo with target_id was not linked")
	}
}

// 前一条分片的回推晚到，不能把后一条分片的发送失败记录抹掉。
func TestLateSelfEchoKeepsLaterChunkFailure(t *testing.T) {
	ctx := context.Background()
	store := openOutboundDeliveryTestStore(t)
	source := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "in-chunks", Time: time.Now().Unix()}
	id := enqueueAckedInbound(t, store, source, "group:20005", "54500")
	if err := store.RecordInboundEventDelivery(ctx, source, assistant.OutboundDeliveryFailed, "", "second chunk failed"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInboundEventSelfEcho(ctx, assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "20005", MessageID: "54500"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	row := readSelfEchoRow(t, store, id)
	if !row.echoed || row.deliveryError != "second chunk failed" || row.stage == string(assistant.OutboundDeliveryEchoPersisted) {
		t.Fatalf("row after late echo = %+v", row)
	}
}
