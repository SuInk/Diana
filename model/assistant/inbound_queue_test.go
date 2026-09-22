// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

func TestRuntimeDurableInboxSurvivesRestartAndDeduplicates(t *testing.T) {
	store := newMemoryInboundEventStore()
	event := queuedDirectTestEvent("incoming-1", time.Now().Unix())
	ingestRuntime := NewRuntime(BotConfig{BotAccount: "42", GroupTriggers: []string{"Diana"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	ingestRuntime.SetInboundEventStore(store)
	for i := 0; i < 2; i++ {
		if err := ingestRuntime.HandleEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if count, _ := store.PendingInboundCount(context.Background()); count != 1 {
		t.Fatalf("pending after duplicate ingest = %d, want 1", count)
	}

	channel := newQueueTestChannel()
	provider := &sequenceLLMProvider{replies: []string{`{"action":"none","prompt":""}`, "恢复成功"}}
	runtime := newQueuedTestRuntime(channel, store, provider)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 3*time.Second, func() bool { return channel.sentCount() == 1 })
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
	if count, _ := store.PendingInboundCount(context.Background()); count != 0 {
		t.Fatalf("pending after reply = %d, want 0", count)
	}

	restarted := newQueuedTestRuntime(channel, store, nil)
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if err := restarted.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := channel.sentCount(); got != 1 {
		t.Fatalf("completed event replayed after restart: sent=%d", got)
	}
}

func TestRuntimeBackfillsMissedHistoryIntoDurableQueue(t *testing.T) {
	store := newMemoryInboundEventStore()
	watermark := time.Now().Add(-10 * time.Minute).Unix()
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: watermark}}
	channel := newQueueTestChannel()
	channel.responses["get_group_list"] = map[string]any{"items": []any{map[string]any{"group_id": int64(123)}}}
	channel.responses["get_group_msg_history"] = map[string]any{"messages": []any{
		historyTestMessage(900, watermark-1, "Diana 已处理的旧水位"),
		historyTestMessage(901, watermark+1, "Diana 重启时漏掉的消息"),
	}}
	provider := &sequenceLLMProvider{replies: []string{`{"action":"none","prompt":""}`, "补回成功"}}
	runtime := newQueuedTestRuntime(channel, store, provider)
	if err := runtime.backfillInboundHistory(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if store.hasEvent("group:123:900") {
		t.Fatal("event older than the persisted watermark was queued")
	}
	if !store.hasEvent("group:123:901") {
		t.Fatal("missed history was not added to the durable queue")
	}
	if channel.sentCount() != 0 {
		t.Fatal("backfill should enqueue before workers process the message")
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 3*time.Second, func() bool {
		return channel.sentCount() == 1 && store.isDone("group:123:901")
	})
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
}

// historyTestMention 是一条 @ 机器人（self_id=42）的群消息。
func historyTestMention(messageID int64, eventTime int64, text string) map[string]any {
	message := historyTestMessage(messageID, eventTime, text)
	message["raw_message"] = "[CQ:at,qq=42] " + text
	message["message"] = []any{
		map[string]any{"type": "at", "data": map[string]any{"qq": "42"}},
		map[string]any{"type": "text", "data": map[string]any{"text": " " + text}},
	}
	return message
}

func backfillEventsByID(events []MessageEvent) map[string]MessageEvent {
	out := make(map[string]MessageEvent, len(events))
	for _, event := range events {
		out[event.MessageID] = event
	}
	return out
}

// 回补名额只管进回复流程的条数，不管往回翻多远：断线期间的消息全部补回来进上下文，
// 没有一条会触发时就一条都不占名额。以前是直接截最新 N 条，更早的连历史都进不去。
func TestHistoryBackfillKeepsWholeWindowButReservesReplySlotsForTriggers(t *testing.T) {
	channel := newQueueTestChannel()
	channel.responses["get_group_msg_history"] = map[string]any{"messages": []any{
		historyTestMessage(900, 900, "oldest"),
		historyTestMessage(901, 901, "older"),
		historyTestMessage(902, 902, "newer"),
		historyTestMessage(903, 903, "newest"),
	}}
	runtime := NewRuntime(BotConfig{HistoryBackfillMessageLimit: 3}, channel, NewPluginManager(), nil, nil, nil, nil)

	events, err := runtime.fetchHistorySince(context.Background(), HistorySession{Kind: EventKindGroup, ID: "123", LastEventTime: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("断线窗口里的消息都该补回来进历史：events=%d, want 4", len(events))
	}
	if events[0].MessageID != "900" || events[3].MessageID != "903" {
		t.Fatalf("回补结果没按时间排好：%#v", events)
	}
	for _, event := range events {
		if !event.BackfillHistoryOnly {
			t.Fatalf("没 @ 机器人的闲聊不该占回复名额：%s", event.MessageID)
		}
	}
}

// 就是这个场景：断线期间一条 @ 机器人之后又来了一串闲聊，那条 @ 不在最新三条里。
// 旧逻辑截最新三条就停，它既没人回也进不了历史；现在要一直翻到它，把名额给它。
func TestHistoryBackfillReachesTriggerBeyondNewestThree(t *testing.T) {
	channel := newQueueTestChannel()
	channel.responses["get_group_msg_history"] = map[string]any{"messages": []any{
		historyTestMention(900, 900, "帮我看看这个报错"),
		historyTestMessage(901, 901, "哈哈"),
		historyTestMessage(902, 902, "吃了吗"),
		historyTestMessage(903, 903, "刚下班"),
		historyTestMessage(904, 904, "今天好热"),
		historyTestMessage(905, 905, "是啊"),
	}}
	runtime := NewRuntime(BotConfig{BotAccount: "42", HistoryBackfillMessageLimit: 3}, channel, NewPluginManager(), nil, nil, nil, nil)

	events, err := runtime.fetchHistorySince(context.Background(), HistorySession{Kind: EventKindGroup, ID: "123", LastEventTime: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("events=%d, want 6", len(events))
	}
	byID := backfillEventsByID(events)
	if byID["900"].BackfillHistoryOnly {
		t.Fatal("最早那条 @ 机器人的消息应当拿到回复名额")
	}
	for _, id := range []string{"901", "902", "903", "904", "905"} {
		if !byID[id].BackfillHistoryOnly {
			t.Fatalf("闲聊 %s 应当只进历史", id)
		}
	}
}

// 会触发的消息超过名额时，只留最新的那几条进回复流程，更早的照样补进历史。
func TestHistoryBackfillReservesOnlyNewestTriggers(t *testing.T) {
	channel := newQueueTestChannel()
	channel.responses["get_group_msg_history"] = map[string]any{"messages": []any{
		historyTestMention(900, 900, "一"),
		historyTestMention(901, 901, "二"),
		historyTestMention(902, 902, "三"),
		historyTestMention(903, 903, "四"),
		historyTestMention(904, 904, "五"),
	}}
	runtime := NewRuntime(BotConfig{BotAccount: "42", HistoryBackfillMessageLimit: 3}, channel, NewPluginManager(), nil, nil, nil, nil)

	events, err := runtime.fetchHistorySince(context.Background(), HistorySession{Kind: EventKindGroup, ID: "123", LastEventTime: 1})
	if err != nil {
		t.Fatal(err)
	}
	byID := backfillEventsByID(events)
	for _, id := range []string{"902", "903", "904"} {
		if byID[id].BackfillHistoryOnly {
			t.Fatalf("最新的三条 @ 应当都拿到名额：%s", id)
		}
	}
	for _, id := range []string{"900", "901"} {
		if !byID[id].BackfillHistoryOnly {
			t.Fatalf("名额外更早的 @ 应当只进历史：%s", id)
		}
	}
}

func TestMarkBackfillReplyEligibleWalksNewestFirst(t *testing.T) {
	events := []MessageEvent{
		{MessageID: "a"}, {MessageID: "b"}, {MessageID: "c"}, {MessageID: "d"},
	}
	triggers := map[string]bool{"a": true, "c": true, "d": false}
	markBackfillReplyEligible(events, 1, func(event MessageEvent) bool { return triggers[event.MessageID] })
	// 从最新往回找：d 不触发，c 触发拿走唯一的名额，a 虽然触发但名额已用完。
	want := map[string]bool{"a": true, "b": true, "c": false, "d": true}
	for _, event := range events {
		if event.BackfillHistoryOnly != want[event.MessageID] {
			t.Fatalf("%s history-only=%v, want %v", event.MessageID, event.BackfillHistoryOnly, want[event.MessageID])
		}
	}
}

// 只进历史的回补消息在 worker 开头就收住：进上下文，不调模型、不发消息。
// 用一条本来会触发的 @ 来测，证明挡住它的是标记而不是触发判定。
func TestBackfillHistoryOnlyEventEntersContextWithoutReplying(t *testing.T) {
	channel := newQueueTestChannel()
	provider := &capturingLLMProvider{reply: "不应该回复"}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "10001", SelfID: "42",
		MessageID: "history-only", Time: time.Now().Unix(),
		RawMessage: "[CQ:at,qq=42] 在吗",
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "42"}},
			{Type: "text", Data: map[string]string{"text": " 在吗"}},
		},
		BackfillHistoryOnly: true,
	}

	outcome, err := runtime.processInboundQueueItem(context.Background(), InboundQueueItem{ID: "q1", Event: event})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "backfill_history_only" {
		t.Fatalf("outcome=%q", outcome)
	}
	if len(provider.requestSnapshot().Messages) != 0 {
		t.Fatalf("只进历史的消息不该调模型：%#v", provider.requestSnapshot())
	}
	if calls := channel.callCount("send_group_msg"); calls != 0 {
		t.Fatalf("只进历史的消息不该发消息：send_group_msg=%d", calls)
	}
	found := false
	for _, item := range runtime.contextHistory(event) {
		if item.MessageID == "history-only" {
			found = true
		}
	}
	if !found {
		t.Fatal("消息没有进上下文历史")
	}
}

