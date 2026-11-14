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

func compactionTestHistory(count int, body string) []MessageEvent {
	history := make([]MessageEvent, 0, count)
	for index := 0; index < count; index++ {
		history = append(history, MessageEvent{
			Kind: EventKindGroup, GroupID: "20001", UserID: fmt.Sprintf("1%04d", index%5), SenderName: "Alice",
			MessageID: fmt.Sprintf("m-%03d", index), Time: 1700000000 + int64(index)*60,
			RawMessage: fmt.Sprintf("第 %d 句 %s", index, body),
		})
	}
	return history
}

// 照 Pi 的 findCutPoint：从最新往回累计到 keepRecent 的那一轮及之后保留。
func TestCompactionCutKeepsRecentTokens(t *testing.T) {
	history := compactionTestHistory(100, strings.Repeat("内容", 100))
	current := MessageEvent{Time: 1700000000 + 100*60}
	cut := compactionCut(history, current, "bot", 2000)
	if cut <= 0 || cut >= len(history) {
		t.Fatalf("cut = %d", cut)
	}
	var kept int64
	for _, turn := range groupHistoryContextTurns(history[cut:], current.Time, "bot") {
		kept += turn.estimated
	}
	var withoutFirst int64
	for _, turn := range groupHistoryContextTurns(history[cut+1:], current.Time, "bot") {
		withoutFirst += turn.estimated
	}
	if kept < 2000 || withoutFirst >= 2000 {
		t.Fatalf("kept %d tokens (%d without the first kept turn), want the first turn reaching 2000", kept, withoutFirst)
	}
	if got := compactionCut(history[:3], current, "bot", 2000); got != 0 {
		t.Fatalf("short history cut = %d, want 0", got)
	}
}

type compactionTestProvider struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
	summary  string
	err      error
}

func (p *compactionTestProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	if p.err != nil {
		return nil, p.err
	}
	return &llm.GenerateResponse{Text: p.summary}, nil
}

func TestCompactContextSummarizesOlderHistoryAndMovesAnchor(t *testing.T) {
	provider := &compactionTestProvider{summary: "## 在聊什么\n- 群友在排上线顺序"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	body := strings.Repeat("很长的发言内容", 120)
	history := compactionTestHistory(120, body)
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "current", Time: 1700000000 + 200*60}
	cfg := runtime.effectiveConfigForEvent(current)

	kept, summary, err := runtime.compactContext(context.Background(), current, cfg, history, "threshold")
	if err != nil {
		t.Fatal(err)
	}
	cut := len(history) - len(kept)
	if cut <= 0 || kept[0].MessageID != history[cut].MessageID {
		t.Fatalf("kept starts at %q, cut %d", kept[0].MessageID, cut)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("summary calls = %d", len(provider.requests))
	}
	prompt := provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content
	// 要摘要的历史逐条完整写出，不截断；保留的那段不进摘要请求。
	if !strings.Contains(prompt, "<conversation>") || !strings.Contains(prompt, "第 0 句 "+body) || strings.Contains(prompt, "第 119 句") {
		t.Fatalf("summary prompt does not carry the full older transcript")
	}
	if strings.Contains(prompt, "<previous-summary>") {
		t.Fatal("first compaction must not send a previous summary")
	}
	if !strings.Contains(summary, "群友在排上线顺序") || !strings.HasPrefix(summary, contextSummaryHeaderPrefix) {
		t.Fatalf("summary = %q", summary)
	}
	if got := runtime.compactionSummary(current); got != summary {
		t.Fatalf("stored summary = %q", got)
	}
	if got := runtime.groupPromptSession(current).anchor(); got != messageHistoryDedupeKey(kept[0]) {
		t.Fatalf("anchor = %q, want first kept message", got)
	}
	_, count := parseContextSummaryHeader(strings.SplitN(summary, "\n", 2)[0])
	if count != cut {
		t.Fatalf("summary header counts %d messages, want %d", count, cut)
	}

	// 第二次压缩带上已有摘要做增量更新，条数累加。
	provider.summary = "## 在聊什么\n- 更新后的摘要"
	more := append(append([]MessageEvent(nil), kept...), compactionTestHistory(60, body)[0:0]...)
	for index := 0; index < 80; index++ {
		more = append(more, MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10002", MessageID: fmt.Sprintf("n-%03d", index), Time: 1700000000 + int64(300+index)*60, RawMessage: "新的 " + body})
	}
	kept2, summary2, err := runtime.compactContext(context.Background(), current, cfg, more, "overflow")
	if err != nil {
		t.Fatal(err)
	}
	prompt = provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content
	if !strings.Contains(prompt, "<previous-summary>") || !strings.Contains(prompt, "群友在排上线顺序") {
		t.Fatal("second compaction did not update the previous summary")
	}
	_, count2 := parseContextSummaryHeader(strings.SplitN(summary2, "\n", 2)[0])
	if count2 != cut+len(more)-len(kept2) {
		t.Fatalf("summary header counts %d, want %d", count2, cut+len(more)-len(kept2))
	}
}

func TestCompactContextFailureLeavesStateUntouched(t *testing.T) {
	provider := &compactionTestProvider{err: errors.New("upstream down")}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	history := compactionTestHistory(120, strings.Repeat("很长的发言内容", 120))
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "current", Time: 1700000000 + 200*60}
	if _, _, err := runtime.compactContext(context.Background(), current, runtime.effectiveConfigForEvent(current), history, "threshold"); err == nil {
		t.Fatal("expected the summary failure")
	}
	if runtime.compactionSummary(current) != "" || runtime.groupPromptSession(current).anchor() != "" {
		t.Fatal("failed compaction changed the session state")
	}
	if _, _, err := runtime.compactContext(context.Background(), current, runtime.effectiveConfigForEvent(current), history[:3], "threshold"); !errors.Is(err, errNothingToCompact) {
		t.Fatalf("short history err = %v", err)
	}
}

