// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDianaScheduleToolCreatesListsAndDeletesQuery(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001"}
	tool := newDianaScheduleTool(runtime, event)

	createdRaw, err := tool.Run(context.Background(), map[string]any{
		"operation": "create",
		"interval":  "6h",
		"query":     "查询目标项目的最新公告并总结变化",
	})
	if err != nil {
		t.Fatal(err)
	}
	var created dianaScheduleResult
	if err := json.Unmarshal([]byte(createdRaw), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Schedule == nil || created.Schedule.Interval != "6h" {
		t.Fatalf("created = %#v", created)
	}
	if len(store.items) != 1 {
		t.Fatalf("items = %#v", store.items)
	}
	item := store.items[0]
	if item.Kind != ReminderKindQuery || item.IntervalSeconds != int64((6*time.Hour)/time.Second) {
		t.Fatalf("item = %#v", item)
	}
	if item.GroupID != "123456" || item.UserID != "10001" || item.OwnerID != "10001" {
		t.Fatalf("target = %#v", item)
	}
	if remaining := time.Until(item.TriggerAt); remaining < 5*time.Hour+59*time.Minute || remaining > 6*time.Hour+time.Minute {
		t.Fatalf("next run in %s", remaining)
	}

	listedRaw, err := tool.Run(context.Background(), map[string]any{"operation": "list"})
	if err != nil || !strings.Contains(listedRaw, item.ID) {
		t.Fatalf("listed=%q err=%v", listedRaw, err)
	}
	deletedRaw, err := tool.Run(context.Background(), map[string]any{"operation": "delete", "id": item.ID})
	if err != nil || !strings.Contains(deletedRaw, "deleted") || len(store.items) != 0 {
		t.Fatalf("deleted=%q err=%v items=%#v", deletedRaw, err, store.items)
	}
}

func TestDianaScheduleToolCreatesAtMostFivePerCall(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"})
	items := make([]any, 0, maximumTasksPerToolCall)
	for index := 0; index < maximumTasksPerToolCall; index++ {
		items = append(items, map[string]any{"interval": fmt.Sprintf("%dh", index+1), "query": fmt.Sprintf("查询 %d", index+1)})
	}
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "create", "items": items})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaScheduleResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != maximumTasksPerToolCall || result.Schedule != nil || len(store.items) != maximumTasksPerToolCall {
		t.Fatalf("result=%#v stored=%#v", result, store.items)
	}

	tooMany := append(append([]any(nil), items...), map[string]any{"interval": "6h", "query": "第六个"})
	_, err = tool.Run(context.Background(), map[string]any{"operation": "create", "items": tooMany})
	if err == nil || !strings.Contains(err.Error(), "一次最多创建 5 个") || len(store.items) != maximumTasksPerToolCall {
		t.Fatalf("err=%v stored=%#v", err, store.items)
	}
}

func TestDianaScheduleBatchUsesRemainingQuota(t *testing.T) {
	store := &stubReminderStore{}
	for index := 0; index < 9; index++ {
		store.items = append(store.items, Reminder{ID: fmt.Sprintf("existing-%d", index), Kind: ReminderKindQuery, OwnerID: "20002", UserID: "20002", IntervalSeconds: 3600})
	}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	memory := newMemoryUserMemoryStore()
	memory.profiles["20002"] = UserMemoryProfile{UserID: "20002", Favorability: 60, MessageCount: 30}
	runtime.SetUserMemoryStore(memory)
	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "20002"})
	raw, err := tool.Run(context.Background(), map[string]any{
		"operation": "create",
		"items": []any{
			map[string]any{"interval": "1h", "query": "A"},
			map[string]any{"interval": "2h", "query": "B"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaScheduleResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Schedule == nil && len(result.Items) != 1 || !strings.Contains(result.Message, "按剩余额度创建了 1 个") || len(store.items) != 10 || store.items[9].Message != "A" {
		t.Fatalf("result=%#v stored=%#v", result, store.items)
	}
}

func TestDianaScheduleExecutedItemsStillConsumeQuota(t *testing.T) {
	store := &stubReminderStore{}
	for index := 0; index < 15; index++ {
		store.items = append(store.items, Reminder{ID: fmt.Sprintf("running-%d", index), Kind: ReminderKindQuery, OwnerID: "20002", UserID: "20002", IntervalSeconds: 3600})
	}
	store.items[0].LastRunAt = time.Now().Add(-time.Minute)
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	memory := newMemoryUserMemoryStore()
	memory.profiles["20002"] = UserMemoryProfile{UserID: "20002", Favorability: 60, MessageCount: 30}
	runtime.SetUserMemoryStore(memory)
	_, err := newDianaScheduleTool(runtime, MessageEvent{UserID: "20002"}).Run(context.Background(), map[string]any{
		"operation": "create",
		"interval":  "1h",
		"query":     "A",
	})
	if err == nil || !strings.Contains(err.Error(), "额度已满") || len(store.items) != 15 {
		t.Fatalf("err=%v stored=%#v", err, store.items)
	}
}

func TestDianaScheduleToolAllowsOneDefaultTaskAndRejectsShortIntervals(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)

	tool := newDianaScheduleTool(runtime, MessageEvent{UserID: "20002"})
	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "create",
		"interval":  "6h",
		"query":     "查询最新消息",
	}); err != nil {
		t.Fatalf("default create: %v", err)
	}
	// 额度不再分级，非主人一律 10：建满之后下一个必须被拒。
	for _, query := range []string{"第二项", "第三项", "第四项", "第五项", "第六项", "第七项", "第八项", "第九项", "第十项"} {
		if _, err := tool.Run(context.Background(), map[string]any{"operation": "create", "interval": "6h", "query": query}); err != nil {
			t.Fatalf("default create %s: %v", query, err)
		}
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "create", "interval": "6h", "query": "第四项"}); err == nil || !strings.Contains(err.Error(), "最多可创建 10 个") {
		t.Fatalf("default quota error = %v", err)
	}
	_, err := newDianaScheduleTool(runtime, MessageEvent{UserID: "10001"}).Run(context.Background(), map[string]any{
		"operation": "create",
		"interval":  "1s",
		"query":     "查询最新消息",
	})
	if err == nil || !strings.Contains(err.Error(), "不能短于") {
		t.Fatalf("short interval error = %v", err)
	}
}