func TestHistoryBackfillDoesNotSendTelegramSessionsToOneBot(t *testing.T) {
	store := newMemoryInboundEventStore()
	store.sessions = []HistorySession{{
		Kind: EventKindGroup, ID: "-5425672870", Platform: PlatformTelegram,
		ProfileID: "telegram-bot", LastEventTime: 10,
	}}
	channel := newQueueTestChannel()
	runtime := NewRuntime(BotConfig{Platform: PlatformOneBotV11}, channel, NewPluginManager(), nil, nil, nil, nil)

	sessions, err := runtime.backfillInboundHistoryFromSessions(context.Background(), store, store.sessions, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("Telegram sessions entered OneBot backfill: %#v", sessions)
	}
	// Runtime-wide OneBot discovery may still list its own groups, but the
	// Telegram session itself must never reach a OneBot history endpoint.
	for _, action := range []string{"get_group_msg_history", "get_friend_msg_history"} {
		if calls := channel.callCount(action); calls != 0 {
			t.Fatalf("%s called %d times for Telegram history", action, calls)
		}
	}
}

func TestHistoryBackfillDropsHistoricalPrivateOutsideRecentContacts(t *testing.T) {
	store := newMemoryInboundEventStore()
	store.sessions = []HistorySession{
		{Kind: EventKindGroup, ID: "123", LastEventTime: 10},
		{Kind: EventKindPrivate, ID: "30007", LastEventTime: 10},
	}
	channel := newQueueTestChannel()
	channel.responses["get_group_list"] = map[string]any{"items": []any{map[string]any{"group_id": "123"}}}
	channel.responses["get_recent_contact"] = map[string]any{"items": []any{}}
	runtime := newQueuedTestRuntime(channel, store, nil)

	sessions, err := runtime.backfillInboundHistoryFromSessions(context.Background(), store, store.sessions, 10)
	if err != nil {
		t.Fatal(err)
	}
	if channel.callCount("get_friend_msg_history") != 0 {
		t.Fatal("stale historical private session was fetched without a recent-contact match")
	}
	if len(sessions) != 1 || sessions[0].Kind != EventKindGroup || sessions[0].ID != "123" {
		t.Fatalf("sessions=%#v", sessions)
	}
}

func TestHistoryBackfillSkipsUnresolvableRecentPrivate(t *testing.T) {
	store := newMemoryInboundEventStore()
	store.sessions = []HistorySession{{Kind: EventKindPrivate, ID: "30007", LastEventTime: 10}}
	channel := newQueueTestChannel()
	channel.responses["get_recent_contact"] = map[string]any{"items": []any{
		map[string]any{"peerUin": "30007", "chatType": 1},
	}}
	channel.errors["get_friend_msg_history"] = fmt.Errorf("failed to resolve UID for UIN 30007")
	runtime := newQueuedTestRuntime(channel, store, nil)

	sessions, err := runtime.backfillInboundHistoryFromSessions(context.Background(), store, store.sessions, 10)
	if err != nil {
		t.Fatalf("permanent stale-private error blocked backfill: %v", err)
	}
	if len(sessions) != 1 || channel.callCount("get_friend_msg_history") != 1 {
		t.Fatalf("sessions=%#v calls=%d", sessions, channel.callCount("get_friend_msg_history"))
	}
}

func TestHistoryBackfillStillRetriesTransientPrivateFailure(t *testing.T) {
	store := newMemoryInboundEventStore()
	store.sessions = []HistorySession{{Kind: EventKindPrivate, ID: "10001", LastEventTime: 10}}
	channel := newQueueTestChannel()
	channel.responses["get_recent_contact"] = map[string]any{"items": []any{
		map[string]any{"peerUin": "10001", "chatType": 1},
	}}
	channel.errors["get_friend_msg_history"] = context.DeadlineExceeded
	runtime := newQueuedTestRuntime(channel, store, nil)

	if _, err := runtime.backfillInboundHistoryFromSessions(context.Background(), store, store.sessions, 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transient error=%v", err)
	}
}

func TestHistoryBackfillBindsActiveProfileContext(t *testing.T) {
	store := newMemoryInboundEventStore()
	channel := NewMultiChannel([]ChannelBinding{{
		ProfileID: "onebot-main",
		Platform:  PlatformOneBotV11,
		Channel:   newQueueTestChannel(),
	}})
	runtime := NewRuntime(BotConfig{
		ID:         "onebot-main",
		Platform:   PlatformOneBotV11,
		BotAccount: "42",
	}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetInboundEventStore(store)

	event, ok := runtime.historyEventFromData(HistorySession{Kind: EventKindGroup, ID: "123"}, historyTestMessage(901, time.Now().Unix(), "Diana 看看"))
	if !ok {
		t.Fatal("history event was not parsed")
	}
	if event.ProfileID != "onebot-main" || event.Platform != PlatformOneBotV11 || event.ContextNamespace != "onebot-main" {
		t.Fatalf("backfilled identity = profile:%q platform:%q namespace:%q", event.ProfileID, event.Platform, event.ContextNamespace)
	}
	if _, inserted, err := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event); err != nil || !inserted {
		t.Fatalf("enqueue backfilled event inserted=%v err=%v", inserted, err)
	}
	if !store.hasEvent("onebot-main:group:123:901") {
		t.Fatal("backfilled event entered the legacy unnamespaced session")
	}
}

