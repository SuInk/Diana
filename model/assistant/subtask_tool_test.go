// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

type subtaskCaptureProvider struct {
	requests []llm.GenerateRequest
	reply    string
}

func (p *subtaskCaptureProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	return &llm.GenerateResponse{Text: p.reply}, nil
}

func subtaskRuntime(reply string) (*Runtime, *subtaskCaptureProvider) {
	provider := &subtaskCaptureProvider{reply: reply}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	return runtime, provider
}

func TestSubtaskToolAnswersFromMaterialOnly(t *testing.T) {
	runtime, provider := subtaskRuntime("材料里提到三处不一致：单价、税率、合计。")
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})

	out, err := tool.Run(context.Background(), map[string]any{
		"question": "这份对账单有哪些不一致",
		"material": "单价 12 与 13 不符；税率写了 6% 和 13%；合计对不上。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "三处不一致") {
		t.Fatalf("subtask answer = %q", out)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider calls = %d", len(provider.requests))
	}
	// 子调用只该看到调用方给的素材：没有系统人格提示、没有聊天历史。
	messages := provider.requests[0].Messages
	if len(messages) != 2 {
		t.Fatalf("subtask carried %d messages, want a system rule plus one user turn", len(messages))
	}
	if !strings.Contains(messages[1].Content, "单价 12 与 13 不符") {
		t.Fatalf("material was not passed through: %q", messages[1].Content)
	}
}

func TestSubtaskToolRejectsIncompleteInput(t *testing.T) {
	runtime, provider := subtaskRuntime("ok")
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})

	for _, input := range []map[string]any{
		{"question": "", "material": "素材"},
		{"question": "问题", "material": "  "},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("incomplete input was accepted: %#v", input)
		}
	}
	if len(provider.requests) != 0 {
		t.Fatalf("rejected input still reached the provider: %d calls", len(provider.requests))
	}
}

func TestSubtaskToolRejectsOversizedMaterial(t *testing.T) {
	runtime, provider := subtaskRuntime("ok")
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})

	// 素材大到这个地步说明这件事该留在主回复里，拆出去反而多付一次 prefill。
	oversized := strings.Repeat("材", maximumSubtaskMaterialRunes+1)
	if _, err := tool.Run(context.Background(), map[string]any{"question": "总结", "material": oversized}); err == nil {
		t.Fatal("oversized material was accepted")
	}
	if len(provider.requests) != 0 {
		t.Fatalf("oversized material reached the provider: %d calls", len(provider.requests))
	}
}

func TestSubtaskToolCapsFanOutPerReply(t *testing.T) {
	runtime, provider := subtaskRuntime("结论")
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})
	input := map[string]any{"question": "总结这段", "material": "一段材料"}

	for index := 0; index < defaultSubtaskMaxTasks; index++ {
		if _, err := tool.Run(context.Background(), input); err != nil {
			t.Fatalf("call %d failed: %v", index+1, err)
		}
	}
	// 子调用是同步的，放开扇出会把一次回复拖成一串串行往返。
	if _, err := tool.Run(context.Background(), input); err == nil {
		t.Fatal("fan-out cap was not enforced")
	}
	if len(provider.requests) != defaultSubtaskMaxTasks {
		t.Fatalf("provider calls = %d, want %d", len(provider.requests), defaultSubtaskMaxTasks)
	}
}

