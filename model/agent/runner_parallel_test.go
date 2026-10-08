// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type slowReadTool struct {
	name   string
	delays map[string]time.Duration
	mu     sync.Mutex
	order  []string
	active *atomic.Int32
	peak   *atomic.Int32
}

func (t *slowReadTool) Name() string        { return t.name }
func (t *slowReadTool) Description() string { return t.name }
func (t *slowReadTool) Run(_ context.Context, input map[string]any) (string, error) {
	key := stringFromInput(input, "query") + stringFromInput(input, "url")
	if now := t.active.Add(1); now > t.peak.Load() {
		t.peak.Store(now)
	}
	defer t.active.Add(-1)
	time.Sleep(t.delays[key])
	t.mu.Lock()
	t.order = append(t.order, key)
	t.mu.Unlock()
	return `{"result":"` + key + `"}`, nil
}

type batchClient struct {
	first    []llm.ToolCall
	requests []llm.GenerateRequest
}

func (c *batchClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if len(c.requests) == 1 {
		return &llm.GenerateResponse{ToolCalls: c.first}, nil
	}
	return &llm.GenerateResponse{Text: `{"action":"final","content":"done"}`}, nil
}

func toolResults(req llm.GenerateRequest) []llm.Message {
	var out []llm.Message
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleTool {
			out = append(out, msg)
		}
	}
	return out
}

func TestParallelReadsRunConcurrentlyAndKeepCallOrder(t *testing.T) {
	var active, peak atomic.Int32
	search := &slowReadTool{name: WebSearchToolName, active: &active, peak: &peak, delays: map[string]time.Duration{"slow": 60 * time.Millisecond}}
	render := &slowReadTool{name: browserRenderToolName, active: &active, peak: &peak, delays: map[string]time.Duration{"https://ai.google.dev/x": 20 * time.Millisecond}}
	client := &batchClient{first: []llm.ToolCall{
		{ID: "c1", Name: WebSearchToolName, Arguments: map[string]any{"query": "slow"}},
		{ID: "c2", Name: finalizeToolName, Arguments: map[string]any{"content": "太早了"}},
		{ID: "c3", Name: browserRenderToolName, Arguments: map[string]any{"url": "https://ai.google.dev/x"}},
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(search, render))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查一下"}}})
	if err != nil {
		t.Fatal(err)
	}
	if peak.Load() < 2 {
		t.Fatalf("read calls were not run concurrently, peak=%d", peak.Load())
	}
	// 慢的先发：完成顺序是 render 在前，但回填和步骤记录仍按模型的调用顺序。
	if search.order[0] != "slow" || len(render.order) != 1 {
		t.Fatalf("tools not executed: %v %v", search.order, render.order)
	}
	results := toolResults(client.requests[1])
	if len(results) != 3 || results[0].ToolCallID != "c1" || results[1].ToolCallID != "c2" || results[2].ToolCallID != "c3" {
		t.Fatalf("results must follow call order: %+v", results)
	}
	if !strings.Contains(results[0].Content, "slow") || results[0].ToolError || !results[1].ToolError || !strings.Contains(results[1].Content, "收尾没有执行") {
		t.Fatalf("unexpected results: %+v", results)
	}
	if len(resp.Steps) != 2 || resp.Steps[0].Tool != WebSearchToolName || resp.Steps[1].Tool != browserRenderToolName || resp.Steps[1].Index != 2 {
		t.Fatalf("steps out of order: %+v", resp.Steps)
	}
	if resp.Text != "done" {
		t.Fatalf("finalize should wait for the next step, got %q", resp.Text)
	}
}

func TestParallelReadsFallBackToSequentialWithSideEffects(t *testing.T) {
	var active, peak atomic.Int32
	search := &slowReadTool{name: WebSearchToolName, active: &active, peak: &peak, delays: map[string]time.Duration{}}
	writer := &countingTool{name: "memory_write"}
	client := &batchClient{first: []llm.ToolCall{
		{ID: "c1", Name: "memory_write", Arguments: map[string]any{"query": "先写"}},
		{ID: "c2", Name: WebSearchToolName, Arguments: map[string]any{"query": "再搜"}},
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(search, writer))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "记一下再查"}}}); err != nil {
		t.Fatal(err)
	}
	if writer.calls != 1 || len(search.order) != 0 {
		t.Fatalf("mixed batch must run only the first call in order: writes=%d searches=%v", writer.calls, search.order)
	}
}

func TestParallelReadsRespectStepBudget(t *testing.T) {
	var active, peak atomic.Int32
	search := &slowReadTool{name: WebSearchToolName, active: &active, peak: &peak, delays: map[string]time.Duration{}}
	client := &batchClient{first: []llm.ToolCall{
		{ID: "c1", Name: WebSearchToolName, Arguments: map[string]any{"query": "a"}},
		{ID: "c2", Name: WebSearchToolName, Arguments: map[string]any{"query": "b"}},
		{ID: "c3", Name: WebSearchToolName, Arguments: map[string]any{"query": "c"}},
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 2}, NewToolRegistry(search))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查"}}}); err != nil {
		t.Fatal(err)
	}
	if len(search.order) != 2 {
		t.Fatalf("ran %d searches with a budget of 2", len(search.order))
	}
	results := toolResults(client.requests[1])
	if len(results) != 3 || !results[2].ToolError || !strings.Contains(results[2].Content, "步数已用完") {
		t.Fatalf("over-budget call must be reported: %+v", results)
	}
}