func TestRuntimeBackfillsGroupRecallForLocallyStoredMessage(t *testing.T) {
	inboundStore := newMemoryInboundEventStore()
	watermark := time.Now().Add(-10 * time.Minute).Unix()
	inboundStore.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: watermark}}
	historyStore := newRecallPersistenceStore()
	original := MessageEvent{
		Kind:       EventKindGroup,
		Time:       watermark + 1,
		SelfID:     "42",
		GroupID:    "123",
		UserID:     "10001",
		MessageID:  "901",
		MessageSeq: "901",
		RawMessage: "断线期间被撤回的原文",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "断线期间被撤回的原文"}}},
		SenderName: "Alice",
	}
	if err := historyStore.AppendMessageEvent(context.Background(), "group:123", original); err != nil {
		t.Fatal(err)
	}

	channel := newQueueTestChannel()
	channel.responses["get_group_msg_history"] = map[string]any{"messages": []any{
		map[string]any{
			"time": watermark + 1, "self_id": int64(42), "message_type": "group",
			"group_id": int64(123), "user_id": int64(10001), "message_id": int64(901),
			"message_seq": int64(901), "raw_message": "", "message": []any{},
		},
	}}
	runtime := newQueuedTestRuntime(channel, inboundStore, nil)
	runtime.SetMessageHistoryStore(historyStore)

	if err := runtime.backfillInboundHistory(context.Background(), inboundStore); err != nil {
		t.Fatal(err)
	}
	recalls, err := historyStore.ListGroupRecallEvents(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if len(recalls) != 1 {
		t.Fatalf("recovered recalls=%#v, want one", recalls)
	}
	if recalls[0].RawMessage != original.RawMessage || recalls[0].OperatorRole != historyBackfillOperatorRole {
		t.Fatalf("recovered recall=%#v", recalls[0])
	}
	if inboundStore.hasEvent("group:123:901") {
		t.Fatal("empty recall marker was queued as an ordinary message")
	}

	if err := runtime.backfillInboundHistory(context.Background(), inboundStore); err != nil {
		t.Fatal(err)
	}
	recalls, err = historyStore.ListGroupRecallEvents(context.Background(), "123")
	if err != nil || len(recalls) != 1 {
		t.Fatalf("duplicate recovery recalls=%#v err=%v", recalls, err)
	}
}

func TestHistoryMessageIsEmptyRequiresAnExplicitEmptyMessage(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, MessageID: "901"}
	if !historyMessageIsEmpty(map[string]any{"message": []any{}}, event) {
		t.Fatal("explicit empty history message was not recognized")
	}
	if historyMessageIsEmpty(map[string]any{}, event) {
		t.Fatal("missing message field was treated as an empty history message")
	}
	event.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "正文"}}}
	if historyMessageIsEmpty(map[string]any{"message": []any{}}, event) {
		t.Fatal("message with parsed content was treated as empty")
	}
}

func TestRuntimeObservesConnectionEpochChangesWithoutDisconnectedEdge(t *testing.T) {
	store := newMemoryInboundEventStore()
	channel := newQueueTestChannel()
	logs := &captureAppLogs{}
	runtime := newQueuedTestRuntime(channel, store, nil)
	runtime.SetAppLogWriter(logs)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()

	waitForCondition(t, 2*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "connection_opened")
	})
	channel.bumpConnectionEpoch()
	waitForCondition(t, 2*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "reconnected")
	})
}

func TestRuntimeBackfillKeepsPreReconnectWatermarkWhenLiveMessageArrivesFirst(t *testing.T) {
	store := newMemoryInboundEventStore()
	watermark := time.Now().Add(-10 * time.Minute).Unix()
	baseline := []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: watermark}}
	// This live event arrives immediately after reconnect. A fresh database
	// watermark would now sit after the missed event and incorrectly skip it.
	live := queuedDirectTestEvent("902", watermark+20)
	live.GroupID = "123"
	if _, inserted, err := store.EnqueueInboundEvent(context.Background(), sessionKey(live), live); err != nil || !inserted {
		t.Fatalf("enqueue live event inserted=%v err=%v", inserted, err)
	}
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: watermark + 20}}

	channel := newQueueTestChannel()
	channel.responses["get_group_msg_history"] = map[string]any{"messages": []any{
		historyTestMessage(901, watermark+10, "Diana 断线时漏掉的消息"),
		historyTestMessage(902, watermark+20, "Diana 重连后实时收到的消息"),
	}}
	runtime := newQueuedTestRuntime(channel, store, nil)
	if _, err := runtime.backfillInboundHistoryFromSessions(context.Background(), store, baseline, watermark); err != nil {
		t.Fatal(err)
	}
	if !store.hasEvent("group:123:901") {
		t.Fatal("missed message was skipped after a newer live event advanced the database watermark")
	}
}

func TestRuntimeBackfillsAgainWhenConnectionEpochChangesWithoutDisconnectEdge(t *testing.T) {
	store := newMemoryInboundEventStore()
	watermark := time.Now().Add(-10 * time.Minute).Unix()
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: watermark}}
	channel := newQueueTestChannel()
	channel.responses["get_group_msg_history"] = map[string]any{"messages": []any{
		historyTestMessage(910, watermark+1, "Diana 第一次连接漏掉的消息"),
	}}
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":""}`, "第一次补回成功",
		`{"action":"none","prompt":""}`, "第二次补回成功",
	}}
	runtime := newQueuedTestRuntime(channel, store, provider)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()
	waitForCondition(t, 4*time.Second, func() bool { return channel.sentCount() == 1 })

	secondTime := time.Now().Unix()
	channel.setResponse("get_group_msg_history", map[string]any{"messages": []any{
		historyTestMessage(910, watermark+1, "Diana 第一次连接漏掉的消息"),
		historyTestMessage(911, secondTime, "Diana 连接替换时漏掉的消息"),
	}})
	channel.bumpConnectionEpoch()
	waitForCondition(t, 4*time.Second, func() bool { return channel.sentCount() == 2 })
	if !store.hasEvent("group:123:911") {
		t.Fatal("connection replacement did not schedule a second history backfill")
	}
}

func TestRuntimeBackfillsWhenAccountRecoversWithoutWSReconnect(t *testing.T) {
	store := newMemoryInboundEventStore()
	watermark := time.Now().Add(-10 * time.Minute).Unix()
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: watermark}}
	channel := newQueueTestChannel()
	logs := &captureAppLogs{}
	runtime := newQueuedTestRuntime(channel, store, nil)
	runtime.SetAppLogWriter(logs)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()

	waitForCondition(t, 4*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "backfill_completed")
	})

	// QQ 被风控：WS 连接保持，心跳报告账号离线。
	channel.setAccountStatus(true, false, false)
	waitForCondition(t, 2*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "account_offline")
	})
	if runtime.inboundProcessingReady() {
		t.Fatal("inbound processing stayed ready while the account was offline")
	}

	// 封控期间漏掉的消息只能通过解封后的历史回补拿到。
	channel.setResponse("get_group_msg_history", map[string]any{"messages": []any{
		historyTestMessage(950, time.Now().Unix(), "封控期间漏掉的消息"),
	}})

	// 解封：账号恢复在线，epoch 与连接状态均未变化。
	channel.setAccountStatus(true, true, true)
	waitForCondition(t, 4*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "account_recovered")
	})
	waitForCondition(t, 4*time.Second, func() bool {
		return store.hasEvent("group:123:950")
	})
}

