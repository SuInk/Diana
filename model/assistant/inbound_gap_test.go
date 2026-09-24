// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// GroupSeqGap 用内存里的入站记录模拟 SQLite 的实现：机器人自己发的消息直接塞进 records。
func (s *memoryInboundEventStore) GroupSeqGap(_ context.Context, query GroupSeqGapQuery) (GroupSeqGap, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gap := GroupSeqGap{}
	for _, record := range s.records {
		event := record.item.Event
		if event.Kind != EventKindGroup || event.GroupID != query.GroupID || event.Time < query.Since || event.Time > query.EventTime {
			continue
		}
		seq, ok := parseMessageSeq(event.MessageSeq)
		if !ok || seq >= query.Seq || seq <= gap.PreviousSeq {
			continue
		}
		gap = GroupSeqGap{Known: true, PreviousSeq: seq, PreviousTime: event.Time}
	}
	if !gap.Known {
		return gap, nil
	}
	for _, record := range s.records {
		event := record.item.Event
		if event.GroupID == query.GroupID && event.UserID == query.SelfID && event.Time >= gap.PreviousTime && event.Time <= query.EventTime {
			gap.SelfMessages++
		}
	}
	gap.Missing = max(int(query.Seq-gap.PreviousSeq-1)-gap.SelfMessages, 0)
	return gap, nil
}

func withGapTestDelays(t *testing.T, followUps, retries []time.Duration) {
	t.Helper()
	previousFollowUps, previousRetries := historyFollowUpDelays, seqGapRetryDelays
	historyFollowUpDelays, seqGapRetryDelays = followUps, retries
	t.Cleanup(func() {
		historyFollowUpDelays, seqGapRetryDelays = previousFollowUps, previousRetries
	})
}

func seqTestEvent(seq int64, eventTime int64, userID string) MessageEvent {
	text := "群友闲聊 " + strconv.FormatInt(seq, 10)
	return MessageEvent{
		Kind:       EventKindGroup,
		Time:       eventTime,
		SelfID:     "42",
		GroupID:    "123",
		UserID:     userID,
		MessageID:  strconv.FormatInt(seq, 10),
		MessageSeq: strconv.FormatInt(seq, 10),
		RawMessage: text,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

func appLogEntry(entries []applog.Entry, action string) (applog.Entry, bool) {
	for _, entry := range entries {
		if entry.Action == action {
			return entry, true
		}
	}
	return applog.Entry{}, false
}

func startGapTestRuntime(t *testing.T, store *memoryInboundEventStore, channel *queueTestChannel) (*Runtime, *captureAppLogs) {
	t.Helper()
	logs := &captureAppLogs{}
	runtime := newQueuedTestRuntime(channel, store, nil)
	runtime.SetAppLogWriter(logs)
	startTestRuntime(t, runtime)
	waitForCondition(t, 4*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "backfill_completed")
	})
	return runtime, logs
}

// 今天线上的情况：重连后第一次回补时 QQ 还没同步离线消息，回补「成功」却什么也没拿到。
// 第一条实时消息的 seq 暴露出缺口，针对这个群补回来。
func TestRuntimeSeqGapTriggersTargetedGroupBackfill(t *testing.T) {
	withGapTestDelays(t, nil, []time.Duration{50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond})
	store := newMemoryInboundEventStore()
	base := time.Now().Add(-10 * time.Minute).Unix()
	before := seqTestEvent(100, base, "10001")
	if _, _, err := store.EnqueueInboundEvent(context.Background(), sessionKey(before), before); err != nil {
		t.Fatal(err)
	}
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: base}}
	channel := newQueueTestChannel()
	runtime, logs := startGapTestRuntime(t, store, channel)

	// 离线消息现在才同步到 QQ 本地。
	channel.setResponse("get_group_msg_history", map[string]any{"messages": []any{
		historyTestMessage(101, base+60, "断线时的消息一"),
		historyTestMessage(102, base+120, "断线时的消息二"),
		historyTestMessage(103, base+180, "断线时的消息三"),
	}})
	if err := runtime.HandleEvent(context.Background(), seqTestEvent(104, base+240, "10002")); err != nil {
		t.Fatal(err)
	}

	waitForCondition(t, 4*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "backfill_gap_resolved")
	})
	for _, id := range []string{"group:123:101", "group:123:102", "group:123:103"} {
		if !store.hasEvent(id) {
			t.Fatalf("seq gap backfill did not enqueue %s", id)
		}
	}
	detected, ok := appLogEntry(logs.entriesSnapshot(), "backfill_gap_detected")
	if !ok || detected.Metadata["missing"] != 3 || detected.Metadata["previous_seq"] != int64(100) {
		t.Fatalf("gap detected entry = %#v", detected)
	}
}

