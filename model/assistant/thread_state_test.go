// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

type memoryThreadStateStore struct {
	mu    sync.Mutex
	items []ThreadState
	err   error
}

func (s *memoryThreadStateStore) PutThreadState(_ context.Context, request ThreadStatePutRequest) (ThreadState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return ThreadState{}, s.err
	}
	for index := range s.items {
		item := &s.items[index]
		if item.ProfileID == request.ProfileID && item.Session == request.Session && item.UserID == request.UserID && item.TaskKind == request.TaskKind && item.Status == ThreadStateActive {
			if request.ExpectedVersion > 0 && request.ExpectedVersion != item.Version {
				return ThreadState{}, ErrThreadStateVersionConflict
			}
			item.State = append(json.RawMessage(nil), request.State...)
			item.Version++
			item.UpdatedAt = request.Now
			item.ExpiresAt = request.ExpiresAt
			return *item, nil
		}
	}
	item := ThreadState{
		ID: fmt.Sprintf("state-%d", len(s.items)+1), ProfileID: request.ProfileID, Session: request.Session,
		UserID: request.UserID, TaskKind: request.TaskKind, State: append(json.RawMessage(nil), request.State...),
		Version: 1, Status: ThreadStateActive, CreatedAt: request.Now, UpdatedAt: request.Now, ExpiresAt: request.ExpiresAt,
	}
	s.items = append(s.items, item)
	return item, nil
}

