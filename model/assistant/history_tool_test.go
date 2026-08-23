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

	"github.com/SuInk/diana/model/llm"
)

func TestDianaChatHistoryToolRecentKeepsNewestEventsWhenMemoryExceedsLimit(t *testing.T) {
	runtime := NewRuntime(BotConfig{RecentContextLimit: 3, ContextSummaryThreshold: 100}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := newSemanticTimelineStore()
	runtime.SetMessageHistoryStore(store)
	for index := 1; index <= 6; index++ {
		runtime.remember(chatHistoryTextEvent(int64(index), "alice", "Alice", fmt.Sprintf("message-%d", index), fmt.Sprintf("消息 %d", index)))
	}

	raw, err := newDianaChatHistoryTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1"}).Run(
		context.Background(),
		map[string]any{"operation": "recent", "limit": 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	var result dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if got := historyMessageIDsFromItems(result.Items); strings.Join(got, ",") != "message-4,message-5,message-6" {
		t.Fatalf("recent message ids = %q", got)
	}
}

func TestMergeMessageHistoryKeepsChronologicalNewestWindow(t *testing.T) {
	event := func(at int64, id string) MessageEvent {
		return chatHistoryTextEvent(at, "alice", "Alice", id, id)
	}
	tests := []struct {
		name   string
		memory []MessageEvent
		stored []MessageEvent
		limit  int
		want   string
	}{
		{
			name:   "memory exceeds limit and stored overlaps newest",
			memory: []MessageEvent{event(1, "m1"), event(2, "m2"), event(3, "m3"), event(4, "m4"), event(5, "m5"), event(6, "m6")},
			stored: []MessageEvent{event(4, "m4"), event(5, "m5"), event(6, "m6")},
			limit:  3,
			want:   "m4,m5,m6",
		},
		{
			name:   "stored only",
			stored: []MessageEvent{event(1, "s1"), event(2, "s2"), event(3, "s3")},
			limit:  2,
			want:   "s2,s3",
		},
		{
			name:   "partial overlap",
			memory: []MessageEvent{event(2, "shared"), event(4, "memory-new")},
			stored: []MessageEvent{event(1, "stored-old"), event(2, "shared"), event(3, "stored-new")},
			limit:  3,
			want:   "shared,stored-new,memory-new",
		},
		{
			name:   "no overlap",
			memory: []MessageEvent{event(4, "memory-new")},
			stored: []MessageEvent{event(1, "stored-old"), event(3, "stored-new")},
			limit:  2,
			want:   "stored-new,memory-new",
		},
		{
			name:   "equal timestamps remain stable",
			memory: []MessageEvent{event(10, "memory-a"), event(10, "memory-b")},
			stored: []MessageEvent{event(10, "stored-a"), event(10, "stored-b")},
			limit:  3,
			want:   "stored-b,memory-a,memory-b",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mergeMessageHistory(test.memory, test.stored, test.limit)
			if ids := strings.Join(historyMessageIDs(got), ","); ids != test.want {
				t.Fatalf("message ids = %q, want %q", ids, test.want)
			}
		})
	}
}

func historyMessageIDs(events []MessageEvent) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.MessageID)
	}
	return ids
}

func historyMessageIDsFromItems(items []dianaChatHistoryItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.MessageID)
	}
	return ids
}

