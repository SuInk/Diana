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

	kept, summary, err := runtime.compactContext(context.Background(), current, cfg, history, compactionKeepRecentTokens, "threshold")
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
	kept2, summary2, err := runtime.compactContext(context.Background(), current, cfg, more, compactionKeepRecentTokens, "overflow")
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
	if _, _, err := runtime.compactContext(context.Background(), current, runtime.effectiveConfigForEvent(current), history, compactionKeepRecentTokens, "threshold"); err == nil {
		t.Fatal("expected the summary failure")
	}
	if runtime.compactionSummary(current) != "" || runtime.groupPromptSession(current).anchor() != "" {
		t.Fatal("failed compaction changed the session state")
	}
	if _, _, err := runtime.compactContext(context.Background(), current, runtime.effectiveConfigForEvent(current), history[:3], compactionKeepRecentTokens, "threshold"); !errors.Is(err, errNothingToCompact) {
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
	if len(summaries) == 0 || len(replies) == 0 {
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

// 小窗口：预留按 1/4 缩，阈值不超过发送层的输入预算，保留量给非历史部分让出空间。
func TestCompactionSizesShrinkWithSmallWindows(t *testing.T) {
	for _, item := range []struct {
		window, threshold int64
	}{
		{window: 16000, threshold: 12000},
		{window: 32000, threshold: 24000},
		{window: 128000, threshold: 111616},
	} {
		if got := compactionThreshold(compactionWindow{Tokens: item.window}); got != item.threshold {
			t.Fatalf("window %d threshold = %d, want %d", item.window, got, item.threshold)
		}
		if limit := llm.InputTokenBudget(item.window, llm.DefaultMaxOutputTokens); compactionThreshold(compactionWindow{Tokens: item.window}) > limit {
			t.Fatalf("window %d threshold exceeds the input budget %d", item.window, limit)
		}
	}
	if got := compactionKeepRecent(compactionWindow{Tokens: 128000}, 30000); got != compactionKeepRecentTokens {
		t.Fatalf("large window keep = %d", got)
	}
	if got := compactionKeepRecent(compactionWindow{Tokens: 32000}, 15000); got != (24000-15000)/2 {
		t.Fatalf("32K keep = %d", got)
	}
	if got := compactionKeepRecent(compactionWindow{Tokens: 16000}, 20000); got != 0 {
		t.Fatalf("keep must not go negative: %d", got)
	}
}

// 主人把上限填成最低值 32K 或 48K 时，压完的请求要真的装得下，而不是每轮都压、压完照样超。
func TestReplyCompactionFitsSmallWindows(t *testing.T) {
	for _, window := range []int64{minimumRequestContextLimit, 48000} {
		t.Run(fmt.Sprint(window), func(t *testing.T) {
			provider := &replyCompactionProvider{}
			runtime := NewRuntime(BotConfig{GroupTriggers: []string{"Diana"}, MaxContextTokens: window, LLMIdentityMaskingEnabled: boolPointer(false)}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			store := newMemoryMessageHistoryStore()
			runtime.SetMessageHistoryStore(store)
			history := compactionTestHistory(300, strings.Repeat("很长的群聊发言", 30))
			for _, item := range history {
				_ = store.AppendMessageEvent(context.Background(), sessionKey(item), item)
			}
			current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", SelfID: "10002", MessageID: "current", RawMessage: "Diana 帮我看看", Time: 1700000000 + 400*60}
			runtime.groupPromptSession(current).rememberAnchor(messageHistoryDedupeKey(history[0]))
			if _, err := runtime.replyTo(context.Background(), current, current.RawMessage); err != nil {
				t.Fatal(err)
			}
			var reply *llm.GenerateRequest
			summaries := 0
			for index, req := range provider.requests {
				if isCompactionRequest(req) {
					summaries++
					continue
				}
				for _, msg := range req.Messages {
					if strings.Contains(msg.Content, "【当前需要回复的消息】") {
						reply = &provider.requests[index]
					}
				}
			}
			if summaries == 0 || reply == nil {
				t.Fatalf("summaries=%d reply=%v", summaries, reply != nil)
			}
			if got := llm.EstimateRequestInputTokens(*reply); got > compactionThreshold(compactionWindow{Tokens: window}) {
				t.Fatalf("compacted request is %d tokens, over the %d threshold", got, compactionThreshold(compactionWindow{Tokens: window}))
			}
		})
	}
}

// 待摘要的部分比摘要模型能接的大时按块逐段更新：每块都带着上一块的结果。
func TestCompactContextSummarizesInChunks(t *testing.T) {
	// 假摘要要够长：每块都把它带进下一块的请求，预算按它的长度重新算才不会超。
	provider := &compactionTestProvider{summary: "## 在聊什么\n- 分块摘要" + strings.Repeat("。摘要里的细节", 400)}
	runtime := NewRuntime(BotConfig{MaxContextTokens: minimumRequestContextLimit}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	history := compactionTestHistory(400, strings.Repeat("很长的发言内容", 60))
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "current", Time: 1700000000 + 500*60}
	if _, _, err := runtime.compactContext(context.Background(), current, runtime.effectiveConfigForEvent(current), history, 2000, "threshold"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) < 3 {
		t.Fatalf("summary calls = %d, want several chunks", len(provider.requests))
	}
	for index, req := range provider.requests {
		prompt := req.Messages[len(req.Messages)-1].Content
		if index > 0 && !strings.Contains(prompt, "<previous-summary>\n## 在聊什么\n- 分块摘要。摘要里的细节") {
			t.Fatalf("chunk %d did not carry the previous chunk's summary", index)
		}
		if got := llm.EstimateRequestInputTokens(req); got > compactionThreshold(compactionWindow{Tokens: minimumRequestContextLimit}) {
			t.Fatalf("chunk %d request is %d tokens", index, got)
		}
	}
	all := ""
	for _, req := range provider.requests {
		all += req.Messages[len(req.Messages)-1].Content
	}
	if !strings.Contains(all, "第 0 句") || !strings.Contains(all, "第 300 句") {
		t.Fatal("some summarized history never reached the summarizer")
	}
}

// 锚丢了（读取范围外、被撤回）但有摘要：从摘要之后第一条接上，不留空档；存储里已经
// 没有那一段时摘要作废，按冷启动处理。
func TestSessionHistoryReconnectsSummaryWhenAnchorIsLost(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	history := compactionTestHistory(50, "话")
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "current", Time: 1700000000 + 100*60}
	runtime.storeCompaction(current, compactionCheckpoint{Summary: "旧摘要", Until: history[19].Time, UntilKey: messageHistoryDedupeKey(history[19])}, "missing-anchor")
	window := runtime.sessionHistoryWindow(current, history, "bot", compactionWindow{Tokens: 128000})
	if len(window) != 30 || window[0].MessageID != history[20].MessageID || runtime.compactionSummary(current) != "旧摘要" {
		t.Fatalf("window starts at %q (%d), summary %q", firstMessageID(window), len(window), runtime.compactionSummary(current))
	}
	if got := runtime.groupPromptSession(current).anchor(); got != messageHistoryDedupeKey(history[20]) {
		t.Fatalf("anchor = %q", got)
	}

	gap := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	gap.storeCompaction(current, compactionCheckpoint{Summary: "旧摘要", Until: history[0].Time - 3600}, "missing-anchor")
	window = gap.sessionHistoryWindow(current, history, "bot", compactionWindow{Tokens: 128000})
	if gap.compactionSummary(current) != "" || len(window) == 0 {
		t.Fatalf("summary that no longer connects must be dropped: %q, window %d", gap.compactionSummary(current), len(window))
	}
}

// 锚比按阈值换算的条数还早（机器人很久没说话）：往前多读，直到接上锚。
func TestPromptContextHistoryReadsBackToAnOldAnchor(t *testing.T) {
	runtime := NewRuntime(BotConfig{MaxContextTokens: minimumRequestContextLimit}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := newMemoryMessageHistoryStore()
	runtime.SetMessageHistoryStore(store)
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "current", Time: 1700000000 + 5000*60}
	history := compactionTestHistory(historyCandidateLimit(compactionWindow{Tokens: minimumRequestContextLimit})*3, "话")
	for _, item := range history {
		_ = store.AppendMessageEvent(context.Background(), sessionKey(item), item)
	}
	runtime.groupPromptSession(current).rememberAnchor(messageHistoryDedupeKey(history[5]))
	got := runtime.promptContextHistory(current, runtime.effectiveConfigForEvent(current))
	if len(got) != len(history)-5 || got[0].MessageID != history[5].MessageID {
		t.Fatalf("history = %d from %q, want %d from the anchor", len(got), firstMessageID(got), len(history)-5)
	}
}

// 重启后从持久化的锚接着带历史，不从冷启动重来。
func TestPromptContextHistoryRestoresAnchorAfterRestart(t *testing.T) {
	store := &promptSessionTestStore{memoryMessageHistoryStore: newMemoryMessageHistoryStore(), states: map[string]GroupPromptSession{}}
	history := compactionTestHistory(40, "话")
	for _, item := range history {
		_ = store.AppendMessageEvent(context.Background(), sessionKey(item), item)
	}
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "current", Time: 1700000000 + 100*60}
	first := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	first.SetMessageHistoryStore(store)
	first.storeCompaction(current, compactionCheckpoint{Summary: "摘要", Until: history[9].Time}, messageHistoryDedupeKey(history[10]))
	second := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	second.SetMessageHistoryStore(store)
	got := second.promptContextHistory(current, second.effectiveConfigForEvent(current))
	if len(got) != 30 || got[0].MessageID != history[10].MessageID {
		t.Fatalf("restarted history = %d from %q", len(got), firstMessageID(got))
	}
}

type slowCompactionProvider struct {
	compactionTestProvider
	release chan struct{}
}

func (p *slowCompactionProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	<-p.release
	return p.compactionTestProvider.Generate(ctx, req)
}

// 同一个群并发的两次回复只压缩一次：后到的拿到锁后看到锚已经挪了，直接用存好的结果。
func TestConcurrentCompactionRunsOnce(t *testing.T) {
	provider := &slowCompactionProvider{compactionTestProvider: compactionTestProvider{summary: "## 在聊什么\n- 并发"}, release: make(chan struct{})}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	history := compactionTestHistory(120, strings.Repeat("很长的发言内容", 120))
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "current", Time: 1700000000 + 200*60}
	cfg := runtime.effectiveConfigForEvent(current)
	var wg sync.WaitGroup
	kept := make([][]MessageEvent, 2)
	for index := range kept {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			got, _, err := runtime.compactContext(context.Background(), current, cfg, history, compactionKeepRecentTokens, "threshold")
			if err != nil {
				t.Error(err)
			}
			kept[index] = got
		}(index)
	}
	close(provider.release)
	wg.Wait()
	if len(provider.requests) != 1 {
		t.Fatalf("summary calls = %d, want 1", len(provider.requests))
	}
	if len(kept[0]) != len(kept[1]) || kept[0][0].MessageID != kept[1][0].MessageID {
		t.Fatal("the two replies saw different kept history")
	}
}

