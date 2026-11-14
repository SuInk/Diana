package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

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

func TestRecentHistoryKeepsOversizedNewestTurn(t *testing.T) {
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

// 没有提供商配置档时也要认配置里的窗口上限。这条路径以前直接 return 兜底常量，
// 机器人和群配的 max_context_tokens 在上面完全不生效。
func TestPromptContextWindowRespectsConfigWithoutLLMStore(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g"}
	cfg := BotConfig{MaxContextTokens: 8000}
	if got := runtime.promptContextWindowTokens(event, cfg); got != 8000 {
		t.Fatalf("window = %d, want 8000", got)
	}
	// 比兜底常量还大的配置不放宽窗口，只收紧。
	wide := BotConfig{MaxContextTokens: llm.DefaultContextWindowTokens * 4}
	if got := runtime.promptContextWindowTokens(event, wide); got != llm.DefaultContextWindowTokens {
		t.Fatalf("window = %d, want the fallback %d", got, llm.DefaultContextWindowTokens)
	}
}

func TestContextSummaryTriggerThresholdKeepsAnInterval(t *testing.T) {
	for _, item := range []struct {
		name             string
		limit, threshold int
		want             int
	}{
		{"默认配置不受影响", 40, 100, 100},
		{"阈值等于保留条数时留出间隔", 100, 100, 150},
		{"阈值低于保留条数时同样留出间隔", 100, 40, 150},
		{"阈值本来就够大就不动它", 40, 300, 300},
		{"保留条数很小时间隔至少为 1", 1, 1, 2},
		{"两项都没填时回落到默认形状", 0, 0, 40},
	} {
		if got := contextSummaryTriggerThreshold(item.limit, item.threshold); got != item.want {
			t.Fatalf("%s: contextSummaryTriggerThreshold(%d, %d) = %d, want %d", item.name, item.limit, item.threshold, got, item.want)
		}
	}
}

func TestRecordPromptContextBudgetReportsSizesAndThreshold(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := &Runtime{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "30001", Time: 1700000200}
	cfg := BotConfig{DebugModeEnabled: boolPointer(true), BotAccount: "90001"}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "人设与规则", Priority: llm.MessagePrioritySystem},
		{Role: llm.RoleUser, Content: "【长期记忆】关系等级：熟人", Priority: llm.MessagePriorityMemory},
		{Role: llm.RoleUser, Content: "更早的一句话", Priority: llm.MessagePriorityHistory},
		{Role: llm.RoleUser, Content: "现在的问题", Priority: llm.MessagePriorityCurrent},
	}
	history := []MessageEvent{{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "29998", Time: 1700000100, RawMessage: "更早的一句话"}}
	runtime.recordPromptContextBudget(context.Background(), event, cfg, messages, history, "threshold", semanticReferencePromptContext{}, semanticReferenceContext{})
	entries := logs.entriesSnapshot()
	if len(entries) != 1 || entries[0].Action != "context_budget" {
		t.Fatalf("unexpected debug entries: %+v", entries)
	}
	metadata := entries[0].Metadata
	for _, key := range []string{"effective_context_window", "compaction_threshold", "keep_recent_tokens", "estimated_tokens", "category_tokens", "history_selected_turns", "history_earliest_time", "compacted"} {
		if _, ok := metadata[key]; !ok {
			t.Fatalf("trace missing %q: %+v", key, metadata)
		}
	}
	if metadata["compaction_threshold"] != llm.DefaultContextWindowTokens-compactionReserveTokens || metadata["compacted"] != "threshold" {
		t.Fatalf("trace = %+v", metadata)
	}
}

func TestRecordPromptContextBudgetStaysSilentWithoutDebugMode(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := &Runtime{}
	runtime.SetAppLogWriter(logs)
	runtime.recordPromptContextBudget(context.Background(), MessageEvent{Kind: EventKindGroup, MessageID: "30002"}, BotConfig{DebugModeEnabled: boolPointer(false)},
		[]llm.Message{{Role: llm.RoleUser, Content: "在吗", Priority: llm.MessagePriorityCurrent}}, nil, "", semanticReferencePromptContext{}, semanticReferenceContext{})
	if entries := logs.entriesSnapshot(); len(entries) != 0 {
		t.Fatalf("debug mode is off, expected no trace, got %+v", entries)
	}
}

