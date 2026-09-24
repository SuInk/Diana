// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// planningTimeoutClient 第一次调用按 mode 失败，之后给出最终答复。
type planningTimeoutClient struct {
	mode     string
	requests []llm.GenerateRequest
}

func (c *planningTimeoutClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if len(c.requests) == 1 {
		switch c.mode {
		case "client-timeout":
			// 模型客户端自己的首包超时：包着 DeadlineExceeded，但规划期限还远没到。
			return nil, fmt.Errorf("等待模型响应头超时: %w", context.DeadlineExceeded)
		case "planning-expired":
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	return &llm.GenerateResponse{Text: `{"action":"final","content":"已有信息的总结"}`}, nil
}

func TestRunnerClientTimeoutIsNotReportedAsPlanningBudget(t *testing.T) {
	client := &planningTimeoutClient{mode: "client-timeout"}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查一下"}}})
	if err == nil || !strings.Contains(err.Error(), "等待模型响应头超时") {
		t.Fatalf("expected the client's own timeout to surface, got %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d, want 1: a provider timeout must not trigger the finalization fallback", len(client.requests))
	}
	for _, req := range client.requests {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "规划时间已用尽") {
				t.Fatalf("model was told the planning budget ran out: %q", msg.Content)
			}
		}
	}
}

func TestRunnerPlanningDeadlineStillFallsBackToFinalization(t *testing.T) {
	client := &planningTimeoutClient{mode: "planning-expired"}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	resp, err := runner.Run(ctx, Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查一下"}}})
	if err != nil {
		t.Fatalf("planning deadline should fall back to finalization, got %v", err)
	}
	if resp.Text != "已有信息的总结" {
		t.Fatalf("Text = %q", resp.Text)
	}
	found := false
	for _, msg := range client.requests[len(client.requests)-1].Messages {
		if strings.Contains(msg.Content, "规划时间已用尽") {
			found = true
		}
	}
	if !found {
		t.Fatal("finalization request should tell the model the planning budget ran out")
	}
}
