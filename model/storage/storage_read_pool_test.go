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

func openContentionStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "contention.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.readDB == nil {
		t.Fatal("file database must open a separate read pool")
	}
	return store
}

// holdWriter 占住唯一的写连接，模拟生产上写连接被别的调用长时间占用。
func holdWriter(t *testing.T, store *SQLiteStore) func() {
	t.Helper()
	writer, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := store.db.PingContext(ctx); err == nil {
		t.Fatal("writer should be exhausted while held")
	}
	return func() { _ = writer.Close() }
}

// 这些查询都是只读的，写连接被占住时也必须照常返回，不能排在入队和领取后面。
func TestReadOnlyQueriesDoNotQueueBehindWriter(t *testing.T) {
	store := openContentionStore(t)
	ctx := context.Background()
	event := historySearchEvent(100, "g1", "m1", "alice", "hello")
	if err := store.AppendMessageEvent(ctx, "onebot-main:group:g1", event); err != nil {
		t.Fatal(err)
	}
	item := assistant.InboundQueueItem{ID: "x", Session: "onebot-main:group:g1", Event: event, EnqueuedAt: time.Now()}
	release := holdWriter(t, store)
	defer release()

	calls := map[string]func(context.Context) error{
		"PendingInboundCount": func(ctx context.Context) error { _, err := store.PendingInboundCount(ctx); return err },
		"ListHistorySessions": func(ctx context.Context) error {
			sessions, err := store.ListHistorySessions(ctx)
			if err == nil && len(sessions) != 1 {
				t.Errorf("sessions = %+v", sessions)
			}
			return err
		},
		"GroupHistoryWatermark": func(ctx context.Context) error {
			_, _, err := store.GroupHistoryWatermark(ctx, "g1")
			return err
		},
		"InboundSessionHasNewerPending": func(ctx context.Context) error {
			_, err := store.InboundSessionHasNewerPending(ctx, item)
			return err
		},
		"GroupSeqGap": func(ctx context.Context) error {
			_, err := store.GroupSeqGap(ctx, assistant.GroupSeqGapQuery{GroupID: "g1", SelfID: "bot", Seq: 10, EventTime: 200})
			return err
		},
		"ListSelfNotes": func(ctx context.Context) error { _, err := store.ListSelfNotes(ctx, "bot", false, 5); return err },
		"GetImageDescription": func(ctx context.Context) error {
			_, _, err := store.GetImageDescription(ctx, "abc")
			return err
		},
		"LoadImageRecognition": func(ctx context.Context) error {
			_, _, err := store.LoadImageRecognition(ctx, "key")
			return err
		},
		"LoadVoiceTranscript": func(ctx context.Context) error {
			_, _, err := store.LoadVoiceTranscript(ctx, "key")
			return err
		},
		"LoadSemanticReferenceCache": func(ctx context.Context) error {
			_, _, err := store.LoadSemanticReferenceCache(ctx, "key")
			return err
		},
		"GroupStyle": func(ctx context.Context) error { _, _, err := store.GroupStyle(ctx, "bot", "g1"); return err },
		"GroupRelationGraphFor": func(ctx context.Context) error {
			_, err := store.GroupRelationGraphFor(ctx, "g1", time.Time{}, "bot")
			return err
		},
		"ListStickerAssets": func(ctx context.Context) error {
			_, err := store.ListStickerAssets(ctx, assistant.StickerHistoryQuery{Session: "onebot-main:group:g1"})
			return err
		},
		"ListStickerLibrary": func(ctx context.Context) error {
			_, err := store.ListStickerLibrary(ctx, StickerLibraryQuery{Limit: 5})
			return err
		},
		"StickerAssetFile": func(ctx context.Context) error {
			_, _, err := store.StickerAssetFile(ctx, strings.Repeat("a", 64), "")
			return err
		},
		"LoadImageModelRecord": func(ctx context.Context) error {
			_, _, err := store.LoadImageModelRecord(ctx, "scope", "")
			return err
		},
		"SearchMessageEvents": func(ctx context.Context) error {
			_, _, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{Session: "onebot-main:group:g1", Text: "hello"})
			return err
		},
		"ClaimMemoryJobBatch(empty)": func(ctx context.Context) error {
			jobs, err := store.ClaimMemoryJobBatch(ctx, "worker", time.Now().Add(time.Minute), 4)
			if len(jobs) != 0 {
				t.Errorf("claimed from an empty queue: %+v", jobs)
			}
			return err
		},
	}
	for name, call := range calls {
		callCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		err := call(callCtx)
		cancel()
		if err != nil {
			t.Errorf("%s queued behind the writer: %v", name, err)
		}
	}
}

