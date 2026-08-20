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

	runtime.recordPromptContextBudget(context.Background(), event, cfg, messages, history, semantic, sources, false)

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
		false,
	)
	if entries := logs.entriesSnapshot(); len(entries) != 0 {
		t.Fatalf("debug mode is off, expected no trace, got %+v", entries)
	}
}

func TestPromptContextHistoryDropsHistoryAlreadyCoveredBySummary(t *testing.T) {
	runtime := NewRuntime(BotConfig{RecentContextLimit: 4, ContextSummaryThreshold: 6}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := newMemoryMessageHistoryStore()
	runtime.SetMessageHistoryStore(store)

	base := int64(1700000000)
	newEvent := func(index int) MessageEvent {
		return MessageEvent{
			Kind:       EventKindGroup,
			GroupID:    "20001",
			UserID:     "10001",
			SenderName: "Alice",
			MessageID:  fmt.Sprintf("msg-%02d", index),
			Time:       base + int64(index)*60,
			RawMessage: fmt.Sprintf("第 %d 句历史", index),
		}
	}
	// remember 会在超过阈值时把最旧的一批压进摘要，同时把它们从内存历史移走，
	// 但存储层仍然保留全部原文。
	for index := 0; index < 10; index++ {
		runtime.remember(newEvent(index))
	}

	session := sessionKey(newEvent(0))
	runtime.mu.RLock()
	summary := strings.TrimSpace(runtime.contextSummaries[session])
	watermark := runtime.contextSummaryMarks[session]
	memoryDepth := len(runtime.history[session])
	runtime.mu.RUnlock()
	if summary == "" || watermark <= 0 {
		t.Fatalf("expected a summary with a watermark, got %q / %d", summary, watermark)
	}
	if stored, _ := store.ListRecentMessageEvents(context.Background(), session, 0); len(stored) != 10 {
		t.Fatalf("store should still hold every raw event, got %d", len(stored))
	}

	current := newEvent(10)
	history := runtime.promptContextHistory(current, runtime.effectiveConfigForEvent(current))

	if len(history) != memoryDepth {
		t.Fatalf("prompt history = %d events, want the %d still-raw ones", len(history), memoryDepth)
	}
	for _, event := range history {
		if event.Time <= watermark {
			t.Fatalf("event %q at %d is already covered by the summary (watermark %d)", event.MessageID, event.Time, watermark)
		}
	}
}

func TestPromptContextHistoryKeepsStoredHistoryWithoutSummary(t *testing.T) {
	runtime := NewRuntime(BotConfig{RecentContextLimit: 20, ContextSummaryThreshold: 100}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := newMemoryMessageHistoryStore()
	runtime.SetMessageHistoryStore(store)

	base := int64(1700000000)
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20002", UserID: "10001", MessageID: "current", Time: base + 600}
	session := sessionKey(current)
	for index := 0; index < 5; index++ {
		_ = store.AppendMessageEvent(context.Background(), session, MessageEvent{
			Kind:       EventKindGroup,
			GroupID:    "20002",
			UserID:     "10001",
			SenderName: "Alice",
			MessageID:  fmt.Sprintf("stored-%02d", index),
			Time:       base + int64(index)*60,
			RawMessage: fmt.Sprintf("第 %d 句历史", index),
		})
	}

	history := runtime.promptContextHistory(current, runtime.effectiveConfigForEvent(current))

	if len(history) != 5 {
		t.Fatalf("without a summary every stored event must stay available, got %d", len(history))
	}
}

func TestDropSummarizedHistoryKeepsEventsStillInMemory(t *testing.T) {
	base := int64(1700000000)
	event := func(id string, offset int64) MessageEvent {
		return MessageEvent{Kind: EventKindGroup, GroupID: "20003", UserID: "1", MessageID: id, Time: base + offset}
	}
	// 与水位同秒、但仍留在内存历史里的事件属于原始窗口，不能被当成已摘要。
	memory := []MessageEvent{event("keep-same-second", 120)}
	history := []MessageEvent{event("old", 60), event("keep-same-second", 120), event("new", 180)}

	filtered := dropSummarizedHistory(history, memory, base+120)

	if len(filtered) != 2 || filtered[0].MessageID != "keep-same-second" || filtered[1].MessageID != "new" {
		t.Fatalf("filtered = %#v", filtered)
	}
	if same := dropSummarizedHistory(history, memory, 0); len(same) != 3 {
		t.Fatalf("no watermark must be a no-op, got %d", len(same))
	}
	unknownTime := []MessageEvent{{Kind: EventKindGroup, MessageID: "no-time"}}
	if kept := dropSummarizedHistory(unknownTime, nil, base+120); len(kept) != 1 {
		t.Fatal("events without a timestamp must be kept")
	}
}

func TestPromptContextWindowFollowsTheModelWindow(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20004", UserID: "1", MessageID: "1"}
	cfg := runtime.effectiveConfigForEvent(event)

	// 没有配置档时按兜底窗口走。
	if got := runtime.promptContextWindowTokens(event, cfg); got != llm.DefaultContextWindowTokens {
		t.Fatalf("fallback window = %d, want %d", got, llm.DefaultContextWindowTokens)
	}

	store := &stubLLMProfileStore{set: llm.ProfileSet{
		ActiveID: "p1",
		Profiles: []llm.Profile{{
			ID:    "p1",
			Name:  "chat",
			Group: llm.GroupChat,
			Config: llm.ProviderConfig{
				Provider:            llm.ProviderOpenAICompatible,
				Model:               "big-window-model",
				APIKey:              "k",
				ContextWindowTokens: 200000,
			},
		}},
	}}
	runtime.mu.Lock()
	runtime.llmStore = store
	runtime.mu.Unlock()

	window := runtime.promptContextWindowTokens(event, cfg)
	if window != 200000 {
		t.Fatalf("window = %d, want the model's 200000", window)
	}
	if history := contextShareBudget(window, recentHistoryTokenShare); history <= contextShareBudget(llm.DefaultContextWindowTokens, recentHistoryTokenShare) {
		t.Fatalf("recent history budget did not grow with the window: %d", history)
	}
}

func TestBotContextCapOnlyTightensTheModelWindow(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20005", UserID: "1", MessageID: "1"}
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		ActiveID: "p1",
		Profiles: []llm.Profile{{
			ID: "p1", Name: "chat", Group: llm.GroupChat,
			Config: llm.ProviderConfig{
				Provider: llm.ProviderOpenAICompatible, Model: "m", APIKey: "k", ContextWindowTokens: 200000,
			},
		}},
	}}
	runtime.mu.Lock()
	runtime.llmStore = store
	runtime.mu.Unlock()

	base := runtime.effectiveConfigForEvent(event)
	if got := runtime.promptContextWindowTokens(event, base); got != 200000 {
		t.Fatalf("uncapped window = %d, want the model's 200000", got)
	}

	capped := base
	capped.MaxContextTokens = 32768
	if got := runtime.promptContextWindowTokens(event, capped); got != 32768 {
		t.Fatalf("capped window = %d, want 32768", got)
	}

	// 上限只能收紧：填得比模型窗口还大不会真的放宽。
	oversized := base
	oversized.MaxContextTokens = 1000000
	if got := runtime.promptContextWindowTokens(event, oversized); got != 200000 {
		t.Fatalf("oversized cap widened the window to %d", got)
	}
}