func TestDianaScheduleToolAllowsFriendWithPersonalQuota(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	memory := newMemoryUserMemoryStore()
	memory.profiles["20002"] = UserMemoryProfile{UserID: "20002", Favorability: 60, MessageCount: 30}
	runtime.SetUserMemoryStore(memory)
	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "20002"})

	// 好感度高不再额外加额度：60 分和新人一样是 10 个。
	for i := 0; i < 10; i++ {
		if _, err := tool.Run(context.Background(), map[string]any{
			"operation": "create",
			"interval":  "6h",
			"query":     fmt.Sprintf("查询第 %d 项", i+1),
		}); err != nil {
			t.Fatalf("friend schedule %d: %v", i, err)
		}
	}
	_, err := tool.Run(context.Background(), map[string]any{
		"operation": "create",
		"interval":  "6h",
		"query":     "超过额度",
	})
	if err == nil || !strings.Contains(err.Error(), "最多可创建 10 个") {
		t.Fatalf("quota error = %v", err)
	}
}

func TestDianaScheduleCancelReleasesQuotaAndDeleteRemovesRecord(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaScheduleTool(runtime, MessageEvent{UserID: "user"})
	createdRaw, err := tool.Run(context.Background(), map[string]any{"operation": "create", "interval": "1h", "query": "A"})
	if err != nil {
		t.Fatal(err)
	}
	var created dianaScheduleResult
	if err := json.Unmarshal([]byte(createdRaw), &created); err != nil {
		t.Fatal(err)
	}
	id := created.Schedule.ID
	cancelledRaw, err := tool.Run(context.Background(), map[string]any{"operation": "cancel", "id": id})
	if err != nil {
		t.Fatal(err)
	}
	var cancelled dianaScheduleResult
	if err := json.Unmarshal([]byte(cancelledRaw), &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled.Schedule == nil || cancelled.Schedule.Status != "cancelled" || store.items[0].CancelledAt.IsZero() {
		t.Fatalf("cancelled=%#v stored=%#v", cancelled, store.items)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "create", "interval": "2h", "query": "B"}); err != nil {
		t.Fatalf("cancelled schedule still consumed quota: %v", err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "delete", "id": id}); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 || store.items[0].Message != "B" {
		t.Fatalf("stored=%#v", store.items)
	}
}