// 开着 Agent 时供应商报超限，同样压缩后重试。
func TestAgentReplyCompactsAndRetriesOnOverflow(t *testing.T) {
	provider := &overflowOnceProvider{}
	runtime := NewRuntime(BotConfig{GroupTriggers: []string{"Diana"}, AgentEnabled: true, LLMIdentityMaskingEnabled: boolPointer(false)}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	store := newMemoryMessageHistoryStore()
	runtime.SetMessageHistoryStore(store)
	history := compactionTestHistory(200, strings.Repeat("很长的群聊发言", 60))
	for _, item := range history {
		_ = store.AppendMessageEvent(context.Background(), sessionKey(item), item)
	}
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", SelfID: "10002", MessageID: "current", RawMessage: "Diana 帮我看看", Time: 1700000000 + 400*60}
	runtime.groupPromptSession(current).rememberAnchor(messageHistoryDedupeKey(history[0]))
	_, _ = runtime.replyTo(context.Background(), current, current.RawMessage)
	summaries := 0
	for _, req := range provider.requests {
		if isCompactionRequest(req) {
			summaries++
		}
	}
	if summaries != 1 {
		t.Fatalf("summary calls = %d, want one compaction after the overflow", summaries)
	}
	if runtime.groupPromptSession(current).anchor() == messageHistoryDedupeKey(history[0]) {
		t.Fatal("anchor did not move")
	}
}

// 旁路（主动开口、入群欢迎、挑新闻词）只带摘要和保留区，并写明没带上的条数。
func TestSideContextHistoryUsesSummaryAndKeptRegion(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := newMemoryMessageHistoryStore()
	runtime.SetMessageHistoryStore(store)
	history := compactionTestHistory(600, strings.Repeat("内容", 100))
	for _, item := range history {
		_ = store.AppendMessageEvent(context.Background(), sessionKey(item), item)
	}
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "current", Time: 1700000000 + 700*60}
	runtime.storeCompaction(current, compactionCheckpoint{Summary: "旧摘要", Until: history[0].Time - 60}, messageHistoryDedupeKey(history[0]))
	header, recent := runtime.sideContextHistory(current, runtime.effectiveConfigForEvent(current))
	if len(recent) == 0 || len(recent) >= len(history) || recent[len(recent)-1].MessageID != history[len(history)-1].MessageID {
		t.Fatalf("recent = %d", len(recent))
	}
	if !strings.Contains(header.Content, "旧摘要") || !strings.Contains(header.Content, fmt.Sprintf("还有 %d 条", len(history)-len(recent))) {
		t.Fatalf("header = %q", header.Content)
	}
}