func (s *memoryThreadStateStore) ListActiveThreadStates(_ context.Context, profileID, session, userID string, now time.Time, limit int) ([]ThreadState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := make([]ThreadState, 0, limit)
	for _, item := range s.items {
		if item.ProfileID == profileID && item.Session == session && item.UserID == userID && item.Status == ThreadStateActive && item.ExpiresAt.After(now) {
			item.State = append(json.RawMessage(nil), item.State...)
			out = append(out, item)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *memoryThreadStateStore) EndThreadState(_ context.Context, request ThreadStateEndRequest) (ThreadState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return ThreadState{}, s.err
	}
	for index := range s.items {
		item := &s.items[index]
		if item.ProfileID == request.ProfileID && item.Session == request.Session && item.UserID == request.UserID && item.TaskKind == request.TaskKind && item.Status == ThreadStateActive {
			if request.ExpectedVersion > 0 && request.ExpectedVersion != item.Version {
				return ThreadState{}, ErrThreadStateVersionConflict
			}
			item.State = nil
			item.Version++
			item.Status = request.Status
			item.UpdatedAt = request.Now
			return *item, nil
		}
	}
	return ThreadState{}, fmt.Errorf("not found")
}

func TestThreadStateToolPersistsAndInjectsPrivateContext(t *testing.T) {
	store := &memoryThreadStateStore{}
	now := time.Date(2026, 9, 1, 15, 24, 0, 0, time.Local)
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }
	runtime.SetThreadStateStore(store)
	event := MessageEvent{ProfileID: "bot-1", Kind: EventKindGroup, GroupID: "group-1", UserID: "user-a", MessageID: "m1"}
	tool := newDianaThreadStateTool(runtime, event)

	output, err := tool.Run(context.Background(), map[string]any{
		"operation": "set", "task_kind": "guess.character",
		"state": map[string]any{"target": "DIO", "rules": []any{"yes", "no", "uncertain"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"target":"DIO"`) {
		t.Fatalf("tool output = %s", output)
	}
	contextText := runtime.privateThreadStateContext(context.Background(), event)
	if !strings.Contains(contextText, privateThreadStateMarker) || !strings.Contains(contextText, `"target":"DIO"`) {
		t.Fatalf("private context = %q", contextText)
	}
	other := event
	other.UserID = "user-b"
	if got := runtime.privateThreadStateContext(context.Background(), other); got != "" {
		t.Fatalf("state leaked to another user: %q", got)
	}

	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "complete", "task_kind": "guess.character", "expected_version": float64(1),
	}); err != nil {
		t.Fatal(err)
	}
	if got := runtime.privateThreadStateContext(context.Background(), event); got != "" {
		t.Fatalf("completed state still injected: %q", got)
	}
}

type threadStateAgentClient struct {
	store *memoryThreadStateStore
	turn  int
}

func (c *threadStateAgentClient) Generate(_ context.Context, _ llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.turn++
	if c.turn == 1 {
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{
			ID: "call-state", Name: dianaThreadStateToolName,
			Arguments: map[string]any{
				"operation": "set", "task_kind": "guess.character",
				"state": map[string]any{"target": "DIO"},
			},
		}}}, nil
	}
	items, _ := c.store.ListActiveThreadStates(context.Background(), "bot-1", "group:group-1", "user-a", time.Now().Add(-time.Hour), 4)
	if len(items) != 1 || !strings.Contains(string(items[0].State), "DIO") {
		return nil, fmt.Errorf("state was not committed before final response")
	}
	return &llm.GenerateResponse{Text: `{"action":"final","content":"准备好了"}`}, nil
}

func TestAgentCommitsThreadStateBeforeReadyReply(t *testing.T) {
	store := &memoryThreadStateStore{}
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetThreadStateStore(store)
	event := MessageEvent{ProfileID: "bot-1", Kind: EventKindGroup, GroupID: "group-1", UserID: "user-a", MessageID: "m1"}
	client := &threadStateAgentClient{store: store}
	runner, err := agent.NewRunner(client, agent.Config{WorkDir: t.TempDir(), MaxSteps: 2}, agent.NewToolRegistry(newDianaThreadStateTool(runtime, event)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runner.Close() }()
	response, err := runner.Run(context.Background(), agent.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "默想一个角色，准备好了吗"}}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "准备好了" || client.turn != 2 {
		t.Fatalf("response = %#v, turns=%d", response, client.turn)
	}
}

func TestThreadStatePromptAndPermissions(t *testing.T) {
	store := &memoryThreadStateStore{}
	runtime := NewRuntime(BotConfig{}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetThreadStateStore(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"}
	registry := agent.NewToolRegistry(newDianaThreadStateTool(runtime, event))
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{}, true, registry)
	if !strings.Contains(prompt, "必须先调用 diana.thread_state set") {
		t.Fatalf("system prompt missing commit rule: %q", prompt)
	}
	if !(RelationshipPolicy{}).allowedAgentToolNames()[dianaThreadStateToolName] {
		t.Fatal("ordinary members cannot use private thread state")
	}
}

func TestThreadStateDebugAndCarryoverAreRedacted(t *testing.T) {
	secret := "DIO-PRIVATE-TARGET"
	req := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: privateThreadStateMarker + ` [{"state":{"target":"` + secret + `"}}]`},
		{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				Name: dianaThreadStateToolName,
				Arguments: map[string]any{
					"operation": "set",
					"task_kind": "guess.character",
					"state":     map[string]any{"target": secret},
				},
			}},
		},
		{Role: llm.RoleTool, ToolName: dianaThreadStateToolName, Content: `{"state":{"target":"` + secret + `"}}`},
	}}
	sanitized := sanitizeDebugGenerateRequest(req)
	encoded, _ := json.Marshal(sanitized)
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("request leaked private state: %s", encoded)
	}
	response := sanitizeDebugGenerateResponse(req, &llm.GenerateResponse{Text: secret, ToolCalls: req.Messages[1].ToolCalls})
	encoded, _ = json.Marshal(response)
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("response leaked private state: %s", encoded)
	}
	compat := sanitizeDebugGenerateResponse(req, &llm.GenerateResponse{Text: `{"action":"tool","tool":"diana.thread_state","input":{"state":{"target":"` + secret + `"}}}`})
	encoded, _ = json.Marshal(compat)
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("compat response leaked private state: %s", encoded)
	}
	entries := agentCarryoverEntries([]agent.Step{{Tool: dianaThreadStateToolName, Input: map[string]any{"state": secret}, Output: secret}})
	if len(entries) != 0 {
		t.Fatalf("private state entered carryover: %#v", entries)
	}
}