func TestRuntimeAgentCanCreateScheduledQuery(t *testing.T) {
	store := &stubReminderStore{}
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":""}`,
		`{"action":"tool","tool":"tools_load","input":{"names":["subscription"]}}`,
		`{"action":"tool","tool":"tools_execute","input":{"name":"subscription","input":{"operation":"create","kind":"schedule","interval":"6h","query":"查询最新公告并总结变化"}}}`,
		`{"action":"final","content":"已建立每 6 小时执行一次的订阅。"}`,
	}}
	runtime := NewRuntime(BotConfig{
		OwnerID:        "10001",
		AgentEnabled:   true,
		AgentMaxSteps:  3,
		RequestTimeout: 5 * time.Second,
		// 序列模型按精确次数喂回复，发送前审核的额外往返与本断言无关，关掉。
		ReplySafetyMasterEnabled: boolPointer(false),
	}, channel, NewPluginManager(), nil, store, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "10001",
		MessageID: "schedule-1",
		Segments:  []MessageSegment{{Type: "text", Data: map[string]string{"text": "每 6 小时自动查询最新公告并通知我"}}},
	}
	reply, err := runtime.replyTo(context.Background(), event, "每 6 小时自动查询最新公告并通知我")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "每 6 小时") || len(store.items) != 1 {
		t.Fatalf("reply=%q items=%#v", reply, store.items)
	}
	if len(channel.sent) != 1 || channel.sent[0].UserID != "10001" {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if len(provider.requests) != 4 {
		t.Fatalf("requests = %d", len(provider.requests))
	}
	foundTool := false
	for _, msg := range provider.requests[1].Messages {
		if strings.Contains(msg.Content, "schedule") {
			foundTool = true
			break
		}
	}
	if !foundTool {
		t.Fatal("agent prompt did not expose schedule")
	}
}