func TestDianaChatHistoryToolReadsAroundQuotedMessageBeyondShortContext(t *testing.T) {
	runtime := NewRuntime(BotConfig{RecentContextLimit: 3, ContextSummaryThreshold: 3}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := newSemanticTimelineStore()
	runtime.SetMessageHistoryStore(store)
	for _, event := range []MessageEvent{
		{
			Kind:       EventKindGroup,
			Time:       100,
			GroupID:    "group-1",
			UserID:     "milk",
			SenderName: "Alice",
			MessageID:  "settings-image",
			Segments:   []MessageSegment{{Type: "image", Data: map[string]string{"cached_file": "/tmp/settings.png"}}},
		},
		chatHistoryTextEvent(101, "alice", "Alice", "settings-text", "项目版本可以在设置页查看"),
		chatHistoryTextEvent(102, "bob", "Bob", "quoted", "这个也能查啊"),
		chatHistoryTextEvent(103, "alice", "Alice", "after", "可以"),
	} {
		runtime.remember(event)
	}
	for index := 0; index < 8; index++ {
		runtime.remember(chatHistoryTextEvent(int64(110+index), "other", "其他人", fmt.Sprintf("filler-%d", index), "后续聊天"))
	}
	if history := runtime.contextHistory(MessageEvent{Kind: EventKindGroup, GroupID: "group-1"}); semanticHistoryContainsMessage(history, "quoted") {
		t.Fatal("quoted message unexpectedly remained in short context")
	}

	tool := newDianaChatHistoryTool(runtime, MessageEvent{
		Kind:    EventKindGroup,
		Time:    200,
		GroupID: "group-1",
		UserID:  "owner",
		Quoted: &QuotedMessage{
			MessageID:  "quoted",
			SenderName: "Bob",
			Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "这个也能查啊"}}},
		},
	})
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "around", "before": 3, "after": 1})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.AnchorMessageID != "quoted" || len(result.Items) != 4 {
		t.Fatalf("result = %#v", result)
	}
	if result.Items[0].MessageID != "settings-image" || result.Items[0].ImageCount != 1 || result.Items[1].MessageID != "settings-text" || result.Items[1].Text != "项目版本可以在设置页查看" || result.Items[2].MessageID != "quoted" {
		t.Fatalf("items = %#v", result.Items)
	}
}

func TestDianaChatHistoryToolEnforcesBounds(t *testing.T) {
	if got := chatHistoryBoundedInt(map[string]any{"before": 999}, "before", 4, maximumChatHistoryAroundRadius); got != maximumChatHistoryAroundRadius {
		t.Fatalf("before = %d", got)
	}
	if got := chatHistoryPositiveInt(map[string]any{"hours": 999}, "hours", defaultChatHistorySearchHours, maximumChatHistorySearchHours); got != 999 {
		t.Fatalf("hours = %d", got)
	}
	if !chatHistoryBool(map[string]any{"all_time": "true"}, "all_time") {
		t.Fatal("all_time string should be accepted")
	}
	if got := chatHistoryPositiveInt(map[string]any{"limit": 999}, "limit", defaultChatHistoryRecentLimit, maximumChatHistoryResultLimit); got != maximumChatHistoryResultLimit {
		t.Fatalf("limit = %d", got)
	}
	result := dianaChatHistoryResult{OK: true, Action: "search", Total: maximumChatHistoryResultLimit}
	for index := 0; index < maximumChatHistoryResultLimit; index++ {
		result.Items = append(result.Items, dianaChatHistoryItem{MessageID: fmt.Sprintf("message-%d", index), Sender: "测试用户", Text: strings.Repeat("很长的历史消息", 80)})
	}
	raw, err := marshalDianaChatHistoryResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(raw)) > maximumChatHistoryOutputRunes {
		t.Fatalf("output runes = %d", len([]rune(raw)))
	}
	var bounded dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &bounded); err != nil {
		t.Fatalf("bounded JSON is invalid: %v", err)
	}
	if !bounded.Limited || len(bounded.Items) >= maximumChatHistoryResultLimit {
		t.Fatalf("bounded result = %#v", bounded)
	}
}

func TestDianaChatHistoryToolReturnsCachedMainAndQuotedImageDescriptions(t *testing.T) {
	mainHash := strings.Repeat("a", 64)
	quotedHash := strings.Repeat("b", 64)
	store := newRecallImageTestStore()
	store.descriptions[mainHash] = ImageDescriptionRecord{ContentSHA256: mainHash, Description: "主消息中的仪表盘截图"}
	store.descriptions[quotedHash] = ImageDescriptionRecord{ContentSHA256: quotedHash, Description: "引用消息中的报错截图"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	event := MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "group-1",
		UserID:    "user-1",
		MessageID: "image-history",
		Segments:  []MessageSegment{{Type: "image", Data: map[string]string{imageContentSHA256Key: mainHash}}},
		Quoted: &QuotedMessage{
			MessageID: "quoted-image",
			Segments:  []MessageSegment{{Type: "image", Data: map[string]string{imageContentSHA256Key: quotedHash}}},
		},
	}
	items := newDianaChatHistoryTool(runtime, event).items(context.Background(), []MessageEvent{event})
	if len(items) != 1 || items[0].ImageCount != 1 || items[0].QuotedImageCount != 1 {
		t.Fatalf("history image counts = %#v", items)
	}
	if len(items[0].ImageDescriptions) != 1 || !strings.Contains(items[0].ImageDescriptions[0], "仪表盘") {
		t.Fatalf("main descriptions = %#v", items[0].ImageDescriptions)
	}
	if len(items[0].QuotedImageDescriptions) != 1 || !strings.Contains(items[0].QuotedImageDescriptions[0], "报错") {
		t.Fatalf("quoted descriptions = %#v", items[0].QuotedImageDescriptions)
	}
}