func TestRuntimeSeqGapIgnoresBotOwnMessages(t *testing.T) {
	withGapTestDelays(t, nil, []time.Duration{50 * time.Millisecond})
	store := newMemoryInboundEventStore()
	base := time.Now().Add(-10 * time.Minute).Unix()
	for _, event := range []MessageEvent{
		seqTestEvent(100, base, "10001"),
		// 机器人自己的两条回复占了 101、102，本地记录不带 seq。
		{Kind: EventKindGroup, Time: base + 10, GroupID: "123", UserID: "42", MessageID: "self-1"},
		{Kind: EventKindGroup, Time: base + 20, GroupID: "123", UserID: "42", MessageID: "self-2"},
	} {
		if _, _, err := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event); err != nil {
			t.Fatal(err)
		}
	}
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: base}}
	channel := newQueueTestChannel()
	runtime, logs := startGapTestRuntime(t, store, channel)
	historyCalls := channel.callCount("get_group_msg_history")

	if err := runtime.HandleEvent(context.Background(), seqTestEvent(103, base+30, "10002")); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		runtime.seqProbeMu.Lock()
		defer runtime.seqProbeMu.Unlock()
		_, probed := runtime.seqProbed[groupSeqProbeKey(seqTestEvent(103, 0, ""))]
		return probed && len(runtime.seqGapRunning) == 0 && runtime.seqGapActive.Load() == 0
	})
	time.Sleep(200 * time.Millisecond)
	if hasAppLogAction(logs.entriesSnapshot(), "backfill_gap_detected") {
		t.Fatal("bot's own messages were reported as a seq gap")
	}
	if calls := channel.callCount("get_group_msg_history"); calls != historyCalls {
		t.Fatalf("history fetched %d extra times without a gap", calls-historyCalls)
	}
}

func TestRuntimeSeqGapReportsUnresolvedAfterRetries(t *testing.T) {
	withGapTestDelays(t, nil, []time.Duration{20 * time.Millisecond, 20 * time.Millisecond})
	store := newMemoryInboundEventStore()
	base := time.Now().Add(-10 * time.Minute).Unix()
	before := seqTestEvent(100, base, "10001")
	if _, _, err := store.EnqueueInboundEvent(context.Background(), sessionKey(before), before); err != nil {
		t.Fatal(err)
	}
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: base}}
	channel := newQueueTestChannel()
	runtime, logs := startGapTestRuntime(t, store, channel)
	historyCalls := channel.callCount("get_group_msg_history")

	if err := runtime.HandleEvent(context.Background(), seqTestEvent(105, base+60, "10002")); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 4*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "backfill_gap_unresolved")
	})
	if calls := channel.callCount("get_group_msg_history") - historyCalls; calls != 2 {
		t.Fatalf("targeted history fetches = %d, want one per retry (2)", calls)
	}
	entry, _ := appLogEntry(logs.entriesSnapshot(), "backfill_gap_unresolved")
	if entry.Level != applog.LevelError || entry.Metadata["missing"] != 4 {
		t.Fatalf("unresolved entry = %#v", entry)
	}
	if hasAppLogAction(logs.entriesSnapshot(), "backfill_gap_resolved") {
		t.Fatal("unresolved gap was also reported as resolved")
	}
}

// 私聊没有 seq、群里也可能再没人说话：重连后按断线窗口补跑，接住晚到的离线消息。
func TestRuntimeFollowUpBackfillCatchesLateSyncedHistory(t *testing.T) {
	withGapTestDelays(t, []time.Duration{200 * time.Millisecond}, nil)
	store := newMemoryInboundEventStore()
	base := time.Now().Add(-10 * time.Minute).Unix()
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: base}}
	channel := newQueueTestChannel()
	_, logs := startGapTestRuntime(t, store, channel)
	if store.hasEvent("group:123:970") {
		t.Fatal("message was available before QQ synced it")
	}

	// 这条消息早于第一次回补推进后的水位，常规回补不会再看它。
	channel.setResponse("get_group_msg_history", map[string]any{"messages": []any{
		historyTestMessage(970, time.Now().Add(-time.Minute).Unix(), "登录后才同步到的离线消息"),
	}})
	waitForCondition(t, 4*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "backfill_follow_up") && store.hasEvent("group:123:970")
	})
	waitForCondition(t, 4*time.Second, func() bool {
		for _, entry := range logs.entriesSnapshot() {
			if entry.Action == "backfill_completed" && entry.Metadata["inserted"] == 1 {
				return true
			}
		}
		return false
	})
}
