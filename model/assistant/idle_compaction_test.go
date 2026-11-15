package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func setIdleCompactionDelay(t *testing.T, delay time.Duration) {
	t.Helper()
	previous := idleCompactionDelay
	idleCompactionDelay = delay
	t.Cleanup(func() { idleCompactionDelay = previous })
}

// idleCompactionFixture 是一个锚在最早一条、历史塞满窗口的群。
func idleCompactionFixture(t *testing.T) (*Runtime, *compactionTestProvider, MessageEvent) {
	t.Helper()
	provider := &compactionTestProvider{summary: "## 在聊什么\n- 空闲压缩"}
	runtime := NewRuntime(BotConfig{MaxContextTokens: 48000}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	t.Cleanup(runtime.stopIdleCompactions)
	store := newMemoryMessageHistoryStore()
	runtime.SetMessageHistoryStore(store)
	history := compactionTestHistory(300, strings.Repeat("很长的群聊发言", 30))
	for _, item := range history {
		_ = store.AppendMessageEvent(context.Background(), sessionKey(item), item)
	}
	runtime.groupPromptSession(history[0]).rememberAnchor(messageHistoryDedupeKey(history[0]))
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "current", Time: 1700000000 + 400*60}
	return runtime, provider, current
}

func (p *compactionTestProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

// 请求超过阈值的 85% 时等会话空闲再压：滑走最旧的原文，留下阈值减非历史部分的一半，
// 摘要请求带 Pi 的输出上限。
func TestIdleCompactionSlidesWindowWhenQuiet(t *testing.T) {
	setIdleCompactionDelay(t, 20*time.Millisecond)
	runtime, provider, current := idleCompactionFixture(t)
	window := runtime.promptCompactionWindow(current, runtime.effectiveConfigForEvent(current))
	threshold := compactionThreshold(window)

	runtime.noteReplyRequestSize(current, window, threshold*80/100, 4000)
	time.Sleep(100 * time.Millisecond)
	if got := provider.requestCount(); got != 0 {
		t.Fatalf("compacted below the trigger: %d summary calls", got)
	}

	runtime.noteReplyRequestSize(current, window, threshold*90/100, 4000)
	waitFor(t, func() bool { return strings.TrimSpace(runtime.compactionSummary(current)) != "" })
	provider.mu.Lock()
	maxTokens := provider.requests[0].MaxOutputTokens
	provider.mu.Unlock()
	if want := compactionSummaryMaxTokens(window); maxTokens != want || want <= 0 {
		t.Fatalf("summary MaxOutputTokens = %d, want %d", maxTokens, want)
	}
	kept := runtime.promptContextHistory(current, runtime.effectiveConfigForEvent(current))
	if len(kept) == 0 || kept[0].MessageID == "m-000" {
		t.Fatalf("anchor did not move: %d events from %q", len(kept), firstMessageID(kept))
	}
	var keptTokens int64
	for _, item := range kept {
		keptTokens += estimateHistoryContextEventTokens(item, current.Time, false)
	}
	if keep := compactionKeepRecent(window, 4000); keptTokens < keep/2 {
		t.Fatalf("kept %d tokens of raw history, want about %d", keptTokens, keep)
	}
}

// 有回复在跑、或者会话一直来新消息时不算空闲。
func TestIdleCompactionWaitsForQuietSession(t *testing.T) {
	setIdleCompactionDelay(t, 50*time.Millisecond)
	runtime, provider, current := idleCompactionFixture(t)
	window := runtime.promptCompactionWindow(current, runtime.effectiveConfigForEvent(current))
	progress := &replyProgress{messageID: "running"}
	runtime.registerSessionReplyProgress(sessionKey(current), progress)
	runtime.noteReplyRequestSize(current, window, compactionThreshold(window)*90/100, 4000)
	for range 6 {
		time.Sleep(25 * time.Millisecond)
		runtime.postponeIdleCompaction(current)
	}
	time.Sleep(120 * time.Millisecond)
	if got := provider.requestCount(); got != 0 {
		t.Fatalf("compacted while a reply was running: %d summary calls", got)
	}
	runtime.unregisterSessionReplyProgress(sessionKey(current), progress)
	waitFor(t, func() bool { return provider.requestCount() > 0 })
}