func TestSubtaskLimitsDefaultAndClampPerBot(t *testing.T) {
	defaults := (BotConfig{}).WithDefaults()
	if defaults.SubtaskMaxTasks != 8 || defaults.SubtaskMaxConcurrency != 3 {
		t.Fatalf("default subtask limits = %d/%d, want 8/3", defaults.SubtaskMaxTasks, defaults.SubtaskMaxConcurrency)
	}
	clamped := (BotConfig{SubtaskMaxTasks: 99, SubtaskMaxConcurrency: 99}).WithDefaults()
	if clamped.SubtaskMaxTasks != maximumSubtaskMaxTasks || clamped.SubtaskMaxConcurrency != maximumSubtaskMaxConcurrency {
		t.Fatalf("clamped subtask limits = %d/%d, want %d/%d", clamped.SubtaskMaxTasks, clamped.SubtaskMaxConcurrency, maximumSubtaskMaxTasks, maximumSubtaskMaxConcurrency)
	}
	roundTrip := ConfigFromPayload(PayloadFromConfig(clamped), BotConfig{})
	if roundTrip.SubtaskMaxTasks != maximumSubtaskMaxTasks || roundTrip.SubtaskMaxConcurrency != maximumSubtaskMaxConcurrency {
		t.Fatalf("round-trip subtask limits = %d/%d", roundTrip.SubtaskMaxTasks, roundTrip.SubtaskMaxConcurrency)
	}
}

func TestSubtaskToolUsesPerBotTaskLimit(t *testing.T) {
	provider := &subtaskCaptureProvider{reply: "ok"}
	runtime := NewRuntime(BotConfig{BotAccount: "42", SubtaskMaxTasks: 2}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})
	_, err := tool.Run(context.Background(), map[string]any{"tasks": []any{
		map[string]any{"id": "one", "mode": "analyze", "objective": "一", "material": "一"},
		map[string]any{"id": "two", "mode": "analyze", "objective": "二", "material": "二"},
		map[string]any{"id": "three", "mode": "analyze", "objective": "三", "material": "三"},
	}})
	if err == nil || !strings.Contains(err.Error(), "1–2") {
		t.Fatalf("three tasks with limit two returned %v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("rejected batch reached provider: %d calls", len(provider.requests))
	}
}

func TestSubtaskToolIsAvailableBelowOwnerTier(t *testing.T) {
	// 它不碰文件、命令和浏览器，只把素材压成一句话，所以普通等级也该有。
	allowed := RelationshipPolicy{}.allowedAgentToolNames()
	if !allowed[dianaSubtaskToolName] {
		t.Fatalf("subtask tool is gated above the ordinary tier: %#v", allowed)
	}
}

type concurrentSubtaskProvider struct {
	mu        sync.Mutex
	active    int
	maxActive int
	target    int
	release   chan struct{}
	once      sync.Once
}

func (p *concurrentSubtaskProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	if p.active >= p.target {
		p.once.Do(func() { close(p.release) })
	}
	p.mu.Unlock()
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
	}()
	return &llm.GenerateResponse{Text: req.Messages[len(req.Messages)-1].Content}, nil
}

