// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// longOutputTool 返回固定长度的结果，并记下 Runner 给它的预算。
type longOutputTool struct {
	name       string
	size       int
	declared   int
	seenBudget int
}

func (t *longOutputTool) Name() string        { return t.name }
func (t *longOutputTool) Description() string { return "long output" }
func (t *longOutputTool) Run(ctx context.Context, _ map[string]any) (string, error) {
	t.seenBudget = ToolOutputBudget(ctx)
	return strings.Repeat("字", t.size), nil
}

type budgetedLongOutputTool struct{ *longOutputTool }

func (t budgetedLongOutputTool) MaxOutputChars() int { return t.declared }

func runLongOutputTool(t *testing.T, tool Tool, cfg Config) Step {
	t.Helper()
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"` + tool.Name() + `","input":{}}`,
		`{"action":"final","content":"done"}`,
	}}
	cfg.WorkDir, cfg.MaxSteps = t.TempDir(), 2
	runner, err := NewRunner(client, cfg, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "读"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Steps) != 1 {
		t.Fatalf("steps=%#v", resp.Steps)
	}
	return resp.Steps[0]
}

// 声明了上限的工具按声明值截，并能从 ctx 读到这次的预算；声明值不能越过全局硬上限。
func TestRunnerHonorsDeclaredToolOutputBudget(t *testing.T) {
	plain := &longOutputTool{name: "plain", size: 12_000}
	step := runLongOutputTool(t, plain, Config{})
	if plain.seenBudget != DefaultMaxToolOutputChars {
		t.Fatalf("未声明的工具应拿到默认预算，got %d", plain.seenBudget)
	}
	if !strings.HasPrefix(step.Output, strings.Repeat("字", DefaultMaxToolOutputChars)+"\n...[结果共 12000 字") {
		t.Fatalf("默认上限截断并说明总长：%q", step.Output[len(step.Output)-200:])
	}

	wide := budgetedLongOutputTool{&longOutputTool{name: "wide", size: 12_000, declared: 15_000}}
	step = runLongOutputTool(t, wide, Config{})
	if wide.seenBudget != 15_000 || step.Output != strings.Repeat("字", 12_000) {
		t.Fatalf("声明 15000 的工具不应被截：budget=%d len=%d", wide.seenBudget, len([]rune(step.Output)))
	}

	greedy := budgetedLongOutputTool{&longOutputTool{name: "greedy", size: MaxAllowedToolOutputChars + 10, declared: 1 << 20}}
	step = runLongOutputTool(t, greedy, Config{})
	if greedy.seenBudget != MaxAllowedToolOutputChars || !strings.Contains(step.Output, "后面 10 字没有给出") {
		t.Fatalf("声明值受全局硬上限约束：budget=%d", greedy.seenBudget)
	}

	narrow := budgetedLongOutputTool{&longOutputTool{name: "narrow", size: 9_000, declared: 100}}
	step = runLongOutputTool(t, narrow, Config{})
	if narrow.seenBudget != DefaultMaxToolOutputChars || !strings.Contains(step.Output, "只给了前 8000 字") {
		t.Fatalf("声明值只能放宽不能收紧：budget=%d", narrow.seenBudget)
	}
}