// 锚之后的历史全部带上，不再按两万 token 的窗口丢掉旧的那一截。
func TestPromptContextHistoryKeepsEverythingSinceAnchor(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := newMemoryMessageHistoryStore()
	runtime.SetMessageHistoryStore(store)
	base := int64(1700000000)
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20002", UserID: "10001", MessageID: "current", Time: base + 100000}
	session := sessionKey(current)
	const total = 600
	for index := 0; index < total; index++ {
		_ = store.AppendMessageEvent(context.Background(), session, MessageEvent{
			Kind: EventKindGroup, GroupID: "20002", UserID: fmt.Sprintf("1%04d", index%7), SenderName: "Alice",
			MessageID: fmt.Sprintf("stored-%03d", index), Time: base + int64(index)*60,
			RawMessage: fmt.Sprintf("第 %d 句 %s", index, strings.Repeat("内容", 60)),
		})
	}
	anchorEvent := MessageEvent{Kind: EventKindGroup, GroupID: "20002", UserID: "10000", MessageID: "stored-000"}
	runtime.setHistoryWindowAnchor(compactionAnchorKey(current), messageHistoryDedupeKey(anchorEvent))
	history := runtime.promptContextHistory(current, runtime.effectiveConfigForEvent(current))
	if len(history) != total || history[0].MessageID != "stored-000" {
		t.Fatalf("history = %d events from %q, want all %d from the anchor", len(history), firstMessageID(history), total)
	}
}

// 冷启动没有锚：从最新往回取 compactionKeepRecentTokens，记下锚，下一轮从同一处开始。
func TestPromptContextHistoryColdStartAnchorsRecentWindow(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := newMemoryMessageHistoryStore()
	runtime.SetMessageHistoryStore(store)
	base := int64(1700000000)
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20003", UserID: "10001", MessageID: "current", Time: base + 100000}
	session := sessionKey(current)
	for index := 0; index < 600; index++ {
		_ = store.AppendMessageEvent(context.Background(), session, MessageEvent{
			Kind: EventKindGroup, GroupID: "20003", UserID: fmt.Sprintf("1%04d", index%7), SenderName: "Alice",
			MessageID: fmt.Sprintf("stored-%03d", index), Time: base + int64(index)*60,
			RawMessage: fmt.Sprintf("第 %d 句 %s", index, strings.Repeat("内容", 60)),
		})
	}
	cfg := runtime.effectiveConfigForEvent(current)
	first := runtime.promptContextHistory(current, cfg)
	if len(first) == 0 || len(first) == 600 {
		t.Fatalf("cold start loaded %d events, want a recent window", len(first))
	}
	_ = store.AppendMessageEvent(context.Background(), session, MessageEvent{Kind: EventKindGroup, GroupID: "20003", UserID: "10002", MessageID: "stored-new", Time: base + 99999, RawMessage: "新的一句"})
	second := runtime.promptContextHistory(current, cfg)
	if second[0].MessageID != first[0].MessageID || len(second) != len(first)+1 {
		t.Fatalf("anchor moved: first starts %q (%d), second starts %q (%d)", first[0].MessageID, len(first), second[0].MessageID, len(second))
	}
}

func firstMessageID(history []MessageEvent) string {
	if len(history) == 0 {
		return ""
	}
	return history[0].MessageID
}