func TestContextBudgetCapReachesTheGenerateRequest(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	provider := &capturingLLMProvider{reply: "ok"}

	// 没有上限时请求不带覆盖值，按配置档窗口结算。
	plain := runtime.withContextBudgetCapRun(context.Background(), func(client LLMProvider) (string, error) {
		_, err := client.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "在吗"}}})
		return "", err
	})
	if err := func() error { _, err := plain(provider); return err }(); err != nil {
		t.Fatal(err)
	}
	if got := provider.requestSnapshot().MaxContextTokens; got != 0 {
		t.Fatalf("uncapped request carried MaxContextTokens=%d", got)
	}

	// 配了上限时一路带到 Generate，Agent 的后续轮次同样受约束。
	ctx := withContextBudgetCap(context.Background(), 32768)
	capped := runtime.withContextBudgetCapRun(ctx, func(client LLMProvider) (string, error) {
		_, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "在吗"}}})
		return "", err
	})
	if err := func() error { _, err := capped(provider); return err }(); err != nil {
		t.Fatal(err)
	}
	if got := provider.requestSnapshot().MaxContextTokens; got != 32768 {
		t.Fatalf("capped request MaxContextTokens = %d, want 32768", got)
	}

	// 超限收缩重试设的更小值不能被上限顶回去。
	shrunk := runtime.withContextBudgetCapRun(ctx, func(client LLMProvider) (string, error) {
		_, err := client.Generate(ctx, llm.GenerateRequest{
			Messages:         []llm.Message{{Role: llm.RoleUser, Content: "在吗"}},
			MaxContextTokens: 8192,
		})
		return "", err
	})
	if err := func() error { _, err := shrunk(provider); return err }(); err != nil {
		t.Fatal(err)
	}
	if got := provider.requestSnapshot().MaxContextTokens; got != 8192 {
		t.Fatalf("cap overrode the shrink retry: %d", got)
	}
}
