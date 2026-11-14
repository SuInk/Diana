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
	seenBudget int
}

func (t *longOutputTool) Name() string        { return t.name }
func (t *longOutputTool) Description() string { return "long output" }
func (t *longOutputTool) Run(ctx context.Context, _ map[string]any) (string, error) {
	t.seenBudget = ToolOutputBudget(ctx)
	return strings.Repeat("字", t.size), nil
}

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

// 所有工具共用同一个单次上限：上限内原样给出，超出时截断并写明总长和缺了多少。
func TestRunnerAppliesOneToolOutputLimit(t *testing.T) {
	fits := &longOutputTool{name: "fits", size: 12_000}
	step := runLongOutputTool(t, fits, Config{})
	if fits.seenBudget != ToolOutputMaxChars || step.Output != strings.Repeat("字", 12_000) {
		t.Fatalf("上限内不应截断：budget=%d len=%d", fits.seenBudget, len([]rune(step.Output)))
	}

	over := &longOutputTool{name: "over", size: ToolOutputMaxChars + 10}
	step = runLongOutputTool(t, over, Config{})
	if !strings.Contains(step.Output, "后面 10 字没有给出") {
		t.Fatalf("超出上限要写明缺了多少：%q", step.Output[len(step.Output)-200:])
	}
}
