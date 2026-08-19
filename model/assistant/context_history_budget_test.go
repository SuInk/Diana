package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestRecentHistoryUsesTokenWindowInsteadOfTwentyMessages(t *testing.T) {
	history := make([]MessageEvent, 0, 160)
	for index := 0; index < 80; index++ {
		history = append(history, MessageEvent{
			Kind:       EventKindGroup,
			Time:       int64(1000 + index*2),
			UserID:     "user-1",
			MessageID:  fmt.Sprintf("u-%d", index),
			SenderName: "Alice",
			RawMessage: fmt.Sprintf("第%d个很短的问题", index),
		})
		history = append(history, MessageEvent{
			Kind:      EventKindGroup,
			Time:      int64(1001 + index*2),
			MessageID: fmt.Sprintf("a-%d", index),
			botReply:  fmt.Sprintf("第%d个简短回答", index),
		})
	}

	windows := []int64{4096, 8192, 16384, 32768}
	previous := 0
	for _, window := range windows {
		budget := contextShareBudget(window, recentHistoryTokenShare)
		selected := selectRecentHistoryTurns(history, MessageEvent{Time: 2000}, "bot", budget)
		if len(selected) < previous {
			t.Fatalf("window %d selected %d messages after %d", window, len(selected), previous)
		}
		previous = len(selected)
		if len(selected)%2 != 0 {
			t.Fatalf("window %d split a user/assistant turn: %d messages", window, len(selected))
		}
	}
	selected16K := selectRecentHistoryTurns(history, MessageEvent{Time: 2000}, "bot", contextShareBudget(16384, recentHistoryTokenShare))
	if len(selected16K) <= 20 {
		t.Fatalf("16K short-message history still behaves like a 20-message cap: %d", len(selected16K))
	}
}

func TestRecentHistoryStopsBeforeOversizedOlderTurn(t *testing.T) {
	history := []MessageEvent{
		{Kind: EventKindPrivate, Time: 1, UserID: "u", MessageID: "old-u", RawMessage: strings.Repeat("很长的旧问题", 500)},
		{Kind: EventKindPrivate, Time: 2, MessageID: "old-a", botReply: strings.Repeat("很长的旧回答", 500)},
		{Kind: EventKindPrivate, Time: 3, UserID: "u", MessageID: "new-u", RawMessage: "最近问题"},
		{Kind: EventKindPrivate, Time: 4, MessageID: "new-a", botReply: "最近回答"},
	}
	selected := selectRecentHistoryTurns(history, MessageEvent{Time: 5}, "bot", 256)
	if len(selected) != 2 || selected[0].MessageID != "new-u" || selected[1].MessageID != "new-a" {
		t.Fatalf("selected turns = %#v", selected)
	}
}

func TestRecentHistoryKeepsOversizedNewestTurnForFinalCompaction(t *testing.T) {
	history := []MessageEvent{
		{Kind: EventKindPrivate, Time: 1, UserID: "u", MessageID: "old-u", RawMessage: "旧问题"},
		{Kind: EventKindPrivate, Time: 2, MessageID: "old-a", botReply: "旧回答"},
		{Kind: EventKindPrivate, Time: 3, UserID: "u", MessageID: "new-u", RawMessage: strings.Repeat("刚发的长问题", 500)},
		{Kind: EventKindPrivate, Time: 4, MessageID: "new-a", botReply: strings.Repeat("刚给的长回答", 500)},
	}
	selected := selectRecentHistoryTurns(history, MessageEvent{Time: 5}, "bot", 128)
	if len(selected) != 2 || selected[0].MessageID != "new-u" || selected[1].MessageID != "new-a" {
		t.Fatalf("oversized newest turn was forgotten: %#v", selected)
	}
}