func TestRuntimeDueScheduledQueryRunsAgentAndReschedules(t *testing.T) {
	store := &stubReminderStore{items: []Reminder{{
		ID:              "task1234",
		Kind:            ReminderKindQuery,
		OwnerID:         "10001",
		GroupID:         "20001",
		UserID:          "10001",
		Message:         "查询最新公告并总结变化",
		TriggerAt:       time.Now().Add(-time.Minute),
		IntervalSeconds: int64((6 * time.Hour) / time.Second),
		CreatedAt:       time.Now().Add(-7 * time.Hour),
	}}}
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"final","content":"最新公告没有变化。"}`,
	}}
	runtime := NewRuntime(BotConfig{
		OwnerID:        "10001",
		SystemPrompt:   "你是说话自然的 Diana。",
		AgentEnabled:   true,
		AgentMaxSteps:  3,
		RequestTimeout: 5 * time.Second,
	}, channel, NewPluginManager(), nil, store, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	runtime.fireDueReminders(context.Background())

	if len(channel.sent) != 1 || !strings.Contains(channel.sent[0].Text, "最新公告没有变化") || strings.Contains(channel.sent[0].Text, "task1234") {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if len(store.items) != 1 {
		t.Fatalf("items = %#v", store.items)
	}
	next := store.items[0]
	if next.TriggerAt.Before(time.Now().Add(5*time.Hour+59*time.Minute)) || next.LastRunAt.IsZero() {
		t.Fatalf("rescheduled item = %#v", next)
	}
	if next.LastError != "" || next.ConsecutiveFailures != 0 {
		t.Fatalf("failure state = %#v", next)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests = %d", len(provider.requests))
	}
	foundQuery := false
	foundPersona := false
	for _, msg := range provider.requests[0].Messages {
		if strings.Contains(msg.Content, "查询最新公告并总结变化") {
			foundQuery = true
		}
		if strings.Contains(msg.Content, "你是说话自然的 Diana。") {
			foundPersona = true
		}
	}
	if !foundQuery || !foundPersona || requestMessagesContain(provider.requests[0].Messages, "task1234") {
		t.Fatalf("scheduled query missing from request: %#v", provider.requests[0].Messages)
	}
}

// 「每周日 22:00 提醒我睡觉」：at 定首次时间，interval 定周期，建出来的是一条周期
// 订阅，而不是只响一次的提醒。
func TestDianaScheduleToolCreatesWeeklyReminderAtFixedTime(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"})
	zone := time.FixedZone("CST", 8*3600)
	first := time.Now().In(zone).Add(3 * time.Hour).Truncate(time.Minute)

	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "create",
		"interval":  "168h",
		"at":        first.Format(time.RFC3339),
		"query":     "提醒用户该睡觉了",
	}); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 {
		t.Fatalf("items = %#v", store.items)
	}
	item := store.items[0]
	if !reminderIsScheduledQuery(item) || !item.TriggerAt.Equal(first) || !item.ScheduleAnchorAt.Equal(first) {
		t.Fatalf("item = %#v, want first run and anchor at %s", item, first)
	}
}

func TestDianaScheduleToolRollsPastFirstTimeToNextSlot(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"})
	past := time.Now().Add(-time.Hour).Truncate(time.Minute)

	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "create",
		"interval":  "24h",
		"at":        past.Format(time.RFC3339),
		"query":     "提醒用户喝水",
	}); err != nil {
		t.Fatal(err)
	}
	item := store.items[0]
	if want := past.Add(24 * time.Hour); !item.TriggerAt.Equal(want) {
		t.Fatalf("trigger = %s, want %s", item.TriggerAt, want)
	}
}

func TestDianaScheduleToolRejectsMalformedFirstTime(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"})
	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "create", "interval": "168h", "at": "周日 22:00", "query": "提醒用户睡觉",
	}); err == nil || len(store.items) != 0 {
		t.Fatalf("err=%v items=%#v", err, store.items)
	}
}

// 跑完之后落回网格：开跑晚了几秒、失败重试晚了几分钟，下一次仍在原来的时间点。
func TestFinishScheduledQueryKeepsAnchoredTimeSlot(t *testing.T) {
	anchor := time.Now().Add(-5 * time.Minute).Truncate(time.Minute)
	store := &stubReminderStore{items: []Reminder{{
		ID:               "weekly",
		Kind:             ReminderKindQuery,
		OwnerID:          "10001",
		UserID:           "10001",
		Message:          "提醒用户该睡觉了",
		TriggerAt:        time.Now().Add(-time.Second),
		IntervalSeconds:  int64((168 * time.Hour) / time.Second),
		ScheduleAnchorAt: anchor,
	}}}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)

	updated, err := runtime.finishScheduledQuery("weekly", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := anchor.Add(168 * time.Hour); !updated.TriggerAt.Equal(want) {
		t.Fatalf("next = %s, want %s", updated.TriggerAt, want)
	}
}

func TestUpdateScheduledQueryMovesAnchorOnlyWhenTimeChanges(t *testing.T) {
	anchor := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	store := &stubReminderStore{items: []Reminder{{
		ID:               "daily",
		Kind:             ReminderKindQuery,
		OwnerID:          "10001",
		UserID:           "10001",
		Message:          "提醒用户喝水",
		TriggerAt:        anchor,
		IntervalSeconds:  int64((24 * time.Hour) / time.Second),
		ScheduleAnchorAt: anchor,
	}}}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)

	item, err := runtime.updateScheduledQuery("10001", "daily", map[string]any{"query": "提醒用户多喝水"})
	if err != nil {
		t.Fatal(err)
	}
	if !item.TriggerAt.Equal(anchor) || !item.ScheduleAnchorAt.Equal(anchor) {
		t.Fatalf("query-only update moved the slot: %#v", item)
	}

	moved := anchor.Add(time.Hour)
	item, err = runtime.updateScheduledQuery("10001", "daily", map[string]any{"at": moved.Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if !item.TriggerAt.Equal(moved) || !item.ScheduleAnchorAt.Equal(moved) {
		t.Fatalf("at update = %#v, want %s", item, moved)
	}
}

func TestDianaScheduleToolCreatesMonthlyReminderOnCalendar(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaScheduleTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"})
	first := time.Now().Add(2 * time.Hour).Truncate(time.Minute)

	raw, err := tool.Run(context.Background(), map[string]any{
		"operation": "create", "interval": "1mo", "at": first.Format(time.RFC3339), "query": "提醒用户交房租",
	})
	if err != nil {
		t.Fatal(err)
	}
	item := store.items[0]
	if !reminderIsScheduledQuery(item) || item.IntervalMonths != 1 || item.IntervalSeconds != int64((30*24*time.Hour)/time.Second) {
		t.Fatalf("item = %#v", item)
	}
	if !strings.Contains(raw, `"interval": "1mo"`) {
		t.Fatalf("tool result should echo interval in new units: %s", raw)
	}
	// 假装第一次已经到点跑完：把原点挪到刚过去的时刻，下一次应落在一个日历月之后。
	past := time.Now().Add(-time.Minute).Truncate(time.Second)
	store.items[0].ScheduleAnchorAt, store.items[0].TriggerAt = past, past
	updated, err := runtime.finishScheduledQuery(item.ID, past, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := addMonthsClamped(past, 1); !updated.TriggerAt.Equal(want) {
		t.Fatalf("next = %s, want %s", updated.TriggerAt, want)
	}
}

func TestDianaScheduleIntervalUnits(t *testing.T) {
	for raw, want := range map[string]calendarDuration{
		"1min":  {Fixed: time.Minute},
		"1d":    {Fixed: 24 * time.Hour},
		"1w":    {Fixed: 7 * 24 * time.Hour},
		"1m":    {Fixed: time.Minute},
		"1h30m": {Fixed: 90 * time.Minute},
		"1mo":   {Months: 1},
		"1y":    {Months: 12},
	} {
		got, err := parseScheduleInterval(raw)
		if err != nil || got != want {
			t.Fatalf("%q = %+v, %v", raw, got, err)
		}
	}
	// 按月的不能再混固定单位；上限一年，下限一分钟。
	for _, raw := range []string{"1mo2d", "2y", "13mo", "30s"} {
		if _, err := parseScheduleInterval(raw); err == nil {
			t.Fatalf("%q should be rejected", raw)
		}
	}
}
