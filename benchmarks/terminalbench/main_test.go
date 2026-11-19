package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type fakeClient struct {
	calls  int
	limits []int64
}

func TestTaskSurvivesToolHistoryBudget(t *testing.T) {
	task := "Write period,severity,count; use the reference date 2025-08-12."
	messages := []llm.Message{{Role: llm.RoleSystem, Content: "Agent rules", Priority: llm.MessagePrioritySystem}, taskMessage(task)}
	for i := 0; i < 20; i++ {
		messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: strings.Repeat("old observation ", 200)})
	}
	messages = append(messages, llm.Message{Role: llm.RoleTool, Content: "latest tool result"})
	kept := llm.FitMessagesToContextBudget(messages, 2048, 512)
	for _, m := range kept {
		if m.Content == task {
			return
		}
	}
	t.Fatal("current benchmark task was lost after tool history accumulated")
}

func (f *fakeClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	f.calls++
	f.limits = append(f.limits, req.MaxOutputTokens)
	return &llm.GenerateResponse{Text: "secret-example", Usage: llm.Usage{OutputTokens: req.MaxOutputTokens}}, nil
}

func TestBudgetAndRedactedLogs(t *testing.T) {
	var log bytes.Buffer
	inner := &fakeClient{}
	c := &recordingClient{inner: inner, log: json.NewEncoder(&log), key: "secret-example", budget: 10, perCall: 6}
	for i := 0; i < 2; i++ {
		if _, err := c.Generate(context.Background(), llm.GenerateRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Generate(context.Background(), llm.GenerateRequest{}); err == nil {
		t.Fatal("expected exhausted budget")
	}
	if inner.calls != 2 || inner.limits[0] != 6 || inner.limits[1] != 4 {
		t.Fatalf("wrong call limits: %+v", inner)
	}
	if bytes.Contains(log.Bytes(), []byte("secret-example")) {
		t.Fatal("key leaked into trajectory")
	}
	decoder := json.NewDecoder(&log)
	for i := 0; i < 2; i++ {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
	}
}