// 队列里没有到期任务时，领取不能去排写连接；有到期任务时照常领取。
func TestClaimMemoryJobSkipsWriteTransactionWhenNothingDue(t *testing.T) {
	store := openContentionStore(t)
	ctx := context.Background()
	logs := useTestSlowLog(t, 0, 0)
	store.SetMemoryEventJobDelay(time.Hour)
	if _, _, err := store.EnqueueMemoryJob(ctx, batchJob("group:123", "alice", "later", 100)); err != nil {
		t.Fatal(err)
	}

	release := holdWriter(t, store)
	for _, claim := range []func(context.Context) (int, error){
		func(ctx context.Context) (int, error) {
			jobs, err := store.ClaimMemoryJobBatch(ctx, "worker", time.Now().Add(time.Minute), 4)
			return len(jobs), err
		},
		func(ctx context.Context) (int, error) {
			_, ok, err := store.ClaimNextMemoryJob(ctx, "worker", time.Now().Add(time.Minute))
			if ok {
				return 1, err
			}
			return 0, err
		},
	} {
		claimCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		claimed, err := claim(claimCtx)
		cancel()
		if err != nil || claimed != 0 {
			t.Fatalf("claim with nothing due must return empty without the writer: claimed=%d err=%v", claimed, err)
		}
	}
	for _, line := range logs.output() {
		if strings.Contains(line, "begin_transaction") {
			t.Fatalf("claim opened a write transaction with nothing due: %s", line)
		}
	}

	// 有到期任务时必须真的去开写事务：写连接被占着就等不到。
	release()
	store.SetMemoryEventJobDelay(0)
	if _, _, err := store.EnqueueMemoryJob(ctx, batchJob("group:123", "alice", "now", 200)); err != nil {
		t.Fatal(err)
	}
	release = holdWriter(t, store)
	claimCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err := store.ClaimMemoryJobBatch(claimCtx, "worker", time.Now().Add(time.Minute), 4)
	cancel()
	release()
	if err == nil {
		t.Fatal("a due job must be claimed through the write transaction")
	}
	jobs, err := store.ClaimMemoryJobBatch(ctx, "worker", time.Now().Add(time.Minute), 4)
	if err != nil || len(jobs) != 1 || jobs[0].Payload.Event.MessageID != "now" {
		t.Fatalf("due job not claimed: %+v %v", jobs, err)
	}
}

// 回补会话查询要只扫覆盖索引，不再逐行回表解析 payload。索引里的表达式和查询
// 差一个字符规划器就认不出来，这里把两个分支都钉住。
func TestListHistorySessionsUsesCoveringIndex(t *testing.T) {
	store := openContentionStore(t)
	rows, err := store.eventReader().Query(`EXPLAIN QUERY PLAN `+historySessionsQuery, string(assistant.EventKindGroup), string(assistant.EventKindPrivate))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	covered := 0
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
		if strings.Contains(detail, "COVERING INDEX idx_message_events_history_sessions") {
			covered++
		}
		if strings.HasPrefix(detail, "SCAN message_events") {
			t.Fatalf("history sessions query scans the table: %q", plan)
		}
	}
	if covered != 2 {
		t.Fatalf("both branches must use the covering index: %q", plan)
	}
}

// 表达式索引在每次写入时求值。payload 不是 JSON 的行以前能写进去，加了索引以后
// 也必须能写、能更新，回补快照照常列出它（平台、机器人按空值算）。
func TestHistorySessionsIndexToleratesNonJSONPayload(t *testing.T) {
	store := openContentionStore(t)
	var indexed int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_message_events_history_sessions'`).Scan(&indexed); err != nil || indexed != 1 {
		t.Fatalf("history sessions index missing: %d %v", indexed, err)
	}
	if _, err := store.db.Exec(`INSERT INTO message_events (id, session, kind, group_id, user_id, message_id, sender_name, event_time, text, payload, created_at)
VALUES ('broken', 'group:g9', 'group', 'g9', 'u1', 'broken', 'Alice', 100, '', 'not json', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("non-JSON payload rejected by the index: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE message_events SET payload = '{oops', event_time = 120 WHERE id = 'broken'`); err != nil {
		t.Fatalf("non-JSON payload update rejected by the index: %v", err)
	}
	event := historySearchEvent(150, "g1", "ok", "bob", "hello")
	event.Platform = "onebot_v11"
	event.ProfileID = "bot"
	if err := store.AppendMessageEvent(context.Background(), "onebot-main:group:g1", event); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.ListHistorySessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]assistant.HistorySession{}
	for _, session := range sessions {
		got[session.ID] = session
	}
	if broken := got["g9"]; broken.Platform != "" || broken.ProfileID != "" || broken.LastEventTime != 120 {
		t.Fatalf("non-JSON row: %+v", broken)
	}
	if ok := got["g1"]; ok.Platform != "onebot_v11" || ok.ProfileID != "bot" || ok.LastEventTime != 150 {
		t.Fatalf("JSON row: %+v", ok)
	}
}
