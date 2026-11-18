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

// 请求超过阈值的 85% 时回复生成完马上滑窗（不调模型），留下阈值减非历史部分的一半；
// 摘要等会话空闲再补，请求带 Pi 的输出上限。
func TestSlideAfterReplyThenSummarizeWhenIdle(t *testing.T) {
	setIdleCompactionDelay(t, 300*time.Millisecond)
	runtime, provider, current := idleCompactionFixture(t)
	cfg := runtime.effectiveConfigForEvent(current)
	window := runtime.promptCompactionWindow(current, cfg)
	threshold := compactionThreshold(window)

	if runtime.noteReplyRequestSize(current, window, threshold*80/100) {
		t.Fatal("must not slide below the trigger")
	}
	if !runtime.noteReplyRequestSize(current, window, threshold*90/100) {
		t.Fatal("must slide after the reply above the trigger")
	}
	runtime.slideAfterReply(current, cfg, 4000)
	waitFor(t, func() bool { return runtime.compactionState(current).PendingFrom != "" })
	if got := provider.requestCount(); got != 0 {
		t.Fatalf("sliding must not call the model: %d calls", got)
	}
	kept := runtime.promptContextHistory(current, cfg)
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

	waitFor(t, func() bool { return strings.TrimSpace(runtime.compactionSummary(current)) != "" })
	provider.mu.Lock()
	maxTokens := provider.requests[0].MaxOutputTokens
	provider.mu.Unlock()
	if want := compactionSummaryMaxTokens(window); maxTokens != want || want <= 0 {
		t.Fatalf("summary MaxOutputTokens = %d, want %d", maxTokens, want)
	}
	if runtime.compactionState(current).PendingFrom != "" {
		t.Fatal("the owed summary was not cleared")
	}
}

// 有回复在跑、或者会话一直来新消息时不算空闲，摘要不写。
func TestIdleSummaryWaitsForQuietSession(t *testing.T) {
	setIdleCompactionDelay(t, 50*time.Millisecond)
	runtime, provider, current := idleCompactionFixture(t)
	cfg := runtime.effectiveConfigForEvent(current)
	window := runtime.promptCompactionWindow(current, cfg)
	if _, _, err := runtime.slideContextWindow(context.Background(), current, cfg, runtime.promptContextHistory(current, cfg), compactionKeepRecent(window, 4000), "threshold"); err != nil {
		t.Fatal(err)
	}
	progress := &replyProgress{messageID: "running"}
	runtime.registerSessionReplyProgress(sessionKey(current), progress)
	if runtime.noteReplyRequestSize(current, window, 0) {
		t.Fatal("a small request must not slide")
	}
	for range 6 {
		time.Sleep(25 * time.Millisecond)
		runtime.postponeIdleCompaction(current)
	}
	time.Sleep(120 * time.Millisecond)
	if got := provider.requestCount(); got != 0 {
		t.Fatalf("summarized while a reply was running: %d summary calls", got)
	}
	runtime.unregisterSessionReplyProgress(sessionKey(current), progress)
	waitFor(t, func() bool { return provider.requestCount() > 0 })
}

// 补摘要期间又滑过窗：只补到开始时的锚，新滑走的那段仍记着欠着；检查点被换掉
// （清空了上下文）时这次结果作废。
func TestCommitSlidSummaryKeepsLaterSlideOwed(t *testing.T) {
	runtime, _, current := idleCompactionFixture(t)
	previous := compactionCheckpoint{Summary: "旧摘要", PendingFrom: "m-010"}
	runtime.storeCompaction(current, previous, "m-080")
	next := compactionCheckpoint{Summary: "新摘要", Until: 1, UntilKey: "m-039"}
	if err := runtime.commitSlidSummary(current, previous, next, "m-040"); err != nil {
		t.Fatal(err)
	}
	state := runtime.compactionState(current)
	if state.Summary != "新摘要" || state.PendingFrom != "m-040" || runtime.groupPromptSession(current).anchor() != "m-080" {
		t.Fatalf("state = %+v, anchor %q", state, runtime.groupPromptSession(current).anchor())
	}
	if err := runtime.commitSlidSummary(current, previous, next, "m-080"); err != errSummaryStale {
		t.Fatalf("stale commit err = %v", err)
	}
}