func TestSubtaskToolRunsBatchConcurrentlyAndKeepsOrder(t *testing.T) {
	provider := &concurrentSubtaskProvider{target: 3, release: make(chan struct{})}
	runtime := NewRuntime(BotConfig{BotAccount: "42", MaxBotConcurrency: 4}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})

	out, err := tool.Run(context.Background(), map[string]any{
		"max_concurrency": 3,
		"tasks": []any{
			map[string]any{"id": "first", "mode": "analyze", "objective": "一", "material": "材料一"},
			map[string]any{"id": "second", "mode": "analyze", "objective": "二", "material": "材料二"},
			map[string]any{"id": "third", "mode": "analyze", "objective": "三", "material": "材料三"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var batch subtaskBatchResult
	if err := json.Unmarshal([]byte(out), &batch); err != nil {
		t.Fatalf("decode batch: %v, output=%s", err, out)
	}
	if len(batch.Results) != 3 || batch.Results[0].ID != "first" || batch.Results[1].ID != "second" || batch.Results[2].ID != "third" {
		t.Fatalf("batch order changed: %#v", batch.Results)
	}
	provider.mu.Lock()
	maxActive := provider.maxActive
	provider.mu.Unlock()
	if maxActive != 3 {
		t.Fatalf("peak concurrency = %d, want 3", maxActive)
	}
}

func TestSubtaskRequestCannotRaisePerBotConcurrency(t *testing.T) {
	provider := &concurrentSubtaskProvider{target: 2, release: make(chan struct{})}
	runtime := NewRuntime(BotConfig{BotAccount: "42", MaxBotConcurrency: 4, SubtaskMaxConcurrency: 2}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})
	_, err := tool.Run(context.Background(), map[string]any{
		"max_concurrency": 4,
		"tasks": []any{
			map[string]any{"id": "one", "mode": "analyze", "objective": "一", "material": "一"},
			map[string]any{"id": "two", "mode": "analyze", "objective": "二", "material": "二"},
			map[string]any{"id": "three", "mode": "analyze", "objective": "三", "material": "三"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	maxActive := provider.maxActive
	provider.mu.Unlock()
	if maxActive != 2 {
		t.Fatalf("peak concurrency = %d, want per-bot cap 2", maxActive)
	}
}

type selectiveFailureSubtaskProvider struct{}

func (*selectiveFailureSubtaskProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	content := req.Messages[len(req.Messages)-1].Content
	if strings.Contains(content, "失败材料") {
		return nil, errors.New("fixture failure")
	}
	return &llm.GenerateResponse{Text: "成功"}, nil
}

func TestSubtaskBatchIsolatesFailures(t *testing.T) {
	provider := &selectiveFailureSubtaskProvider{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", MaxBotConcurrency: 2}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})
	out, err := tool.Run(context.Background(), map[string]any{"tasks": []any{
		map[string]any{"id": "bad", "mode": "analyze", "objective": "总结", "material": "失败材料"},
		map[string]any{"id": "good", "mode": "analyze", "objective": "总结", "material": "正常材料"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var batch subtaskBatchResult
	if err := json.Unmarshal([]byte(out), &batch); err != nil {
		t.Fatal(err)
	}
	if batch.Results[0].Status != "failed" || !strings.Contains(batch.Results[0].Error, "fixture failure") {
		t.Fatalf("failed result = %#v", batch.Results[0])
	}
	if batch.Results[1].Status != "completed" || batch.Results[1].Output != "成功" {
		t.Fatalf("successful sibling = %#v", batch.Results[1])
	}
}

type subtaskSearchProbeProvider struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
}

func (p *subtaskSearchProbeProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	call := len(p.requests)
	p.mu.Unlock()
	if call == 1 {
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "search-1", Name: agent.WebSearchToolName, Arguments: map[string]any{"query": "official source"}}}}, nil
	}
	if call == 2 {
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "read-1", Name: "browser_render", Arguments: map[string]any{"url": "https://example.com/source"}}}}, nil
	}
	return &llm.GenerateResponse{Text: "已核验：https://example.com/source"}, nil
}

type subtaskSearchFixtureTool struct{ name string }

func (t subtaskSearchFixtureTool) Name() string        { return t.name }
func (t subtaskSearchFixtureTool) Description() string { return "fixture" }
func (t subtaskSearchFixtureTool) Run(context.Context, map[string]any) (string, error) {
	return `{"status":"ok","sources":["https://example.com/source"],"content":"official evidence"}`, nil
}

type subtaskSearchFixturePlugin struct{}

func (*subtaskSearchFixturePlugin) Manifest() PluginManifest {
	return PluginManifest{ID: "fixture.subtask-search", Name: "fixture", Version: "0.0.1", BuiltIn: true}
}
func (*subtaskSearchFixturePlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}
func (*subtaskSearchFixturePlugin) AgentTools(SettingValues) ([]agent.Tool, error) {
	return []agent.Tool{
		subtaskSearchFixtureTool{name: agent.WebSearchToolName},
		subtaskSearchFixtureTool{name: "browser_render"},
		subtaskSearchFixtureTool{name: "forbidden_write"},
	}, nil
}

func TestSearchSubtaskOnlyExposesReadOnlyResearchTools(t *testing.T) {
	provider := &subtaskSearchProbeProvider{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", MaxBotConcurrency: 2}.WithDefaults(), nilChannel{}, NewPluginManager(&subtaskSearchFixturePlugin{}), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := tool.Run(ctx, map[string]any{"tasks": []any{
		map[string]any{"id": "official", "mode": "search", "objective": "查找官方资料", "queries": []any{"official source"}, "source_policy": "primary"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var batch subtaskBatchResult
	if err := json.Unmarshal([]byte(out), &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 || batch.Results[0].Status != "completed" || !strings.Contains(batch.Results[0].Output, "example.com") {
		t.Fatalf("search result = %#v", batch.Results)
	}
	provider.mu.Lock()
	requests := append([]llm.GenerateRequest(nil), provider.requests...)
	provider.mu.Unlock()
	if len(requests) < 3 {
		t.Fatalf("provider requests = %d, want search, page read and synthesis", len(requests))
	}
	for _, definition := range requests[0].Tools {
		if definition.Name == "forbidden_write" {
			t.Fatalf("search worker received forbidden tool: %#v", requests[0].Tools)
		}
	}
	foundSearch, foundBrowser := false, false
	for _, definition := range requests[0].Tools {
		foundSearch = foundSearch || definition.Name == agent.WebSearchToolName
		foundBrowser = foundBrowser || definition.Name == "browser_render"
	}
	if !foundSearch || !foundBrowser {
		t.Fatalf("search worker tools = %#v", requests[0].Tools)
	}
}

func TestSubtaskBatchRejectsDuplicateIDsWithoutCallingProvider(t *testing.T) {
	runtime, provider := subtaskRuntime("unused")
	tool := newDianaSubtaskTool(runtime, MessageEvent{})
	_, err := tool.Run(context.Background(), map[string]any{"tasks": []any{
		map[string]any{"id": "same", "mode": "analyze", "objective": "一", "material": "甲"},
		map[string]any{"id": "same", "mode": "analyze", "objective": "二", "material": "乙"},
	}})
	if err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("duplicate id error = %v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("invalid batch reached provider: %d calls", len(provider.requests))
	}
}

func TestSubtaskStatusUnwrapsContextErrors(t *testing.T) {
	cases := map[string]error{
		"timed_out": fmt.Errorf("agent run: %w", context.DeadlineExceeded),
		"cancelled": fmt.Errorf("agent run: %w", context.Canceled),
		"failed":    errors.New("boom"),
	}
	for want, err := range cases {
		if got := subtaskStatus(err); got != want {
			t.Fatalf("subtaskStatus(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestSearchSubtaskEvidenceKeepsResultsForPartialOutput(t *testing.T) {
	evidence := &subtaskSearchEvidence{}
	if got := evidence.partialOutput(); got != "" {
		t.Fatalf("empty evidence should produce no output, got %q", got)
	}
	ctx := context.Background()
	evidence.observe(ctx, agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: agent.WebSearchToolName, ToolOutput: "第一批结果"})
	evidence.observe(ctx, agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "browser_render", ToolOutput: "正文证据"})
	evidence.observe(ctx, agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: agent.WebSearchToolName, Error: "boom", ToolOutput: "失败的输出"})
	evidence.observe(ctx, agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "other", ToolOutput: "别的工具"})
	evidence.observe(ctx, agent.RunEvent{Phase: agent.RunPhaseFailed, ModelTurn: 3, ToolsExecuted: 2})
	got := evidence.partialOutput()
	if !strings.Contains(got, "第一批结果") || !strings.Contains(got, "正文证据") || strings.Contains(got, "失败的输出") || strings.Contains(got, "别的工具") {
		t.Fatalf("partial output = %q", got)
	}
	if evidence.modelTurns != 3 || evidence.toolCalls != 2 {
		t.Fatalf("usage counters = %d/%d", evidence.modelTurns, evidence.toolCalls)
	}
}