func TestSemanticReferenceContextIncludesAllTextSources(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotQQ: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	ids := make([]string, 0, 6)
	for index := 1; index <= 6; index++ {
		id := fmt.Sprintf("source-%d", index)
		ids = append(ids, id)
		runtime.remember(MessageEvent{
			Kind:       EventKindGroup,
			GroupID:    "group-1",
			Time:       int64(100 + index),
			UserID:     "bot",
			MessageID:  id,
			SenderName: "Diana",
			RawMessage: fmt.Sprintf("第%d条原始回复", index),
		})
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", Time: 200, UserID: "owner", MessageID: "current"}
	setEventSemanticSourceMessageIDs(&event, ids)
	got := runtime.semanticReferenceContextBlock(context.Background(), event)
	if got.Requested != 6 || got.Resolved != 6 || got.TextSources != 6 || got.ExpectedImages != 0 {
		t.Fatalf("semantic context counts = %#v", got)
	}
	for index, id := range ids {
		if !strings.Contains(got.Block, "message_id="+id) || !strings.Contains(got.Block, fmt.Sprintf("第%d条原始回复", index+1)) {
			t.Fatalf("semantic block missing %s: %s", id, got.Block)
		}
	}
	notice := semanticReferenceAttachmentNotice(got, 0)
	if !strings.Contains(notice, "6 条文字来源") || !strings.Contains(notice, "逐条核对") {
		t.Fatalf("semantic notice = %q", notice)
	}
	for _, unwanted := range []string{"6 张", "逐张查看", "原图"} {
		if strings.Contains(notice, unwanted) {
			t.Fatalf("text-only semantic notice contains %q: %q", unwanted, notice)
		}
	}
}

func TestPromptHistoryCandidateLimitIsDerivedFromTokens(t *testing.T) {
	low := historyCandidateLimitForBudget(contextShareBudget(4096, recentHistoryTokenShare))
	high := historyCandidateLimitForBudget(contextShareBudget(32768, recentHistoryTokenShare))
	if low == 20 || high == 20 || high <= low {
		t.Fatalf("candidate limits low=%d high=%d", low, high)
	}
}

func TestTokenHistoryMergeKeepsTrueNewestTimeline(t *testing.T) {
	stored := []MessageEvent{
		{Kind: EventKindPrivate, Time: 10, MessageID: "old", RawMessage: "旧消息"},
		{Kind: EventKindPrivate, Time: 20, MessageID: "middle", RawMessage: "中间消息"},
	}
	memory := []MessageEvent{{Kind: EventKindPrivate, Time: 30, MessageID: "new", RawMessage: "最新消息"}}
	got := mergeMessageHistory(memory, stored, 2)
	if len(got) != 2 || got[0].MessageID != "middle" || got[1].MessageID != "new" {
		t.Fatalf("merged timeline = %#v", got)
	}
}

func TestSemanticReferenceNoticeSeparatesTextAndImages(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotQQ: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{
		Kind: EventKindPrivate, Time: 1, UserID: "user", MessageID: "text", SenderName: "Diana", botReply: "文字结论",
	})
	runtime.remember(MessageEvent{
		Kind: EventKindPrivate, Time: 2, UserID: "user", MessageID: "image", SenderName: "Alice", RawMessage: "[图片]",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.com/a.png"}}},
	})
	event := MessageEvent{Kind: EventKindPrivate, Time: 3, UserID: "user", MessageID: "current"}
	setEventSemanticSourceMessageIDs(&event, []string{"text", "image"})
	contextBlock := runtime.semanticReferenceContextBlock(context.Background(), event)
	notice := semanticReferenceAttachmentNotice(contextBlock, 1)
	if contextBlock.TextSources != 1 || contextBlock.ExpectedImages != 1 ||
		!strings.Contains(notice, "1 条文字来源") || !strings.Contains(notice, "1 张来源图片") ||
		!strings.Contains(notice, "逐条核对") || !strings.Contains(notice, "逐张查看") {
		t.Fatalf("mixed semantic context=%#v notice=%q", contextBlock, notice)
	}
}

