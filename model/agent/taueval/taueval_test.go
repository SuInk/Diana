// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package taueval

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// queueClient 依次返回预设响应；userTurns 用来区分用户模拟器的请求。
type queueClient struct {
	mu        sync.Mutex
	responses []*llm.GenerateResponse
	userTurns []string
}

func (c *queueClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.Contains(req.Messages[0].Content, "扮演一位真人用户") {
		next := stopToken
		if len(c.userTurns) > 0 {
			next, c.userTurns = c.userTurns[0], c.userTurns[1:]
		}
		return &llm.GenerateResponse{Text: next}, nil
	}
	next := c.responses[0]
	c.responses = c.responses[1:]
	return next, nil
}

func call(name string, args map[string]any) *llm.GenerateResponse {
	return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: name, Name: name, Arguments: args}}}
}

func finalize(text string) *llm.GenerateResponse {
	return call("send", map[string]any{"content": text})
}

func reminderCase() Case {
	return Case{
		ID:       "reminder",
		Messages: []llm.Message{{Role: llm.RoleSystem, Content: "你是 Diana"}, {Role: llm.RoleUser, Content: "【当前需要回复的消息】群友: 明早提醒我开会"}},
		Tools: []ToolSpec{{Name: "reminder", Description: "管理提醒", Actions: map[string]ActionSpec{
			"create": {Op: "append", Collection: "reminders", Key: "id", Fields: []string{"time", "text"}},
			"list":   {Op: "read", Collection: "reminders"},
			"delete": {Op: "remove", Collection: "reminders", Key: "id"},
		}}},
		State: map[string]any{"reminders": []any{map[string]any{"id": "old", "time": "2026-10-01 08:00", "text": "旧的"}}},
		User:  UserSpec{Name: "群友", Instructions: "想要明天 9 点的开会提醒，并删掉旧提醒"},
		Expect: Expect{
			State:        map[string]any{"reminders": []any{map[string]any{"time": "re:^2026-10-20 09:00", "text": "re:开会"}}},
			Exact:        []string{"reminders"},
			RequireTools: []string{"reminder"},
			Replies:      []string{"9 点"},
		},
	}
}

func TestRunTrialMultiTurnStateCheck(t *testing.T) {
	agentClient := &queueClient{
		responses: []*llm.GenerateResponse{
			finalize("几点呀？"),
			call("reminder", map[string]any{"action": "create", "time": "2026-10-20 09:00", "text": "开会"}),
			call("reminder", map[string]any{"action": "delete", "id": "old"}),
			finalize("好，明天 9 点提醒你，旧的也删了"),
		},
		userTurns: []string{"9 点，顺便把旧的删了"},
	}
	trial := RunTrial(context.Background(), Options{Agent: agentClient}, reminderCase(), 0)
	if !trial.Pass {
		t.Fatalf("trial failed: %v %s turns=%+v state=%v", trial.Failures, trial.Error, trial.Turns, trial.State)
	}
	if len(trial.Turns) != 2 || len(trial.Calls) != 2 {
		t.Fatalf("turns=%d calls=%d", len(trial.Turns), len(trial.Calls))
	}
}

func TestRunTrialFailsWhenStaleRecordRemains(t *testing.T) {
	agentClient := &queueClient{responses: []*llm.GenerateResponse{
		call("reminder", map[string]any{"action": "create", "time": "2026-10-20 09:00", "text": "开会"}),
		finalize("好，明天 9 点提醒你"),
	}}
	trial := RunTrial(context.Background(), Options{Agent: agentClient}, reminderCase(), 0)
	if trial.Pass || !strings.Contains(strings.Join(trial.Failures, ";"), "state.reminders") {
		t.Fatalf("exact collection should fail with the stale reminder left: %v", trial.Failures)
	}
}

func TestMatchesAssignsDistinctRecords(t *testing.T) {
	want := []any{map[string]any{"text": "a"}, map[string]any{"text": "a"}}
	if matches(want, []any{map[string]any{"text": "a", "id": "1"}}, false) {
		t.Fatal("two expected records must not share one actual record")
	}
	if !matches(map[string]any{"gone": nil}, map[string]any{}, false) {
		t.Fatal("null expects the key to be absent")
	}
}

func TestPassHatK(t *testing.T) {
	// 任务一 4/4，任务二 2/4：pass^1 = (1+0.5)/2，pass^2 = (1 + C(2,2)/C(4,2))/2。
	successes, trials := []int{4, 2}, []int{4, 4}
	for k, want := range map[int]float64{1: 0.75, 2: (1 + 1.0/6) / 2, 3: 0.5, 4: 0.5} {
		if got := PassHatK(successes, trials, k); math.Abs(got-want) > 1e-9 {
			t.Fatalf("pass^%d = %v, want %v", k, got, want)
		}
	}
}

func TestEvaluateAggregates(t *testing.T) {
	c := reminderCase()
	c.User = UserSpec{Name: "群友", Script: []string{}, Instructions: "x"}
	c.Expect = Expect{Replies: []string{"好"}}
	client := &queueClient{}
	for range 3 {
		client.responses = append(client.responses, finalize("好"))
	}
	report := Evaluate(context.Background(), Options{Agent: client, Trials: 3, Parallel: 1}, []Case{c})
	if report.Cases[0].Successes != 3 || report.PassHatK[2] != 1 {
		t.Fatalf("report = %+v", report.Cases)
	}
}

func TestRepositoryExamplesLoad(t *testing.T) {
	cases, err := LoadCases("../../../docs/evals/tau")
	if err != nil || len(cases) == 0 {
		t.Fatalf("cases=%d err=%v", len(cases), err)
	}
}

func TestCheckCallsMatchesInputSubset(t *testing.T) {
	expect := Expect{Calls: []Call{{Tool: "bot_config", Input: map[string]any{"operation": "set", "enabled": true}}}}
	ok := []Call{{Tool: "bot_config", Input: map[string]any{"operation": "set", "enabled": true, "scope": "group"}}}
	if failures := CheckCalls(expect, ok); len(failures) != 0 {
		t.Fatal(failures)
	}
	failed := []Call{{Tool: "bot_config", Input: map[string]any{"operation": "set", "enabled": true}, Error: "denied"}}
	if failures := CheckCalls(expect, failed); len(failures) == 0 {
		t.Fatal("a failed call must not satisfy the expectation")
	}
}