func TestDianaChatHistoryToolCrossGroupSearchRequiresOptInAndKeepsNamespace(t *testing.T) {
	disabled := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &capturingHistorySearchStore{}
	disabled.SetMessageHistoryStore(store)
	event := MessageEvent{Kind: EventKindGroup, Time: 200, GroupID: "current", UserID: "owner", ContextNamespace: "bot-a"}
	_, err := newDianaChatHistoryTool(disabled, event).Run(context.Background(), map[string]any{
		"operation": "search", "query": "长期记忆", "scope": "all_groups", "all_time": true,
	})
	if err == nil || store.calls != 0 {
		t.Fatalf("disabled cross-group search err=%v calls=%d", err, store.calls)
	}

	enabled := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	enabled.SetMessageHistoryStore(store)
	raw, err := newDianaChatHistoryTool(enabled, event).Run(context.Background(), map[string]any{
		"operation": "search", "query": "长期记忆", "scope": "all_groups", "all_time": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 || !store.query.CrossSession || store.query.SessionPrefix != "bot-a:group:" || store.query.FromTime != 0 {
		t.Fatalf("captured query = %#v calls=%d", store.query, store.calls)
	}
	var result dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].GroupID != "other" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRuntimeAgentCanQueryHistoryAroundCurrentQuote(t *testing.T) {
	provider := &privacyAwareTestProvider{}
	provider.generate = func(call int, req llm.GenerateRequest) (string, error) {
		switch call {
		case 1:
			// The scope router may omit the tool. A direct quote whose target fell
			// outside the short context must still leave history lookup available.
			return `{"action":"none","tools":[],"context_message_ids":[],"keep_older_summary":false}`, nil
		case 2:
			if !requestMessagesContain(req.Messages, dianaChatHistoryToolName) {
				return "", fmt.Errorf("history tool missing from Agent prompt")
			}
			return `{"action":"tool","tool":"diana.chat_history","input":{"operation":"around","before":3,"after":1}}`, nil
		case 3:
			if !requestMessagesContain(req.Messages, "项目版本可以在设置页查看") {
				return "", fmt.Errorf("history tool result missing from Agent follow-up")
			}
			return `{"action":"final","content":"这里的“这个”指项目版本也可以在设置页查看。"}`, nil
		default:
			return "", fmt.Errorf("unexpected LLM call %d", call)
		}
	}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "10000",
		RecentContextLimit:      3,
		ContextSummaryThreshold: 3,
		AgentEnabled:            true,
		AgentMaxSteps:           3,
	}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	store := newSemanticTimelineStore()
	runtime.SetMessageHistoryStore(store)
	for _, event := range []MessageEvent{
		chatHistoryTextEvent(100, "alice", "Alice", "context", "项目版本可以在设置页查看"),
		chatHistoryTextEvent(101, "bob", "Bob", "quoted", "这个也能查啊"),
	} {
		runtime.remember(event)
	}
	for index := 0; index < 8; index++ {
		runtime.remember(chatHistoryTextEvent(int64(110+index), "other", "其他人", fmt.Sprintf("filler-%d", index), "后续聊天"))
	}
	event := MessageEvent{
		Kind:       EventKindGroup,
		Time:       200,
		SelfID:     "10000",
		GroupID:    "group-1",
		UserID:     "owner",
		SenderName: "TestOwner",
		MessageID:  "question",
		RawMessage: "Diana，这里说的这个是什么",
		Segments:   []MessageSegment{{Type: "reply", Data: map[string]string{"id": "quoted"}}, {Type: "text", Data: map[string]string{"text": "Diana，这里说的这个是什么"}}},
		Quoted: &QuotedMessage{
			MessageID:  "quoted",
			UserID:     "bob",
			SenderName: "Bob",
			Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "这个也能查啊"}}},
		},
		ToMe: true,
	}
	reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "项目版本") || len(provider.requests) != 3 {
		t.Fatalf("reply = %q requests = %d", reply, len(provider.requests))
	}
}