func TestApplyCompactionRemovesOnlySummarizedHistory(t *testing.T) {
	history := compactionTestHistory(4, "话")
	messages := []llm.Message{{Role: llm.RoleSystem, Content: "系统"}}
	header, _ := compactionHeaderMessage("", history)
	messages = append(messages, header)
	for _, item := range history {
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: item.RawMessage, ContextGroup: historyContextGroupPrefix + messageHistoryDedupeKey(item)})
	}
	messages[len(messages)-1].CacheBreakpoint = true
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "当前", Priority: llm.MessagePriorityCurrent})
	out, positions := applyCompactionToMessages(messages, history[2:], "摘要正文")
	if len(out) != 5 || out[0].Content != "系统" || !strings.Contains(out[1].Content, "<summary>\n摘要正文\n</summary>") {
		t.Fatalf("out = %#v", out)
	}
	if out[2].Content != history[2].RawMessage || out[3].Content != history[3].RawMessage || !out[3].CacheBreakpoint || out[4].Content != "当前" {
		t.Fatalf("kept part changed: %#v", out)
	}
	if positions[2] != -1 || positions[3] != -1 || positions[len(messages)-1] != 4 {
		t.Fatalf("positions = %v", positions)
	}
}

// 模型要知道发生了什么：有摘要时写明是压缩摘要，没有时写明历史从哪一刻开始，
// 两种都指向 chat_history。
func TestCompactionHeaderTellsTheModel(t *testing.T) {
	history := compactionTestHistory(1, "话")
	cold, ok := compactionHeaderMessage("", history)
	if !ok || !strings.Contains(cold.Content, "以下聊天记录从") || !strings.Contains(cold.Content, "chat_history") {
		t.Fatalf("cold header = %q", cold.Content)
	}
	summarized, _ := compactionHeaderMessage("摘要", history)
	if !strings.Contains(summarized.Content, "已压缩为以下摘要") || !strings.Contains(summarized.Content, "chat_history") {
		t.Fatalf("summary header = %q", summarized.Content)
	}
	if _, ok := compactionHeaderMessage("", nil); ok {
		t.Fatal("empty history must not get a header")
	}
}

// 图片预算层不再碰文字：文字再长也原样交给供应商，由上层压缩。
func TestImageBudgetLeavesOversizedTextUntouched(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	provider := &capturingLLMProvider{reply: "ok"}
	long := strings.Repeat("很长的旧历史", 60000)
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "系统"},
		{Role: llm.RoleUser, Content: long, Priority: llm.MessagePriorityHistory},
		{Role: llm.RoleUser, Content: "当前", Priority: llm.MessagePriorityCurrent},
	}}
	wrapped := &imageBudgetProvider{runtime: runtime, provider: provider, group: llm.GroupChat}
	if _, err := wrapped.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := provider.requestSnapshot().Messages[1].Content; got != long {
		t.Fatalf("history was changed: %d -> %d runes", len([]rune(long)), len([]rune(got)))
	}
}

type replyCompactionProvider struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
}

func (p *replyCompactionProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	if isCompactionRequest(req) {
		return &llm.GenerateResponse{Text: "## 在聊什么\n- 早先的长讨论"}, nil
	}
	for _, msg := range req.Messages {
		if strings.Contains(msg.Content, "older_summary_available") {
			return &llm.GenerateResponse{Text: `{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":true}`}, nil
		}
	}
	return &llm.GenerateResponse{Text: "收到。"}, nil
}

