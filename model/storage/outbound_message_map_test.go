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

// 运行时靠这个可选接口走按主键定位的回执路径，少实现一个方法就会悄悄退回扫表。
var _ assistant.InboundEventDeliveryByIDStore = (*SQLiteStore)(nil)

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

func explainQueryPlan(t *testing.T, store *SQLiteStore, query string, args ...any) []string {
	t.Helper()
	rows, err := store.db.Query("EXPLAIN QUERY PLAN "+query, args...)
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
	return plan
}

// 发送回执每条分片都要写一次，走单写连接：按入站事件主键定位、映射按 VALUES
// 写入，两步都不能扫 inbound_events。
func TestDeliveryReceiptPathUsesPrimaryKeysNotFullScan(t *testing.T) {
	store := openOutboundDeliveryTestStore(t)
	setArgs := inboundDeliverySetArgs(assistant.OutboundDeliveryAcknowledged, "54321", "", 1)
	receiptArgs := append(append(append([]any{}, setArgs...), "event-1"), "in-1", "group", "20005", "10001")
	for name, plan := range map[string][]string{
		"receipt update": explainQueryPlan(t, store, inboundDeliveryByIDSQL, receiptArgs...),
		"map write":      explainQueryPlan(t, store, recordOutboundMessageMapSQL, "bot", "group:20005", "54321", "event-1", 1),
	} {
		for _, line := range plan {
			if strings.HasPrefix(line, "SCAN ") || strings.Contains(line, "TEMP B-TREE") {
				t.Fatalf("%s is not an indexed lookup:\n%s", name, strings.Join(plan, "\n"))
			}
		}
	}
}

// 按 id 定位时 id 必须对得上这条消息；对不上（这一轮顺带发给别的会话）就退回
// 按消息查找，不会把回执记到这一轮的入站事件上。
func TestDeliveryByIDFallsBackWhenIDDoesNotMatchEvent(t *testing.T) {
	ctx := context.Background()
	store := openOutboundDeliveryTestStore(t)
	turn := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "in-turn", Time: time.Now().Unix()}
	other := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "20006", UserID: "10001", MessageID: "in-other", Time: time.Now().Unix()}
	turnID, _, err := store.EnqueueInboundEvent(ctx, "group:20005", turn)
	if err != nil {
		t.Fatal(err)
	}
	otherID, _, err := store.EnqueueInboundEvent(ctx, "group:20006", other)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.RecordInboundEventDeliveryByID(ctx, turnID, turn, assistant.OutboundDeliveryAcknowledged, "54700", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInboundEventDeliveryByID(ctx, turnID, other, assistant.OutboundDeliveryAcknowledged, "54701", ""); err != nil {
		t.Fatal(err)
	}
	outbound := func(id string) string {
		var value string
		if err := store.db.QueryRow(`SELECT COALESCE(outbound_message_id, '') FROM inbound_events WHERE id = ?`, id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if got := outbound(turnID); got != "54700" {
		t.Fatalf("turn outbound ids = %q", got)
	}
	if got := outbound(otherID); got != "54701" {
		t.Fatalf("other outbound ids = %q", got)
	}
	var mapped string
	if err := store.db.QueryRow(`SELECT inbound_event_id FROM outbound_message_map WHERE conversation = 'group:20006' AND message_id = '54701'`).Scan(&mapped); err != nil || mapped != otherID {
		t.Fatalf("map for fallback row = %q err=%v", mapped, err)
	}
}

// 确认不了送达的终态算错误，不算「未回复」。
func TestUnconfirmedOutboundOutcomeCountsAsError(t *testing.T) {
	ctx := context.Background()
	store := openOutboundDeliveryTestStore(t)
	now := time.Now()
	if _, err := store.db.Exec(`
INSERT INTO inbound_events (
  id, session, kind, group_id, user_id, message_id, event_time, payload, priority,
  status, attempts, available_at, outcome, created_at, updated_at, completed_at
) VALUES ('unconfirmed', 'group:g', 'group', 'g', 'u', 'unconfirmed', ?, '{}', 0, 'done', 1, ?, 'dropped_outbound_unconfirmed', ?, ?, ?)
`, now.Unix(), now.UnixNano(), now.UnixNano(), now.UnixNano(), now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListInboundEventDetails(ctx, InboundEventQuery{Since: now.Add(-time.Hour), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Errors != 1 || page.NotReplied != 0 || page.Replied != 0 {
		t.Fatalf("counts = replied=%d not=%d errors=%d", page.Replied, page.NotReplied, page.Errors)
	}
}
