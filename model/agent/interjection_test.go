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
	ready   chan struct{}
	pending []llm.Message
}

func (q *queuedInterjections) push(text string) {
	q.mu.Lock()
	q.pending = append(q.pending, llm.Message{Role: llm.RoleUser, Content: text})
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func (q *queuedInterjections) Ready() <-chan struct{} { return q.ready }

func (q *queuedInterjections) Take() []llm.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	taken := q.pending
	q.pending = nil
	select {
	case <-q.ready:
	default:
	}
	return taken
}

// 第一步规划卡住时来了补充：这一步作废重做，补充进上下文，不计入模型轮次。
type interjectedClient struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
	started  chan struct{}
}

func (c *interjectedClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	first := len(c.requests) == 1
	c.mu.Unlock()
	if first {
		close(c.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &llm.GenerateResponse{Text: `{"action":"final","content":"带上价格的回答"}`}, nil
}

func TestRunnerRedoesPlanningStepWithInterjection(t *testing.T) {
	client := &interjectedClient{started: make(chan struct{})}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 3}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	q := &queuedInterjections{ready: make(chan struct{}, 1)}
	go func() {
		<-client.started
		q.push("顺便说下价格")
	}()
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "介绍下这款茶"}}, Interjections: q})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "带上价格的回答" || resp.ModelTurns != 1 {
		t.Fatalf("resp = %#v", resp)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d", len(client.requests))
	}
	last := client.requests[1].Messages
	if !strings.Contains(last[len(last)-1].Content, "顺便说下价格") {
		t.Fatalf("interjection missing from redo: %#v", last[len(last)-1])
	}
}