func TestRecordPromptContextBudgetEmitsCategoryBreakdown(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := &Runtime{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "20001",
		UserID:    "10001",
		MessageID: "30001",
		Time:      1700000200,
	}
	cfg := BotConfig{DebugModeEnabled: true, BotQQ: "90001"}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "人设与规则", Priority: llm.MessagePrioritySystem},
		{Role: llm.RoleUser, Content: "【较早上下文压缩摘要】" + strings.Repeat("旧事", 50), Priority: llm.MessagePrioritySummary},
		{Role: llm.RoleUser, Content: "【长期记忆】关系等级：熟人", Priority: llm.MessagePriorityMemory},
		{Role: llm.RoleUser, Content: "更早的一句话", Priority: llm.MessagePriorityHistory},
		{Role: llm.RoleUser, Content: "刚刚说的话", Priority: llm.MessagePriorityRecentHistory},
		{Role: llm.RoleUser, Content: "现在的问题", Priority: llm.MessagePriorityCurrent},
	}
	history := []MessageEvent{
		{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "29998", Time: 1700000100, RawMessage: "更早的一句话"},
		{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "29999", Time: 1700000180, RawMessage: "刚刚说的话"},
	}
	semantic := semanticReferencePromptContext{Requested: 6, Resolved: 6, TextSources: 6}
	sources := semanticReferenceContext{RequestedSourceCount: 6, ResolvedSourceCount: 6, TextSourceCount: 6, AttachedImageCount: 0, MissingSourceCount: 0}

	runtime.recordPromptContextBudget(context.Background(), event, cfg, messages, history, semantic, sources)

	entries := logs.entriesSnapshot()
	if len(entries) != 1 || entries[0].Action != "qqbot.context_budget" {
		t.Fatalf("unexpected debug entries: %+v", entries)
	}
	metadata := entries[0].Metadata
	for _, key := range []string{
		"effective_context_window", "output_reserve", "safety_reserve", "input_budget",
		"requested_tokens", "selected_tokens", "dropped_tokens", "over_budget",
		"categories", "summary", "history_selected_turns", "history_earliest_time",
		"history_latest_time", "semantic_attached_images", "semantic_missing_sources",
	} {
		if _, ok := metadata[key]; !ok {
			t.Fatalf("budget breakdown missing %q: %+v", key, metadata)
		}
	}

	categories, ok := metadata["categories"].([]map[string]any)
	if !ok || len(categories) == 0 {
		t.Fatalf("categories not reported: %+v", metadata["categories"])
	}
	selected := int64(0)
	names := map[string]bool{}
	for _, category := range categories {
		name, _ := category["category"].(string)
		names[name] = true
		if _, ok := category["reason_text"].(string); !ok {
			t.Fatalf("%s missing reason_text: %+v", name, category)
		}
		for _, key := range []string{"requested_tokens", "selected_tokens", "dropped_tokens"} {
			if _, ok := category[key].(int64); !ok {
				t.Fatalf("%s missing %s: %+v", name, key, category)
			}
		}
		selected += category["selected_tokens"].(int64)
	}
	for _, want := range []string{"system", "summary", "memory", "history", "recent_history", "current"} {
		if !names[want] {
			t.Fatalf("category %q missing from trace: %+v", want, names)
		}
	}
	if budget, _ := metadata["input_budget"].(int64); selected > budget {
		t.Fatalf("categories selected %d tokens over input budget %d", selected, budget)
	}

	summary, ok := metadata["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary trace missing: %+v", metadata["summary"])
	}
	if present, _ := summary["present"].(bool); !present {
		t.Fatalf("summary should be reported as present: %+v", summary)
	}
	if recompressed, _ := summary["recompressed"].(bool); recompressed {
		t.Fatalf("summary fits the window and must not be reported as recompressed: %+v", summary)
	}
	if attached, _ := metadata["semantic_attached_images"].(int); attached != 0 {
		t.Fatalf("semantic_attached_images = %v", metadata["semantic_attached_images"])
	}
}

func TestRecordPromptContextBudgetStaysSilentWithoutDebugMode(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := &Runtime{}
	runtime.SetAppLogWriter(logs)
	runtime.recordPromptContextBudget(
		context.Background(),
		MessageEvent{Kind: EventKindGroup, MessageID: "30002"},
		BotConfig{},
		[]llm.Message{{Role: llm.RoleUser, Content: "在吗", Priority: llm.MessagePriorityCurrent}},
		nil,
		semanticReferencePromptContext{},
		semanticReferenceContext{},
	)
	if entries := logs.entriesSnapshot(); len(entries) != 0 {
		t.Fatalf("debug mode is off, expected no trace, got %+v", entries)
	}
}