// 主回复估算超过「窗口 − 16384」时先压缩：发出去的请求带摘要、不带被摘要的原文，
// 保留的近期原文和当前消息都在。
func TestReplyCompactsHistoryWhenOverThreshold(t *testing.T) {
	provider := &replyCompactionProvider{}
	runtime := NewRuntime(BotConfig{GroupTriggers: []string{"Diana"}, MaxContextTokens: 60000, LLMIdentityMaskingEnabled: boolPointer(false)}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	store := newMemoryMessageHistoryStore()
	runtime.SetMessageHistoryStore(store)
	body := strings.Repeat("很长的群聊发言", 60)
	history := compactionTestHistory(300, body)
	for _, item := range history {
		_ = store.AppendMessageEvent(context.Background(), sessionKey(item), item)
	}
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", SelfID: "10002", MessageID: "current", RawMessage: "Diana 帮我看看", Time: 1700000000 + 400*60}
	runtime.setHistoryWindowAnchor(compactionAnchorKey(current), messageHistoryDedupeKey(history[0]))
	runtime.groupPromptSession(current).rememberAnchor(messageHistoryDedupeKey(history[0]))
	if _, err := runtime.replyTo(context.Background(), current, current.RawMessage); err != nil {
		t.Fatal(err)
	}
	var summaries, replies []llm.GenerateRequest
	for _, req := range provider.requests {
		if isCompactionRequest(req) {
			summaries = append(summaries, req)
			continue
		}
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "【当前需要回复的消息】") {
				replies = append(replies, req)
				break
			}
		}
	}
	if len(summaries) != 1 || len(replies) == 0 {
		t.Fatalf("summary calls = %d, reply calls = %d", len(summaries), len(replies))
	}
	var sent strings.Builder
	for _, msg := range replies[0].Messages {
		sent.WriteString(msg.Content)
		sent.WriteString("\n")
	}
	text := sent.String()
	if !strings.Contains(text, "早先的长讨论") || !strings.Contains(text, "<summary>") {
		t.Fatal("reply request lacks the compaction summary")
	}
	if strings.Contains(text, "第 0 句") {
		t.Fatal("summarized history is still in the reply request")
	}
	if !strings.Contains(text, "第 299 句") {
		t.Fatal("recent history was lost")
	}
	if runtime.groupPromptSession(current).anchor() == messageHistoryDedupeKey(history[0]) {
		t.Fatal("anchor did not move after compaction")
	}
}

func isCompactionRequest(req llm.GenerateRequest) bool {
	for _, msg := range req.Messages {
		if strings.HasPrefix(msg.Content, "<conversation>") {
			return true
		}
	}
	return false
}

type overflowOnceProvider struct {
	replyCompactionProvider
	failed bool
}

func (p *overflowOnceProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if !isCompactionRequest(req) {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "【当前需要回复的消息】") {
				p.mu.Lock()
				first := !p.failed
				p.failed = true
				p.mu.Unlock()
				if first {
					p.mu.Lock()
					p.requests = append(p.requests, req)
					p.mu.Unlock()
					return nil, errors.New("this model's maximum context length is 32000 tokens")
				}
				break
			}
		}
	}
	return p.replyCompactionProvider.Generate(ctx, req)
}

// 供应商报上下文超限：压缩一次后重试，重试的请求带摘要、不带被摘要的原文。
func TestReplyCompactsAndRetriesOnProviderOverflow(t *testing.T) {
	provider := &overflowOnceProvider{}
	runtime := NewRuntime(BotConfig{GroupTriggers: []string{"Diana"}, LLMIdentityMaskingEnabled: boolPointer(false)}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	store := newMemoryMessageHistoryStore()
	runtime.SetMessageHistoryStore(store)
	history := compactionTestHistory(200, strings.Repeat("很长的群聊发言", 60))
	for _, item := range history {
		_ = store.AppendMessageEvent(context.Background(), sessionKey(item), item)
	}
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", SelfID: "10002", MessageID: "current", RawMessage: "Diana 帮我看看", Time: 1700000000 + 400*60}
	runtime.groupPromptSession(current).rememberAnchor(messageHistoryDedupeKey(history[0]))
	if _, err := runtime.replyTo(context.Background(), current, current.RawMessage); err != nil {
		t.Fatal(err)
	}
	var summaries, replies int
	var last llm.GenerateRequest
	for _, req := range provider.requests {
		if isCompactionRequest(req) {
			summaries++
			continue
		}
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "【当前需要回复的消息】") {
				replies++
				last = req
				break
			}
		}
	}
	if summaries != 1 || replies < 2 {
		t.Fatalf("summary calls = %d, reply calls = %d", summaries, replies)
	}
	var sent strings.Builder
	for _, msg := range last.Messages {
		sent.WriteString(msg.Content)
	}
	if !strings.Contains(sent.String(), "早先的长讨论") || strings.Contains(sent.String(), "第 0 句") {
		t.Fatal("retried request was not compacted")
	}
}
