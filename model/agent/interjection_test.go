// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type queuedInterjections struct {
	mu      sync.Mutex
	pending []llm.Message
}

func (q *queuedInterjections) push(text string) {
	q.mu.Lock()
	q.pending = append(q.pending, llm.Message{Role: llm.RoleUser, Content: text})
	q.mu.Unlock()
}

func (q *queuedInterjections) Pending() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending) > 0
}

func (q *queuedInterjections) Take() []llm.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	taken := q.pending
	q.pending = nil
	return taken
}

// 第一步规划途中来了补充：这一步照常跑完（调工具），补充在下一步规划前接进上下文。
type interjectedClient struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
	onFirst  func()
}

func (c *interjectedClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	first := len(c.requests) == 1
	c.mu.Unlock()
	if first {
		c.onFirst()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return &llm.GenerateResponse{Text: `{"action":"tool","tool":"lookup","input":{}}`}, nil
	}
	return &llm.GenerateResponse{Text: `{"action":"final","content":"带上价格的回答"}`}, nil
}

type interjectionLookupTool struct{}

func (interjectionLookupTool) Name() string        { return "lookup" }
func (interjectionLookupTool) Description() string { return "查资料" }
func (interjectionLookupTool) Run(context.Context, map[string]any) (string, error) {
	return "茶的介绍", nil
}

func TestRunnerKeepsPlanningStepAndTakesInterjectionNext(t *testing.T) {
	q := &queuedInterjections{}
	client := &interjectedClient{onFirst: func() { q.push("顺便说下价格") }}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 3}, NewToolRegistry(interjectionLookupTool{}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "介绍下这款茶"}}, Interjections: q})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "带上价格的回答" {
		t.Fatalf("resp = %#v", resp)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, the interrupted step must not be redone", len(client.requests))
	}
	found := false
	for _, message := range client.requests[1].Messages {
		if strings.Contains(message.Content, "顺便说下价格") {
			found = true
		}
	}
	if !found {
		t.Fatal("interjection missing from the next planning step")
	}
}

// 收尾这一步回来时补充还没接进去：答案还没发出去，丢掉它带着补充重做，只出一份答案。
type finalThenRedoClient struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
	onFirst  func()
}

func (c *finalThenRedoClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	first := len(c.requests) == 1
	c.mu.Unlock()
	if first {
		c.onFirst()
		return &llm.GenerateResponse{Text: `{"action":"final","content":"只回答了第一条"}`}, nil
	}
	return &llm.GenerateResponse{Text: `{"action":"final","content":"带上补充的回答"}`}, nil
}

func TestRunnerRedoesFinalStepWhenInterjectionArrives(t *testing.T) {
	q := &queuedInterjections{}
	client := &finalThenRedoClient{onFirst: func() { q.push("顺便说下价格") }}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 3}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "介绍下这款茶"}}, Interjections: q})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "带上补充的回答" || len(client.requests) != 2 {
		t.Fatalf("resp = %q, requests = %d", resp.Text, len(client.requests))
	}
	last := client.requests[1].Messages
	if !strings.Contains(last[len(last)-1].Content, "顺便说下价格") {
		t.Fatalf("redo missed the interjection: %#v", last[len(last)-1])
	}
}

// 人一直在补的话，重做次数有上限，第三次照常收尾。
func TestRunnerBoundsFinalStepRedos(t *testing.T) {
	q := &queuedInterjections{}
	client := &finalThenRedoClient{}
	client.onFirst = func() {}
	runner, err := NewRunner(alwaysInterjectClient{client: client, q: q}, Config{WorkDir: t.TempDir(), MaxSteps: 5}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "介绍下这款茶"}}, Interjections: q}); err != nil {
		t.Fatal(err)
	}
	if got := len(client.requests); got != maxInterjectionRedos+1 {
		t.Fatalf("requests = %d, want %d", got, maxInterjectionRedos+1)
	}
}

type alwaysInterjectClient struct {
	client *finalThenRedoClient
	q      *queuedInterjections
}

func (c alwaysInterjectClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.q.push("又补了一句")
	return c.client.Generate(ctx, req)
}