func TestRuntimeManualBackfillRewindsWatermarkWithinWindow(t *testing.T) {
	store := newMemoryInboundEventStore()
	now := time.Now().Unix()
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: now}}
	channel := newQueueTestChannel()
	logs := &captureAppLogs{}
	runtime := newQueuedTestRuntime(channel, store, nil)
	runtime.SetAppLogWriter(logs)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()
	waitForCondition(t, 4*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "backfill_completed")
	})

	// 这条消息早于当前水位，常规回补不会再看它，只有手动回退水位才能找回。
	channel.setResponse("get_group_msg_history", map[string]any{"messages": []any{
		historyTestMessage(960, now-1800, "手动回补找回的消息"),
	}})
	if err := runtime.RequestHistoryBackfill(time.Hour); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 4*time.Second, func() bool {
		return hasAppLogAction(logs.entriesSnapshot(), "backfill_manual_requested")
	})
	waitForCondition(t, 4*time.Second, func() bool {
		return store.hasEvent("group:123:960")
	})
}

func TestRuntimeManualBackfillDoesNotReplyToDuplicates(t *testing.T) {
	store := newMemoryInboundEventStore()
	watermark := time.Now().Add(-10 * time.Minute).Unix()
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: watermark}}
	channel := newQueueTestChannel()
	channel.responses["get_group_msg_history"] = map[string]any{"messages": []any{
		historyTestMessage(920, watermark+1, "Diana 只应回复一次的消息"),
	}}
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":""}`, "第一次回复",
		`{"action":"none","prompt":""}`, "不应出现的第二次回复",
	}}
	logs := &captureAppLogs{}
	runtime := newQueuedTestRuntime(channel, store, provider)
	runtime.SetAppLogWriter(logs)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()
	waitForCondition(t, 4*time.Second, func() bool { return channel.sentCount() == 1 })

	// 手动回补会把水位回退，再次拉到同一条消息，但入队去重必须挡住二次回复。
	if err := runtime.RequestHistoryBackfill(time.Hour); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 4*time.Second, func() bool {
		return countAppLogAction(logs.entriesSnapshot(), "backfill_completed") >= 2
	})
	time.Sleep(500 * time.Millisecond)
	if got := channel.sentCount(); got != 1 {
		t.Fatalf("duplicate backfilled message produced %d replies, want 1", got)
	}
}

func TestChannelEffectivelyOnlineRequiresHealthyAccount(t *testing.T) {
	if channelEffectivelyOnline(ChannelStatus{Connected: true, AccountStatusKnown: true, AccountOnline: false}) {
		t.Fatal("offline account must not count as online")
	}
	if channelEffectivelyOnline(ChannelStatus{Connected: true, AccountStatusKnown: true, AccountOnline: true, AccountGood: false}) {
		t.Fatal("degraded account must not count as online")
	}
	if !channelEffectivelyOnline(ChannelStatus{Connected: true}) {
		t.Fatal("unknown account state must fall back to transport connectivity")
	}
	if channelEffectivelyOnline(ChannelStatus{Connected: false, AccountStatusKnown: true, AccountOnline: true, AccountGood: true}) {
		t.Fatal("healthy account cannot compensate for a dropped transport")
	}
}

func TestRuntimeInboundStatusUsesOneBotChannelInMultiChannel(t *testing.T) {
	onebot := &multiChannelProbe{status: ChannelStatus{
		Connected:       false,
		ConnectionEpoch: 4,
	}}
	telegram := &multiChannelProbe{status: ChannelStatus{Connected: true}}
	runtime := NewRuntime(BotConfig{ID: "telegram", Platform: PlatformTelegram}, NewMultiChannel([]ChannelBinding{
		{ProfileID: "qq", Platform: PlatformOneBotV11, Channel: onebot},
		{ProfileID: "telegram", Platform: PlatformTelegram, Channel: telegram},
	}), NewPluginManager(), nil, nil, nil, nil)

	status := runtime.channelStatus()
	if status.Connected {
		t.Fatal("Telegram connectivity must not mark the OneBot inbound queue ready")
	}
	if status.Platform != PlatformOneBotV11 || status.ProfileID != "qq" || status.ConnectionEpoch != 4 {
		t.Fatalf("inbound status = %#v, want OneBot profile status", status)
	}
}

func hasAppLogAction(entries []applog.Entry, action string) bool {
	return countAppLogAction(entries, action) > 0
}

func countAppLogAction(entries []applog.Entry, action string) int {
	count := 0
	for _, entry := range entries {
		if entry.Action == action {
			count++
		}
	}
	return count
}

func TestRuntimeDrainsPendingWhileHistoryBackfillIsSlow(t *testing.T) {
	store := newMemoryInboundEventStore()
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: 100}}
	event := queuedDirectTestEvent("pending-before-restart", time.Now().Unix())
	if _, inserted, err := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	channel := newBlockingHistoryChannel()
	provider := &sequenceLLMProvider{replies: []string{`{"action":"none","prompt":""}`, "队列先恢复"}}
	runtime := newQueuedTestRuntime(channel, store, provider)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	go func() { _ = runtime.backfillInboundHistory(context.Background(), store) }()
	waitForSignal(t, channel.historyStarted)
	waitForCondition(t, 2*time.Second, func() bool { return channel.sentCount() == 1 })
	close(channel.releaseHistory)
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRecoversProcessingMessageWithinReplayWindowAfterRestart(t *testing.T) {
	store := newMemoryInboundEventStore()
	event := queuedDirectTestEvent("old-processing", time.Now().Add(-90*time.Minute).Unix())
	store.sessions = []HistorySession{{Kind: EventKindGroup, ID: "123", LastEventTime: event.Time}}
	if _, inserted, err := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	if _, ok, err := store.ClaimNextInboundEvent(context.Background(), "dead-runtime", time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("pre-restart claim ok=%v err=%v", ok, err)
	}
	channel := newQueueTestChannel()
	runtime := newQueuedTestRuntime(channel, store, &sequenceLLMProvider{replies: []string{`{"action":"none","prompt":""}`, "旧消息恢复成功"}})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 3*time.Second, func() bool {
		return channel.sentCount() == 1 && store.isDone("group:123:old-processing")
	})
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
	if count, _ := store.PendingInboundCount(context.Background()); count != 0 {
		t.Fatalf("pending after restart recovery=%d, want 0", count)
	}
}

func TestRuntimeDoesNotReplyBeyondInboundReplayWindow(t *testing.T) {
	channel := newQueueTestChannel()
	runtime := newQueuedTestRuntime(channel, newMemoryInboundEventStore(), &sequenceLLMProvider{replies: []string{"不应调用"}})
	event := queuedDirectTestEvent("expired", time.Now().Add(-InboundReplayWindow-time.Minute).Unix())
	outcome, err := runtime.processInboundQueueItem(context.Background(), InboundQueueItem{Event: event})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "ignored_stale" {
		t.Fatalf("outcome=%q, want ignored_stale", outcome)
	}
	if channel.sentCount() != 0 {
		t.Fatal("message older than the replay window triggered a reply")
	}
}

func TestInboundReplayCutoffFollowsOfflineDurationWithPaddingAndCap(t *testing.T) {
	reconnectedAt := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)

	if got, want := inboundReplayCutoff(reconnectedAt.Add(-3*time.Hour), reconnectedAt), reconnectedAt.Add(-3*time.Hour-inboundReplayPadding); !got.Equal(want) {
		t.Fatalf("three-hour cutoff=%s, want %s", got, want)
	}
	if got, want := inboundReplayCutoff(reconnectedAt.Add(-25*time.Hour), reconnectedAt), reconnectedAt.Add(-InboundReplayWindow); !got.Equal(want) {
		t.Fatalf("capped cutoff=%s, want %s", got, want)
	}
}