// 被摘要的最后一条和保留的第一条在同一秒：锚丢了以后按去重键接回，同一秒的不丢。
func TestSessionHistoryReconnectsSameSecondMessages(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	history := compactionTestHistory(10, "话")
	for index := 4; index < 8; index++ {
		history[index].Time = history[4].Time
	}
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "current", Time: 1700000000 + 100*60}
	runtime.storeCompaction(current, compactionCheckpoint{Summary: "旧摘要", Until: history[5].Time, UntilKey: messageHistoryDedupeKey(history[5])}, "missing-anchor")
	window := runtime.sessionHistoryWindow(current, history, "bot", compactionWindow{Tokens: 128000})
	if len(window) != 4 || window[0].MessageID != history[6].MessageID {
		t.Fatalf("window starts at %q (%d), want the message right after the summarized one", firstMessageID(window), len(window))
	}
	// 被摘要的那条自己被撤回了：按时间接，同一秒的宁可多带也不漏。
	withdrawn := append(append([]MessageEvent(nil), history[:5]...), history[6:]...)
	runtime2 := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime2.storeCompaction(current, compactionCheckpoint{Summary: "旧摘要", Until: history[5].Time, UntilKey: messageHistoryDedupeKey(history[5])}, "missing-anchor")
	window = runtime2.sessionHistoryWindow(current, withdrawn, "bot", compactionWindow{Tokens: 128000})
	if len(window) == 0 || window[0].MessageID != history[4].MessageID {
		t.Fatalf("withdrawn case starts at %q", firstMessageID(window))
	}
}

// 输出上限设得大时，阈值跟着发送层的输入预算收紧。
func TestCompactionThresholdRespectsMaxOutput(t *testing.T) {
	window := compactionWindow{Tokens: 128000, MaxOutput: 32000}
	if got, want := compactionThreshold(window), llm.InputTokenBudget(128000, 32000); got != want {
		t.Fatalf("threshold = %d, want the input budget %d", got, want)
	}
}