func chatHistoryTextEvent(eventTime int64, userID, sender, messageID, text string) MessageEvent {
	return MessageEvent{
		Kind:       EventKindGroup,
		Time:       eventTime,
		GroupID:    "group-1",
		UserID:     userID,
		SenderName: sender,
		MessageID:  messageID,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

type capturingHistorySearchStore struct {
	calls int
	query MessageHistorySearchQuery
}

func (s *capturingHistorySearchStore) AppendMessageEvent(context.Context, string, MessageEvent) error {
	return nil
}

func (s *capturingHistorySearchStore) ListRecentMessageEvents(context.Context, string, int) ([]MessageEvent, error) {
	return nil, nil
}

func (s *capturingHistorySearchStore) SearchMessageEvents(_ context.Context, query MessageHistorySearchQuery) ([]MessageEvent, int, error) {
	s.calls++
	s.query = query
	event := chatHistoryTextEvent(100, "alice", "Alice", "old", "长期记忆")
	event.GroupID = "other"
	return []MessageEvent{event}, 1, nil
}

// 「总结昨天 12 点到 17 点」这类请求没有关键词可检索，需要按时间段整段列出。
func TestDianaChatHistoryToolRangeListsWholeWindow(t *testing.T) {
	runtime := NewRuntime(BotConfig{RecentContextLimit: 3, ContextSummaryThreshold: 100}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := newSemanticTimelineStore()
	runtime.SetMessageHistoryStore(store)
	noon := time.Date(2026, 8, 19, 12, 0, 0, 0, time.Local).Unix()
	runtime.remember(chatHistoryTextEvent(noon-3600, "alice", "Alice", "before", "窗口之前"))
	runtime.remember(chatHistoryTextEvent(noon+600, "alice", "Alice", "inside-1", "窗口内第一条"))
	runtime.remember(chatHistoryTextEvent(noon+7200, "bob", "Bob", "inside-2", "窗口内第二条"))
	runtime.remember(chatHistoryTextEvent(noon+7*3600, "bob", "Bob", "after", "窗口之后"))

	raw, err := newDianaChatHistoryTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1"}).Run(
		context.Background(),
		map[string]any{"operation": "range", "from_time": "2026-08-19 12:00", "through_time": "2026-08-19 17:00"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var result dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyMessageIDsFromItems(result.Items), ","); got != "inside-1,inside-2" {
		t.Fatalf("range message ids = %q", got)
	}
	if result.Total != 2 || result.Limited {
		t.Fatalf("total = %d limited = %v", result.Total, result.Limited)
	}
	if !strings.Contains(result.Window, "2026-08-19 12:00:00") || !strings.Contains(result.Window, "2026-08-19 17:00:00") {
		t.Fatalf("window = %q", result.Window)
	}
}

// 时间段没读完时要给出续读起点，别让模型只拿前一批就下结论。
func TestDianaChatHistoryToolRangePagesWithNextFromTime(t *testing.T) {
	runtime := NewRuntime(BotConfig{RecentContextLimit: 3, ContextSummaryThreshold: 100}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := newSemanticTimelineStore()
	runtime.SetMessageHistoryStore(store)
	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.Local).Unix()
	for index := 1; index <= 5; index++ {
		runtime.remember(chatHistoryTextEvent(base+int64(index)*60, "alice", "Alice", fmt.Sprintf("m%d", index), fmt.Sprintf("消息 %d", index)))
	}

	tool := newDianaChatHistoryTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1"})
	raw, err := tool.Run(context.Background(), map[string]any{
		"operation": "range", "from_time": base, "through_time": base + 3600, "limit": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	var first dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &first); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyMessageIDsFromItems(first.Items), ","); got != "m1,m2" {
		t.Fatalf("first page = %q", got)
	}
	if !first.Limited || first.Total != 5 || first.NextFromTime != base+120+1 {
		t.Fatalf("paging info = %+v", first)
	}

	raw, err = tool.Run(context.Background(), map[string]any{
		"operation": "range", "from_time": first.NextFromTime, "through_time": base + 3600, "limit": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	var second dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &second); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(historyMessageIDsFromItems(second.Items), ","); got != "m3,m4" {
		t.Fatalf("second page = %q", got)
	}
}

// 时间段真的没消息时要说清是「没记录」，而不是让模型以为工具坏了或去编内容。
func TestDianaChatHistoryToolRangeReportsEmptyWindow(t *testing.T) {
	runtime := NewRuntime(BotConfig{RecentContextLimit: 3, ContextSummaryThreshold: 100}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(newSemanticTimelineStore())
	runtime.remember(chatHistoryTextEvent(time.Date(2026, 8, 19, 20, 0, 0, 0, time.Local).Unix(), "alice", "Alice", "night", "晚上的消息"))

	raw, err := newDianaChatHistoryTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1"}).Run(
		context.Background(),
		map[string]any{"operation": "range", "from_time": "2026-08-19 12:00", "through_time": "2026-08-19 17:00"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var result dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Total != 0 || len(result.Items) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Message, "没有消息") {
		t.Fatalf("message = %q", result.Message)
	}
}

// 没有 query 的 search 不该报错让模型以为记录是空的，直接按时间段列出来。
func TestDianaChatHistoryToolSearchWithoutQueryFallsBackToRange(t *testing.T) {
	runtime := NewRuntime(BotConfig{RecentContextLimit: 3, ContextSummaryThreshold: 100}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(newSemanticTimelineStore())
	noon := time.Date(2026, 8, 19, 12, 0, 0, 0, time.Local).Unix()
	runtime.remember(chatHistoryTextEvent(noon+600, "alice", "Alice", "inside", "窗口内"))

	raw, err := newDianaChatHistoryTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1"}).Run(
		context.Background(),
		map[string]any{"operation": "search", "from_time": "2026-08-19 12:00", "through_time": "2026-08-19 17:00"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var result dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "range" || len(result.Items) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestChatHistoryTimeValueParsesLocalStringsAndUnixSeconds(t *testing.T) {
	wantNoon := time.Date(2026, 8, 19, 12, 30, 0, 0, time.Local).Unix()
	tests := []struct {
		raw      any
		want     int64
		dateOnly bool
	}{
		{"2026-08-19 12:30", wantNoon, false},
		{"2026-08-19T12:30", wantNoon, false},
		{"2026-08-19 12:30:00", wantNoon, false},
		{"2026-08-19", time.Date(2026, 8, 19, 0, 0, 0, 0, time.Local).Unix(), true},
		{float64(1755600000), 1755600000, false},
		{"1755600000", 1755600000, false},
	}
	for _, test := range tests {
		value, dateOnly, ok := chatHistoryTimeValue(map[string]any{"from_time": test.raw}, "from_time")
		if !ok || value != test.want || dateOnly != test.dateOnly {
			t.Fatalf("%v -> (%d, %v, %v), want (%d, %v, true)", test.raw, value, dateOnly, ok, test.want, test.dateOnly)
		}
	}
	if _, _, ok := chatHistoryTimeValue(map[string]any{"from_time": "昨天"}, "from_time"); ok {
		t.Fatal("unparsable time should not be accepted")
	}
}

// 只给日期的 through_time 要覆盖整天，否则「到昨天」会被截在零点。
func TestDianaChatHistoryToolRangeTreatsDateOnlyThroughAsWholeDay(t *testing.T) {
	runtime := NewRuntime(BotConfig{RecentContextLimit: 3, ContextSummaryThreshold: 100}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(newSemanticTimelineStore())
	runtime.remember(chatHistoryTextEvent(time.Date(2026, 8, 19, 22, 0, 0, 0, time.Local).Unix(), "alice", "Alice", "late", "深夜消息"))

	raw, err := newDianaChatHistoryTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1"}).Run(
		context.Background(),
		map[string]any{"operation": "range", "from_time": "2026-08-19", "through_time": "2026-08-19"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var result dianaChatHistoryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("date-only window missed the message: %+v", result)
	}
}