func TestRuntimeInboundReplayUsesStableReconnectCutoff(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	cutoff := time.Date(2026, time.August, 15, 8, 0, 0, 0, time.UTC)
	runtime.setInboundReplayCutoff(cutoff)

	withinWindow := queuedDirectTestEvent("within", cutoff.Add(time.Second).Unix())
	if runtime.inboundEventIsStale(withinWindow, cutoff.Add(11*time.Hour)) {
		t.Fatal("message after the reconnect cutoff became stale while waiting in the queue")
	}
	beforeWindow := queuedDirectTestEvent("before", cutoff.Add(-time.Second).Unix())
	if !runtime.inboundEventIsStale(beforeWindow, cutoff.Add(time.Minute)) {
		t.Fatal("message before the reconnect cutoff was accepted")
	}
}

func TestHistoryBackfillPaddingDoesNotExceedReplayCutoff(t *testing.T) {
	cutoff := time.Unix(1_000, 0)
	sessions := []HistorySession{
		{Kind: EventKindGroup, ID: "near", LastEventTime: 1_100},
		{Kind: EventKindGroup, ID: "far", LastEventTime: 3_000},
	}
	padded := historyBackfillBaselineWithPadding(sessions, cutoff)
	if padded[0].LastEventTime != cutoff.Unix() {
		t.Fatalf("near watermark=%d, want cutoff %d", padded[0].LastEventTime, cutoff.Unix())
	}
	if padded[1].LastEventTime != 3_000-int64(inboundReplayPadding/time.Second) {
		t.Fatalf("far watermark=%d", padded[1].LastEventTime)
	}
	if sessions[0].LastEventTime != 1_100 {
		t.Fatal("padding mutated the coordinator baseline")
	}
}

// 引用机器人的消息直接进回复流程，不再交给接话评分：被引用就该理人。
func TestReplyToBotTriggersReplyDirectly(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return &capturingLLMProvider{}, nil
	})
	event := MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "123",
		UserID:    "10001",
		MessageID: "reply-1",
		Quoted:    &QuotedMessage{MessageID: "bot-1", UserID: "42"},
		Segments:  []MessageSegment{{Type: "text", Data: map[string]string{"text": "再说一下"}}},
	}
	event = runtime.enrichReplyReference(context.Background(), event)
	if !event.ToMe {
		t.Fatal("replying to the bot should remain addressed to the bot")
	}
	if !runtime.shouldHandleChat(event, "再说一下") {
		t.Fatal("引用机器人的消息应当直接进回复流程")
	}
}

func TestProactiveReplyRouterUsesStrictSemanticTimeout(t *testing.T) {
	if got := proactiveReplyRouteTimeout(BotConfig{RequestTimeout: 5 * time.Minute}); got != 60*time.Second {
		t.Fatalf("route timeout = %s, want 60s", got)
	}
	if got := proactiveReplyRouteTimeout(BotConfig{RequestTimeout: 3 * time.Minute}); got != 60*time.Second {
		t.Fatalf("route timeout = %s, want 60s", got)
	}
	if got := proactiveReplyRouteTimeout(BotConfig{RequestTimeout: 8 * time.Second}); got != 8*time.Second {
		t.Fatalf("short configured timeout = %s, want 8s", got)
	}
}

func TestRuntimeAssignsInboundPriorities(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	tests := []struct {
		name  string
		event MessageEvent
		want  int
	}{
		{
			name: "direct trigger",
			event: MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "1", ToMe: true,
				Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "帮我看看"}}}},
			want: InboundPriorityTriggered,
		},
		{
			name: "quoted reply",
			event: MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "1", Quoted: &QuotedMessage{MessageID: "9", UserID: "2"},
				Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "接着说"}}}},
			want: InboundPriorityReply,
		},
		{
			name: "resolver",
			event: MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "1",
				Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "https://www.bilibili.com/video/BV1Gc7K6UEgz/"}}}},
			want: InboundPriorityResolver,
		},
		{
			name: "ordinary chat",
			event: MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "1",
				Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "今天天气还行"}}}},
			want: InboundPriorityNormal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runtime.inboundPriority(tt.event); got != tt.want {
				t.Fatalf("priority=%d, want %d", got, tt.want)
			}
		})
	}
}

func TestProactiveReplyRouterRetriesTransientErrorOnce(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "cheap-primary", Group: "cheap", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "cheap-primary"}},
		},
	}}
	provider := &countingErrorLLMProvider{err: errors.New("502 Bad Gateway")}
	runtime := NewRuntime(BotConfig{BotAccount: "42", ProactiveReplyChance: 1}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	var configuredModels []string
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		configuredModels = append(configuredModels, cfg.Model)
		return provider, nil
	})
	event := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123",
		UserID:     "1",
		MessageID:  "proactive-1",
		RawMessage: "这个问题该怎么处理？",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "这个问题该怎么处理？"}}},
	}
	if runtime.shouldHandleProactiveReply(context.Background(), event, event.RawMessage) {
		t.Fatal("failed proactive route unexpectedly allowed a reply")
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls=%d, want initial request plus one retry", provider.calls)
	}
	if len(configuredModels) != 1 || configuredModels[0] != "cheap-primary" {
		t.Fatalf("configured models=%#v, want only cheap-primary", configuredModels)
	}
}

func TestMainLLMProviderRetriesTimeoutOnce(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "main", Group: "default", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "main"}},
		},
	}}
	provider := &countingErrorLLMProvider{err: fmt.Errorf("request failed: %w", context.DeadlineExceeded)}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) {
		return provider, nil
	})
	_, err := runtime.runLLMProvider(context.Background(), func(client LLMProvider) (string, error) {
		_, runErr := client.Generate(context.Background(), llm.GenerateRequest{})
		return "", runErr
	})
	if err == nil {
		t.Fatal("timeout retry unexpectedly succeeded")
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls=%d, want initial request plus one retry", provider.calls)
	}
}

func TestDefaultBotConcurrencyIsEight(t *testing.T) {
	if got := (BotConfig{}).WithDefaults().MaxBotConcurrency; got != 8 {
		t.Fatalf("MaxBotConcurrency=%d, want 8", got)
	}
}

func newQueuedTestRuntime(channel Channel, store InboundEventStore, provider LLMProvider) *Runtime {
	var factory LLMProviderFactory
	if provider != nil {
		factory = func() (LLMProvider, error) { return provider, nil }
	}
	runtime := NewRuntime(BotConfig{Enabled: true, BotAccount: "42", GroupTriggers: []string{"Diana"}, OneBotAccessToken: "test-token"}, channel, NewPluginManager(), nil, nil, nil, factory)
	runtime.SetInboundEventStore(store)
	return runtime
}

func queuedDirectTestEvent(messageID string, eventTime int64) MessageEvent {
	return MessageEvent{
		Kind:       EventKindGroup,
		Time:       eventTime,
		SelfID:     "42",
		GroupID:    "123",
		UserID:     "10001",
		MessageID:  messageID,
		RawMessage: "Diana 帮我看看",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana 帮我看看"}}},
	}
}

func historyTestMessage(messageID int64, eventTime int64, text string) map[string]any {
	return map[string]any{
		"time":         eventTime,
		"self_id":      int64(42),
		"message_type": "group",
		"group_id":     int64(123),
		"user_id":      int64(10001),
		"message_id":   messageID,
		"message_seq":  messageID,
		"raw_message":  text,
		"message":      []any{map[string]any{"type": "text", "data": map[string]any{"text": text}}},
	}
}