func TestPromptContextWindowFollowsTheModelWindow(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20004", UserID: "1", MessageID: "1"}
	cfg := runtime.effectiveConfigForEvent(event)
	if got := runtime.promptContextWindowTokens(event, cfg); got != llm.DefaultContextWindowTokens {
		t.Fatalf("fallback window = %d, want %d", got, llm.DefaultContextWindowTokens)
	}
	runtime.mu.Lock()
	runtime.llmStore = &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{
		ID: "p1", Name: "chat", Group: llm.GroupChat,
		Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "big-window-model", APIKey: "k", ContextWindowTokens: 200000},
	}}}}
	runtime.mu.Unlock()
	// 留空时不再收到 64K：窗口就是模型窗口，快满了才压缩。
	if got := runtime.promptContextWindowTokens(event, cfg); got != 200000 {
		t.Fatalf("window = %d, want the model's 200000", got)
	}
	capped := cfg
	capped.MaxContextTokens = 32768
	if got := runtime.promptContextWindowTokens(event, capped); got != 32768 {
		t.Fatalf("capped window = %d, want 32768", got)
	}
	oversized := cfg
	oversized.MaxContextTokens = 1000000
	if got := runtime.promptContextWindowTokens(event, oversized); got != 200000 {
		t.Fatalf("oversized cap widened the window to %d", got)
	}
}

func TestContextBudgetCapReachesTheGenerateRequest(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	provider := &capturingLLMProvider{reply: "ok"}
	plain := runtime.withContextBudgetCapRun(context.Background(), func(client LLMProvider) (string, error) {
		_, err := client.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "在吗"}}})
		return "", err
	})
	if _, err := plain(provider); err != nil {
		t.Fatal(err)
	}
	if got := provider.requestSnapshot().MaxContextTokens; got != 0 {
		t.Fatalf("uncapped request carried MaxContextTokens=%d", got)
	}
	ctx := withContextBudgetCap(context.Background(), 32768)
	capped := runtime.withContextBudgetCapRun(ctx, func(client LLMProvider) (string, error) {
		_, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "在吗"}}})
		return "", err
	})
	if _, err := capped(provider); err != nil {
		t.Fatal(err)
	}
	if got := provider.requestSnapshot().MaxContextTokens; got != 32768 {
		t.Fatalf("capped request MaxContextTokens = %d, want 32768", got)
	}
}

func TestContextBudgetBreakdownReportsCompactionSettings(t *testing.T) {
	runtime := NewRuntime(BotConfig{MaxContextTokens: 100000}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	breakdown := runtime.ContextBudgetBreakdownForGroup("", "12345")
	if breakdown.GroupID != "12345" || breakdown.ContextWindow != 100000 {
		t.Fatalf("breakdown = %+v", breakdown)
	}
	if breakdown.CompactionThreshold != 100000-compactionReserveTokens || breakdown.ReserveTokens != compactionReserveTokens || breakdown.KeepRecentTokens != compactionKeepRecentTokens || breakdown.HasSummary {
		t.Fatalf("breakdown = %+v", breakdown)
	}
}

// TestContextSummaryDoesNotCompactOnEveryMessage 盯住一个会自锁的配置组合：触发条数
// 不比保留条数大时，每来一条消息都触发一次批量抽取。这里数内存历史回落的次数。
func TestContextSummaryDoesNotCompactOnEveryMessage(t *testing.T) {
	const (
		limit = 10
		total = 60
	)
	runtime := NewRuntime(BotConfig{RecentContextLimit: limit, ContextSummaryThreshold: limit}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	base := int64(1700000000)
	batches, previous := 0, 0
	for index := 0; index < total; index++ {
		event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", SenderName: "Alice", MessageID: fmt.Sprintf("msg-%02d", index), Time: base + int64(index)*60, RawMessage: fmt.Sprintf("第 %d 句", index)}
		runtime.remember(event)
		runtime.mu.RLock()
		length := len(runtime.history[sessionKey(event)])
		runtime.mu.RUnlock()
		if length < previous {
			batches++
		}
		previous = length
	}
	if batches == 0 {
		t.Fatal("这个组合下完全没有触发批量抽取，内存历史会无限增长")
	}
	interval := limit * contextSummaryMinIntervalPercent / 100
	if maximum := total/interval + 1; batches > maximum {
		t.Fatalf("触发 %d 次，超过间隔允许的 %d 次", batches, maximum)
	}
}
