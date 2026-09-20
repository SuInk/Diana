// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// loadThenWorkClient 先用掉一次 tools_load，再把 MaxSteps 格预算全部用于真工具。
type loadThenWorkClient struct {
	calls    int
	realWork int
}

func (c *loadThenWorkClient) Generate(_ context.Context, _ llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.calls++
	if c.calls == 1 {
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{
			ID: "load", Name: ToolsLoadToolName, Arguments: map[string]any{"names": []any{"rare"}},
		}}}, nil
	}
	if c.realWork < 2 {
		c.realWork++
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{
			ID:        fmt.Sprintf("work-%d", c.realWork),
			Name:      ToolsExecuteToolName,
			Arguments: map[string]any{"name": "rare", "input": map[string]any{"query": fmt.Sprint(c.realWork)}},
		}}}, nil
	}
	return &llm.GenerateResponse{Text: `{"action":"final","content":"done"}`}, nil
}

// tools_load 只从注册表取 schema，不做外部动作，不该占 MaxSteps。
//
// 延迟加载本来就已经多花一次模型往返（先 tools_load、看结果、再调真工具），再扣一格
// 预算等于「需要一个延迟工具的回合只剩 MaxSteps-1 格干正事」。
func TestToolsLoadDoesNotConsumeStepBudget(t *testing.T) {
	client := &loadThenWorkClient{}
	rare := &countingTool{name: "rare"}
	runner, err := NewRunner(client, Config{
		MaxSteps: 2, CoreTools: []string{"noop"},
	}, NewToolRegistry(rare, &countingTool{name: "noop"}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "用一下冷门工具两次"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// MaxSteps=2，加载占掉一格的话这里只会调到 1 次。
	if rare.calls != 2 {
		t.Fatalf("真工具应当跑满 %d 格预算，实际只跑了 %d 次（tools_load 吃掉了预算？）", 2, rare.calls)
	}
	if resp.Text != "done" {
		t.Fatalf("未正常收尾: %#v", resp)
	}
}

// spinningLoadClient 一直加载不同的工具，什么正事都不干。
type spinningLoadClient struct{ calls int }

func (c *spinningLoadClient) Generate(_ context.Context, _ llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.calls++
	if c.calls > 40 {
		return &llm.GenerateResponse{Text: `{"action":"final","content":"stopped"}`}, nil
	}
	return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{
		ID:        fmt.Sprintf("load-%d", c.calls),
		Name:      ToolsLoadToolName,
		Arguments: map[string]any{"names": []any{fmt.Sprintf("rare%d", c.calls)}},
	}}}, nil
}

// 不占 MaxSteps 不等于无限量：交替加载不同工具必须被自己的配额拦住。
func TestToolsLoadHasItsOwnQuota(t *testing.T) {
	client := &spinningLoadClient{}
	tools := []Tool{&countingTool{name: "noop"}}
	for i := 1; i <= 40; i++ {
		tools = append(tools, &countingTool{name: fmt.Sprintf("rare%d", i)})
	}
	runner, err := NewRunner(client, Config{
		MaxSteps: 8, CoreTools: []string{"noop"}, ProtocolRepairLimit: 8,
	}, NewToolRegistry(tools...))
	if err != nil {
		t.Fatal(err)
	}

	// 出错时 Run 返回的 response 是 nil，拿不到 Steps，所以用 Observer 数事件。
	var started, repaired int
	_, _ = runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "空转"}},
		Observer: func(_ context.Context, e RunEvent) {
			switch e.Phase {
			case RunPhaseToolStarted:
				if e.Tool == ToolsLoadToolName {
					started++
				}
			case RunPhaseProtocolRepair:
				repaired++
			}
		},
	})

	if started > maxToolLoadCallsPerAgentRun {
		t.Fatalf("tools_load 实际执行 %d 次，超过配额 %d", started, maxToolLoadCallsPerAgentRun)
	}
	if started != maxToolLoadCallsPerAgentRun {
		t.Fatalf("配额应当被用满再拦，实际只执行了 %d 次（配额 %d）", started, maxToolLoadCallsPerAgentRun)
	}
	if repaired == 0 {
		t.Fatal("超出配额后应当走协议修复提示模型，没有观察到")
	}
	if client.calls > 20 {
		t.Fatalf("空转没有被及时拦住，模型被调用了 %d 次", client.calls)
	}
	t.Logf("tools_load 执行 %d 次后被拦，协议修复 %d 次，模型调用 %d 次", started, repaired, client.calls)
}