type memoryInboundRecord struct {
	item       InboundQueueItem
	state      string
	outcome    string
	leaseOwner string
}

func TestAttachInboundTurnMediaPreservesSourcesAndRealSegments(t *testing.T) {
	question := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "question-1",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "图片里是什么？"}}},
	}
	sources := []MessageEvent{
		{MessageID: "image-1", Segments: []MessageSegment{{Type: "image", Data: map[string]string{"cached_file": "/tmp/image-1.png"}}}},
		{MessageID: "video-1", Segments: []MessageSegment{{Type: "video", Data: map[string]string{"file": "/tmp/video-1.mp4"}}}},
	}
	got := attachInboundTurnMedia(question, sources)
	if len(got.Segments) != 3 {
		t.Fatalf("segments=%#v", got.Segments)
	}
	if strings.Join(eventSemanticSourceMessageIDs(got), ",") != "image-1,video-1" {
		t.Fatalf("source ids=%#v", eventSemanticSourceMessageIDs(got))
	}
	if got.Segments[1].Data["source_message_id"] != "image-1" || got.Segments[2].Data["source_message_id"] != "video-1" {
		t.Fatalf("source metadata missing: %#v", got.Segments)
	}
	if len(question.Segments) != 1 {
		t.Fatalf("original event mutated: %#v", question.Segments)
	}
}

func TestInboundMediaTurnClassificationUsesSegments(t *testing.T) {
	for _, segmentType := range []string{"image", "video", "file", "record"} {
		event := MessageEvent{Segments: []MessageSegment{{Type: segmentType, Data: map[string]string{"file": "media.bin"}}}}
		if !EventIsMergeableMediaOnly(event) {
			t.Fatalf("%s-only event was not mergeable", segmentType)
		}
		event.Segments = append(event.Segments, MessageSegment{Type: "text", Data: map[string]string{"text": "独立说明"}})
		if EventIsMergeableMediaOnly(event) {
			t.Fatalf("%s event with text was treated as media-only", segmentType)
		}
	}
}

func TestInboundMediaSupersessionBlocksFinalSend(t *testing.T) {
	store := newMemoryInboundEventStore()
	store.superseded["media-1"] = "turn-1"
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetInboundEventStore(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "media-1"}
	_, err := runtime.sendOutgoingWithResult(context.Background(), event, OutgoingMessage{GroupID: "group-1", Text: "stale description"})
	if !errors.Is(err, errInboundTurnSuperseded) {
		t.Fatalf("send error=%v", err)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("superseded reply was sent: %#v", channel.sent)
	}
}

type countingErrorLLMProvider struct {
	calls int
	err   error
}

func (p *countingErrorLLMProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls++
	return nil, p.err
}

type memoryInboundEventStore struct {
	mu         sync.Mutex
	records    map[string]*memoryInboundRecord
	order      []string
	sessions   []HistorySession
	audits     []EventRecord
	media      []MessageEvent
	superseded map[string]string
	steps      map[string]string
}

func newMemoryInboundEventStore() *memoryInboundEventStore {
	return &memoryInboundEventStore{records: map[string]*memoryInboundRecord{}, superseded: map[string]string{}, steps: map[string]string{}}
}

func (s *memoryInboundEventStore) PeekInboundMediaForTurn(_ context.Context, _, _ string, _ MessageEvent, _ time.Duration) ([]MessageEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]MessageEvent(nil), s.media...), nil
}

func (s *memoryInboundEventStore) ClaimInboundMediaForTurn(_ context.Context, currentID, _ string, _ MessageEvent, _ time.Duration) ([]MessageEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	claimed := append([]MessageEvent(nil), s.media...)
	for _, source := range claimed {
		s.superseded[source.MessageID] = currentID
	}
	s.media = nil
	return claimed, nil
}

func (s *memoryInboundEventStore) InboundEventSuperseded(_ context.Context, event MessageEvent) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	turnID := s.superseded[event.MessageID]
	return turnID, turnID != "", nil
}

func (s *memoryInboundEventStore) EnqueueInboundEvent(_ context.Context, session string, event MessageEvent, priorities ...int) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := session + ":" + event.MessageID
	if _, ok := s.records[id]; ok {
		return id, false, nil
	}
	priority := InboundPriorityNormal
	if len(priorities) > 0 {
		priority = priorities[0]
	}
	s.records[id] = &memoryInboundRecord{item: InboundQueueItem{ID: id, Session: session, Event: event, Priority: priority}, state: "pending"}
	s.order = append(s.order, id)
	return id, true, nil
}

func (s *memoryInboundEventStore) ClaimNextInboundEvent(_ context.Context, leaseOwner string, _ time.Time, limits ...InboundConcurrency) (InboundQueueItem, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	concurrency := InboundConcurrency{Group: 1, Private: 1}
	if len(limits) > 0 {
		if limits[0].Group > 0 {
			concurrency.Group = limits[0].Group
		}
		if limits[0].Private > 0 {
			concurrency.Private = limits[0].Private
		}
	}
	selectedID := ""
	for _, id := range s.order {
		record := s.records[id]
		if record.state != "pending" {
			continue
		}
		limit := concurrency.Private
		if record.item.Event.Kind == EventKindGroup {
			limit = concurrency.Group
		}
		active := 0
		for _, candidate := range s.records {
			if candidate.state == "processing" && candidate.item.Session == record.item.Session {
				active++
			}
		}
		if active >= limit {
			continue
		}
		if selectedID == "" || record.item.Priority > s.records[selectedID].item.Priority {
			selectedID = id
		}
	}
	if selectedID == "" {
		return InboundQueueItem{}, false, nil
	}
	record := s.records[selectedID]
	record.state = "processing"
	record.leaseOwner = leaseOwner
	record.item.Attempts++
	return record.item, true, nil
}

func (s *memoryInboundEventStore) CompleteInboundEvent(_ context.Context, id string, leaseOwner string, outcome string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record := s.records[id]; record != nil && record.state == "processing" && record.leaseOwner == leaseOwner {
		record.state = "done"
		record.outcome = outcome
		record.leaseOwner = ""
	}
	return nil
}

func (s *memoryInboundEventStore) OutboundStepDelivered(_ context.Context, turnID, stepKey string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	messageID, ok := s.steps[turnID+"\x00"+stepKey]
	return messageID, ok, nil
}

func (s *memoryInboundEventStore) RecordOutboundStep(_ context.Context, turnID, stepKey, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.steps == nil {
		s.steps = map[string]string{}
	}
	s.steps[turnID+"\x00"+stepKey] = messageID
	return nil
}

func (s *memoryInboundEventStore) ClearOutboundSteps(_ context.Context, turnID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.steps {
		if strings.HasPrefix(key, turnID+"\x00") {
			delete(s.steps, key)
		}
	}
	return nil
}

func (s *memoryInboundEventStore) RetryInboundEvent(_ context.Context, id string, leaseOwner string, _ time.Time, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record := s.records[id]; record != nil && record.state == "processing" && record.leaseOwner == leaseOwner {
		record.state = "pending"
		record.leaseOwner = ""
	}
	return nil
}

func (s *memoryInboundEventStore) ReleaseInboundLeases(_ context.Context, leaseOwner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.state == "processing" && (leaseOwner == "" || record.leaseOwner == leaseOwner) {
			record.state = "pending"
			record.leaseOwner = ""
		}
	}
	return nil
}

func (s *memoryInboundEventStore) PendingInboundCount(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, record := range s.records {
		if record.state != "done" {
			count++
		}
	}
	return count, nil
}

