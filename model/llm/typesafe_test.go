// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func typeSafeTestSpec() *DecisionSpec {
	return &DecisionSpec{Questions: []DecisionQuestion{
		{
			Key:           "relevance",
			Kind:          DecisionNoul,
			Label:         "在跟机器人说话",
			Instructions:  "当前消息是不是明确在跟机器人说话。",
			TrueCriteria:  "@ 或引用机器人",
			FalseCriteria: "群友彼此聊天",
			Path:          "relevance.directed",
			ReasonPath:    "relevance.reason",
		},
		{
			Key:          "chat_in",
			Kind:         DecisionScore,
			Label:        "闲聊适合度",
			Instructions: "插一句是否自然。",
			Levels:       []string{"低", "中", "高"},
			Max:          1,
			Path:         "chat_in.score",
			ReasonPath:   "chat_in.reason",
		},
	}}
}

func TestTypeSafeGenerateRendersTheContract(t *testing.T) {
	var captured struct {
		Model     string                     `json:"model"`
		State     []string                   `json:"state"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != typeSafeDecisionPath {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			t.Errorf("unexpected authorization %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("request is not valid JSON: %v", err)
		}
		_, _ = io.WriteString(w, `{"model":"jev-1.13.0","answers":{"relevance":{"type":"noul","noul":0.91},"chat_in":{"type":"score","score":1.0,"confidence":0.7}},"usage":{"input_tokens":120,"output_tokens":0}}`)
	}))
	defer server.Close()

	client := newTypeSafeClient(ProviderConfig{Provider: ProviderTypeSafe, APIKey: "key", BaseURL: server.URL, Model: "jev-latest"}, server.Client())
	resp, err := client.Generate(context.Background(), GenerateRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "本轮回应提问：开"},
			{Role: RoleUser, Parts: []ContentPart{{Type: ContentPartText, Text: "diana 看看这个"}, {Type: ContentPartImageURL, ImageURL: "https://example.com/a.png"}}},
		},
		Decision: typeSafeTestSpec(),
	})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if resp.Model != "jev-1.13.0" || resp.Provider != ProviderTypeSafe {
		t.Fatalf("unexpected response identity: %+v", resp)
	}
	if resp.Usage.InputTokens != 120 || resp.Usage.TotalTokens != 120 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
	if len(captured.Questions) != 2 {
		t.Fatalf("expected both questions on the wire, got %v", captured.Questions)
	}
	if len(captured.State) != 1 || !strings.Contains(captured.State[0], "diana 看看这个") {
		t.Fatalf("expected the user message in state, got %v", captured.State)
	}
	if !strings.Contains(captured.State[0], "图片 ×1") {
		t.Fatalf("expected images to survive as a marker, got %v", captured.State)
	}
	// 系统提示词没有对应角色，跟在每道题的 instructions 后面。
	if !strings.Contains(string(captured.Questions["relevance"]), "本轮回应提问：开") {
		t.Fatalf("expected the system prompt to reach the question, got %s", captured.Questions["relevance"])
	}
	var decoded struct {
		Relevance struct {
			Directed *bool  `json:"directed"`
			Reason   string `json:"reason"`
		} `json:"relevance"`
		ChatIn struct {
			Score *float64 `json:"score"`
		} `json:"chat_in"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &decoded); err != nil {
		t.Fatalf("rendered text is not valid JSON: %v (%s)", err, resp.Text)
	}
	if decoded.Relevance.Directed == nil || !*decoded.Relevance.Directed || decoded.Relevance.Reason == "" {
		t.Fatalf("unexpected relevance: %s", resp.Text)
	}
	if decoded.ChatIn.Score == nil || *decoded.ChatIn.Score != 0.5 {
		t.Fatalf("expected the middle level to map to 0.5, got %s", resp.Text)
	}
}

func TestTypeSafeGenerateRejectsTextOnlyWork(t *testing.T) {
	client := newTypeSafeClient(ProviderConfig{Provider: ProviderTypeSafe, APIKey: "key", Model: "jev-latest"}, http.DefaultClient)
	_, err := client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "写一段总结"}}})
	if !errors.Is(err, ErrDecisionRequired) {
		t.Fatalf("expected ErrDecisionRequired, got %v", err)
	}
	_, err = client.Generate(context.Background(), GenerateRequest{
		Messages: []Message{{Role: RoleUser, Content: "查一下"}},
		Tools:    []ToolDefinition{{Name: "search"}},
		Decision: typeSafeTestSpec(),
	})
	if !errors.Is(err, ErrDecisionRequired) {
		t.Fatalf("expected tool calls to be refused, got %v", err)
	}
}

func TestTypeSafeGenerateSurfacesUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate limited"}`)
	}))
	defer server.Close()
	client := newTypeSafeClient(ProviderConfig{Provider: ProviderTypeSafe, APIKey: "key", BaseURL: server.URL, Model: "jev-latest"}, server.Client())
	_, err := client.Generate(context.Background(), GenerateRequest{
		Messages: []Message{{Role: RoleUser, Content: "在吗"}},
		Decision: typeSafeTestSpec(),
	})
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("expected the upstream status in the error, got %v", err)
	}
}
