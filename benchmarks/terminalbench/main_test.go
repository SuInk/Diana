package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type fakeClient struct {
	calls  int
	limits []int64
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