func (s *memoryInboundEventStore) GroupHistoryWatermark(context.Context, string) (int64, bool, error) {
	return 0, false, nil
}

func (s *memoryInboundEventStore) ListHistorySessions(context.Context) ([]HistorySession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]HistorySession(nil), s.sessions...), nil
}

func (s *memoryInboundEventStore) RecordInboundEventAudit(_ context.Context, event EventRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, event)
	return nil
}

func TestRuntimeRecordPersistsInboundReasonSynchronously(t *testing.T) {
	store := newMemoryInboundEventStore()
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetInboundEventStore(store)
	runtime.record(EventRecord{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1",
		Decision: "not_replied", Reason: "主动回复判断不建议回复：普通闲聊无需插话",
	})
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.audits) != 1 || store.audits[0].Reason != "主动回复判断不建议回复：普通闲聊无需插话" {
		t.Fatalf("persisted audits = %#v", store.audits)
	}
}

func (s *memoryInboundEventStore) hasEvent(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.records[id]
	return ok
}

func (s *memoryInboundEventStore) isDone(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[id]
	return record != nil && record.state == "done"
}

type queueTestChannel struct {
	mu            sync.Mutex
	connected     bool
	epoch         uint64
	accountKnown  bool
	accountOnline bool
	accountGood   bool
	sent          []OutgoingMessage
	responses     map[string]map[string]any
	errors        map[string]error
	calls         map[string]int
}

func newQueueTestChannel() *queueTestChannel {
	return &queueTestChannel{
		connected: true,
		epoch:     1,
		responses: map[string]map[string]any{
			"get_group_list":         {"items": []any{}},
			"get_recent_contact":     {"items": []any{}},
			"get_group_msg_history":  {"messages": []any{}},
			"get_friend_msg_history": {"messages": []any{}},
		},
		errors: map[string]error{},
		calls:  map[string]int{},
	}
}

func (c *queueTestChannel) Connect(ctx context.Context, _ EventHandler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (c *queueTestChannel) Send(_ context.Context, msg OutgoingMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, msg)
	return nil
}

func (c *queueTestChannel) CallAPI(_ context.Context, action string, _ map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[action]++
	if err := c.errors[action]; err != nil {
		return nil, err
	}
	if response, ok := c.responses[action]; ok {
		return response, nil
	}
	return map[string]any{}, nil
}

func (c *queueTestChannel) callCount(action string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[action]
}

func (c *queueTestChannel) Status() ChannelStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ChannelStatus{
		Connected:          c.connected,
		SelfID:             "42",
		ConnectionEpoch:    c.epoch,
		AccountStatusKnown: c.accountKnown,
		AccountOnline:      c.accountOnline,
		AccountGood:        c.accountGood,
	}
}

func (c *queueTestChannel) setAccountStatus(known, online, good bool) {
	c.mu.Lock()
	c.accountKnown = known
	c.accountOnline = online
	c.accountGood = good
	c.mu.Unlock()
}

func (c *queueTestChannel) bumpConnectionEpoch() {
	c.mu.Lock()
	c.epoch++
	c.mu.Unlock()
}

func (c *queueTestChannel) setResponse(action string, response map[string]any) {
	c.mu.Lock()
	c.responses[action] = response
	c.mu.Unlock()
}

func (c *queueTestChannel) Close() error { return nil }

func (c *queueTestChannel) sentCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

type blockingHistoryChannel struct {
	*queueTestChannel
	historyStarted chan struct{}
	releaseHistory chan struct{}
	startOnce      sync.Once
}

func newBlockingHistoryChannel() *blockingHistoryChannel {
	return &blockingHistoryChannel{
		queueTestChannel: newQueueTestChannel(),
		historyStarted:   make(chan struct{}),
		releaseHistory:   make(chan struct{}),
	}
}

func (c *blockingHistoryChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if action == "get_group_msg_history" {
		c.startOnce.Do(func() { close(c.historyStarted) })
		select {
		case <-c.releaseHistory:
			return map[string]any{"messages": []any{}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.queueTestChannel.CallAPI(ctx, action, params)
}

func (s *memoryInboundEventStore) outcomeAndAttempts(id string) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[id]
	if record == nil {
		return "", 0
	}
	return record.outcome, record.item.Attempts
}

func (s *memoryInboundEventStore) InboundSessionHasNewerPending(_ context.Context, item InboundQueueItem) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, record := range s.records {
		other := record.item
		if id != item.ID && record.state == "pending" && other.Session == item.Session && other.Event.Time > item.Event.Time {
			return true, nil
		}
	}
	return false, nil
}

func TestInboundBacklogHandoverRules(t *testing.T) {
	now := time.Now()
	base := queuedDirectTestEvent("old", now.Add(-2*time.Minute).Unix())
	later := base
	later.MessageID, later.UserID, later.Time = "new", "20002", base.Time+1
	cases := []struct {
		name  string
		item  InboundQueueItem
		newer bool
		owner bool
		want  bool
	}{
		{name: "刚进队不交接", item: InboundQueueItem{Attempts: 1, EnqueuedAt: now.Add(-10 * time.Second)}, newer: true},
		{name: "积压且后面还有待处理消息", item: InboundQueueItem{Attempts: 1, EnqueuedAt: now.Add(-time.Minute)}, newer: true, want: true},
		{name: "积压但后面没有消息", item: InboundQueueItem{Attempts: 1, EnqueuedAt: now.Add(-time.Minute)}},
		{name: "重试不交接", item: InboundQueueItem{Attempts: 2, EnqueuedAt: now.Add(-time.Minute)}, newer: true},
		{name: "没有入队时间不交接", item: InboundQueueItem{Attempts: 1}, newer: true},
		{name: "主人不交接", item: InboundQueueItem{Attempts: 1, EnqueuedAt: now.Add(-time.Minute)}, newer: true, owner: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryInboundEventStore()
			runtime := newQueuedTestRuntime(newQueueTestChannel(), store, nil)
			if tc.owner {
				runtime = NewRuntime(BotConfig{Enabled: true, BotAccount: "42", OwnerID: base.UserID}, newQueueTestChannel(), NewPluginManager(), nil, nil, nil, nil)
				runtime.SetInboundEventStore(store)
			}
			item := tc.item
			item.Event, item.Session = base, "group:123"
			item.ID, _, _ = store.EnqueueInboundEvent(context.Background(), item.Session, base)
			if tc.newer {
				if _, _, err := store.EnqueueInboundEvent(context.Background(), item.Session, later); err != nil {
					t.Fatal(err)
				}
			}
			if got := runtime.inboundBacklogShouldHandOver(context.Background(), item, now); got != tc.want {
				t.Fatalf("handover=%v, want %v", got, tc.want)
			}
		})
	}
}

