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

	"github.com/SuInk/diana/model/llm"
)

// segmentSummaryProvider 只答历史分段摘要的请求，其余请求不该出现。
type segmentSummaryProvider struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (p *segmentSummaryProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.fail {
		return nil, errors.New("upstream down")
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: fmt.Sprintf("第 %d 段摘要：大家在聊天", p.calls)}, nil
}

func segmentTestRuntime(t *testing.T, enabled bool, provider *segmentSummaryProvider) (*Runtime, BotConfig, MessageEvent) {
	t.Helper()
	previous := historySegmentAsync
	historySegmentAsync = func(run func()) { run() }
	t.Cleanup(func() { historySegmentAsync = previous })
	store := &promptSessionTestStore{memoryMessageHistoryStore: newMemoryMessageHistoryStore(), states: map[string]GroupPromptSession{}}
	cfg := BotConfig{BotAccount: "bot", HistorySegmentSummaryEnabled: boolPointer(enabled)}.WithDefaults()
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	r.SetMessageHistoryStore(store)
	current := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, ProfileID: "profile", SelfID: "bot", GroupID: "one", UserID: "alice", MessageID: "current", Time: 100000}
	return r, cfg, current
}

func segmentTestHistory(current MessageEvent, from, to int) []MessageEvent {
	var history []MessageEvent
	for i := from; i < to; i++ {
		item := current
		item.MessageID = fmt.Sprintf("m%03d", i)
		item.UserID = fmt.Sprintf("u%d", i%5)
		item.Time = int64(10000 + i*60)
		item.RawMessage = fmt.Sprintf("第 %d 条群消息，说点什么", i)
		history = append(history, item)
	}
	return history
}

func segmentSummaries(messages []llm.Message) []string {
	var out []string
	for _, message := range messages {
		if strings.HasPrefix(message.Content, historySegmentHeaderPrefix) {
			out = append(out, message.Content)
		}
	}
	return out
}

// 较早的 50 条压成一段摘要，最近 40 条保留原文；窗口往前滑以后这一段和它的摘要逐字不变，
// 也不会再生成一次。
func TestHistorySegmentsStayByteIdenticalWhenWindowSlides(t *testing.T) {
	provider := &segmentSummaryProvider{}
	r, cfg, current := segmentTestRuntime(t, true, provider)
	ctx := context.Background()
	history := segmentTestHistory(current, 0, 100)
	// 第一次组装：还没有摘要，照旧带原文，后台生成第一段。
	first, _ := r.stableGroupHistory(ctx, current, cfg, history, true, nil)
	if len(segmentSummaries(first)) != 0 || provider.calls != 1 {
		t.Fatalf("first assembly must send raw history and schedule one segment: summaries=%d calls=%d", len(segmentSummaries(first)), provider.calls)
	}
	second, _ := r.stableGroupHistory(ctx, current, cfg, history, true, nil)
	summaries := segmentSummaries(second)
	if len(summaries) != 1 || !strings.Contains(summaries[0], "共 50 条") || len(second) != 1+50 {
		t.Fatalf("second assembly: summaries=%v messages=%d", summaries, len(second))
	}
	if !strings.Contains(second[1].Content, "第 50 条") || !strings.Contains(second[len(second)-1].Content, "第 99 条") {
		t.Fatalf("recent history must stay raw after the summary: %q … %q", second[1].Content, second[len(second)-1].Content)
	}
	// 窗口往前滑 10 条、又来 10 条新消息：段还在，摘要逐字不变，不重新生成。
	slid := segmentTestHistory(current, 10, 110)
	third, _ := r.stableGroupHistory(ctx, current, cfg, slid, true, nil)
	if got := segmentSummaries(third); len(got) != 1 || got[0] != summaries[0] {
		t.Fatalf("segment changed after the window slid:\n got %v\nwant %v", got, summaries)
	}
	if provider.calls != 1 {
		t.Fatalf("segment regenerated: calls=%d", provider.calls)
	}
}

// 关着时历史区和现在逐字相同，也不发任何摘要请求。
func TestHistorySegmentsDisabledKeepsHistoryUnchanged(t *testing.T) {
	provider := &segmentSummaryProvider{}
	r, cfg, current := segmentTestRuntime(t, false, provider)
	history := segmentTestHistory(current, 0, 150)
	got, _ := r.stableGroupHistory(context.Background(), current, cfg, history, true, nil)
	if provider.calls != 0 || len(segmentSummaries(got)) != 0 || len(got) != 150 {
		t.Fatalf("disabled: calls=%d messages=%d", provider.calls, len(got))
	}
	for i, message := range got {
		if !strings.Contains(message.Content, fmt.Sprintf("第 %d 条", i)) {
			t.Fatalf("message %d changed: %q", i, message.Content)
		}
	}
}

// 摘要生成失败就照旧带原文，不留半成品，下一轮再试。
func TestHistorySegmentsFallBackToRawHistoryOnFailure(t *testing.T) {
	provider := &segmentSummaryProvider{fail: true}
	r, cfg, current := segmentTestRuntime(t, true, provider)
	history := segmentTestHistory(current, 0, 100)
	for range 2 {
		got, _ := r.stableGroupHistory(context.Background(), current, cfg, history, true, nil)
		if len(segmentSummaries(got)) != 0 || len(got) != 100 {
			t.Fatalf("failed summary must fall back to raw history: messages=%d", len(got))
		}
	}
	if provider.calls != 2 {
		t.Fatalf("failed segment must be retried on the next assembly: calls=%d", provider.calls)
	}
	if s := r.groupPromptSession(current); len(s.data.Segments) != 0 {
		t.Fatalf("failed summary persisted: %+v", s.data.Segments)
	}
}

// 段里有一条被编辑过、或中间有一条被删掉，这一段作废，那几条退回原文。
func TestHistorySegmentsDropOnEditOrDeletion(t *testing.T) {
	for _, change := range []func([]MessageEvent) []MessageEvent{
		func(h []MessageEvent) []MessageEvent { h[20].RawMessage = "改过的内容"; return h },
		func(h []MessageEvent) []MessageEvent { return append(h[:20:20], h[21:]...) },
	} {
		provider := &segmentSummaryProvider{}
		r, cfg, current := segmentTestRuntime(t, true, provider)
		ctx := context.Background()
		history := segmentTestHistory(current, 0, 100)
		r.stableGroupHistory(ctx, current, cfg, history, true, nil)
		if got, _ := r.stableGroupHistory(ctx, current, cfg, history, true, nil); len(segmentSummaries(got)) != 1 {
			t.Fatal("segment not ready")
		}
		changed := change(segmentTestHistory(current, 0, 100))
		got, _ := r.stableGroupHistory(ctx, current, cfg, changed, true, nil)
		for _, summary := range segmentSummaries(got) {
			if strings.Contains(summary, "第 1 段摘要") {
				t.Fatalf("stale segment kept after the change: %q", summary)
			}
		}
	}
}