// 积压的 @ 交给后面一条普通消息：只发一份回复，回复对象换回那条 @，后面那条作为同轮消息交给模型。
func TestInboundBacklogMergesEarlierTriggerIntoLaterMessage(t *testing.T) {
	channel := newQueueTestChannel()
	provider := &capturingLLMProvider{reply: "一起回复"}
	store := newMemoryInboundEventStore()
	runtime := newQueuedTestRuntime(channel, store, provider)
	now := time.Now()

	question := queuedDirectTestEvent("question", now.Add(-time.Minute).Unix())
	question.RawMessage = "Diana 积压的问题"
	question.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana 积压的问题"}}}
	chatter := queuedDirectTestEvent("chatter", now.Unix())
	chatter.UserID = "20002"
	chatter.RawMessage = "后面随口一句"
	chatter.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "后面随口一句"}}}
	questionID, _, _ := store.EnqueueInboundEvent(context.Background(), "group:123", question)
	chatterID, _, _ := store.EnqueueInboundEvent(context.Background(), "group:123", chatter)

	outcome, err := runtime.processInboundQueueItem(context.Background(), InboundQueueItem{
		ID: questionID, Session: "group:123", Event: question, Attempts: 1, EnqueuedAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "merged_into_backlog_turn" {
		t.Fatalf("积压消息 outcome=%q, want merged_into_backlog_turn", outcome)
	}
	if channel.sentCount() != 0 || len(provider.requestSnapshot().Messages) != 0 {
		t.Fatal("交接出去的积压消息不该自己调模型或发消息")
	}
	store.records[questionID].state = "done"

	outcome, err = runtime.processInboundQueueItem(context.Background(), InboundQueueItem{
		ID: chatterID, Session: "group:123", Event: chatter, Attempts: 1, EnqueuedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "replied" {
		t.Fatalf("合并那一轮 outcome=%q, want replied", outcome)
	}
	if channel.sentCount() != 1 {
		t.Fatalf("合并后应只发一份回复，sent=%d", channel.sentCount())
	}
	request := provider.requestSnapshot()
	last := request.Messages[len(request.Messages)-1].Content
	if !strings.Contains(last, "积压的问题") || !strings.Contains(last, "积压期间一起到达的消息") || !strings.Contains(last, "后面随口一句") {
		t.Fatalf("回复请求应以积压的 @ 为对象，并带上后面那条：%q", last)
	}
	if runtime.takeBacklogMessages(chatter, time.Now()) != nil {
		t.Fatal("积压包应该已经被取走")
	}
}

// 当前这条自己就是触发消息时仍以它为回复对象，积压下来的触发消息作为同轮消息一起交给模型。
func TestInboundBacklogKeepsCurrentTriggerAsAnchor(t *testing.T) {
	channel := newQueueTestChannel()
	provider := &capturingLLMProvider{reply: "一起回复"}
	store := newMemoryInboundEventStore()
	runtime := newQueuedTestRuntime(channel, store, provider)
	now := time.Now()

	first := queuedDirectTestEvent("first", now.Add(-time.Minute).Unix())
	first.RawMessage = "Diana 第一个问题"
	first.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana 第一个问题"}}}
	second := queuedDirectTestEvent("second", now.Unix())
	second.RawMessage = "Diana 第二个问题"
	second.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana 第二个问题"}}}
	firstID, _, _ := store.EnqueueInboundEvent(context.Background(), "group:123", first)
	secondID, _, _ := store.EnqueueInboundEvent(context.Background(), "group:123", second)

	if outcome, err := runtime.processInboundQueueItem(context.Background(), InboundQueueItem{
		ID: firstID, Session: "group:123", Event: first, Attempts: 1, EnqueuedAt: now.Add(-time.Minute),
	}); err != nil || outcome != "merged_into_backlog_turn" {
		t.Fatalf("first outcome=%q err=%v", outcome, err)
	}
	store.records[firstID].state = "done"
	if outcome, err := runtime.processInboundQueueItem(context.Background(), InboundQueueItem{
		ID: secondID, Session: "group:123", Event: second, Attempts: 1, EnqueuedAt: now,
	}); err != nil || outcome != "replied" {
		t.Fatalf("second outcome=%q err=%v", outcome, err)
	}
	if channel.sentCount() != 1 {
		t.Fatalf("sent=%d, want 1", channel.sentCount())
	}
	request := provider.requestSnapshot()
	last := request.Messages[len(request.Messages)-1].Content
	head, backlog, found := strings.Cut(last, "积压期间一起到达的消息")
	if !found || !strings.Contains(head, "第二个问题") || !strings.Contains(backlog, "第一个问题") {
		t.Fatalf("应以第二个问题为对象、第一个问题作为积压消息：%q", last)
	}
}

// 没有直接触发时，积压的候选和当前这条一起进主动回复路由：只判一次，路由挑中的积压消息成为回复对象。
func TestInboundBacklogRoutesHeldProactiveCandidatesTogether(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{`{"should_reply":true,"confidence":0.97,"category":"needs_response","target_message_id":"message-1","turn_message_ids":["message-1","message-2"],"directed_at_bot":false,"answerable":true}`}}
	runtime := NewRuntime(BotConfig{
		BotAccount: "42", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	held := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1", Time: time.Now().Add(-time.Minute).Unix(),
		RawMessage: "这个报错应该怎么处理？",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "这个报错应该怎么处理？"}}},
	}
	current := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-2", MessageID: "message-2", Time: time.Now().Unix(),
		RawMessage: "我也遇到了同样的报错",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "我也遇到了同样的报错"}}},
	}
	store := newMemoryInboundEventStore()
	runtime.SetInboundEventStore(store)
	heldID, _, _ := store.EnqueueInboundEvent(context.Background(), sessionKey(held), held)
	if _, _, err := store.EnqueueInboundEvent(context.Background(), sessionKey(current), current); err != nil {
		t.Fatal(err)
	}
	held.backlogProbe = &InboundQueueItem{ID: heldID, Session: sessionKey(held), Event: held, Attempts: 1, EnqueuedAt: time.Now().Add(-time.Minute)}

	if _, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), held); handled || outcome != "merged_into_backlog_turn" {
		t.Fatalf("held handled=%v outcome=%q", handled, outcome)
	}
	if len(provider.requestsSnapshot()) != 0 {
		t.Fatal("交接出去的积压消息不该自己进路由")
	}
	event, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), current)
	if !handled || outcome != "replied_proactive" {
		t.Fatalf("handled=%v outcome=%q", handled, outcome)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 1 {
		t.Fatalf("router calls=%d, want 1", len(requests))
	}
	routeInput := requests[0].Messages[len(requests[0].Messages)-1].Content
	if !strings.Contains(routeInput, "这个报错应该怎么处理") || !strings.Contains(routeInput, "我也遇到了同样的报错") {
		t.Fatalf("路由应同时看到积压消息和当前消息：%q", routeInput)
	}
	if event.MessageID != "message-1" {
		t.Fatalf("回复对象=%q, want message-1", event.MessageID)
	}
	if len(event.backlogTurn) != 1 || event.backlogTurn[0].Event.MessageID != "message-2" {
		t.Fatalf("同轮消息=%#v, want message-2", event.backlogTurn)
	}
}

// 空闲时不该一直空手敲库：4 个 worker 固定 500 毫秒轮询，一分钟 480 次；改成 1 秒起步
// 加空手退避之后是 40 次（River 那种固定 1 秒是 240 次）。新事件走 inboundWake 立刻
// 唤醒，所以这笔省下来的开销不换延迟。
func TestInboundIdlePollBacksOff(t *testing.T) {
	delay := inboundPollInterval
	seen := []time.Duration{}
	for i := 0; i < 6; i++ {
		delay = nextInboundPollDelay(delay)
		seen = append(seen, delay)
	}
	if seen[0] != 2*inboundPollInterval {
		t.Fatalf("第一次空手应当翻倍，实际 %v", seen[0])
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("退避只能变长：%v -> %v", seen[i-1], seen[i])
		}
	}
	if last := seen[len(seen)-1]; last != inboundIdlePollMax {
		t.Fatalf("退避应当封顶在 %v，实际 %v", inboundIdlePollMax, last)
	}
	// 封顶之后不能再涨：否则一台空闲久了的机器人要好几十秒才兜底轮询一次。
	if next := nextInboundPollDelay(inboundIdlePollMax); next != inboundIdlePollMax {
		t.Fatalf("封顶之后不该继续翻倍，实际 %v", next)
	}
}
