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

// probeTool 是声明了自省的测试工具；onlyList 时只认 action=list，模拟一半只读一半动作。
type probeTool struct {
	countingTool
	onlyList bool
}

func (t *probeTool) Introspection(input map[string]any) bool {
	if !t.onlyList {
		return true
	}
	action, _ := input["action"].(string)
	return action == "list"
}

// askThenWorkClient 先把几个只读自省工具各问一遍，再把 MaxSteps 格预算全用于真工具。
type askThenWorkClient struct {
	asked    int
	realWork int
	budget   int
}

func (c *askThenWorkClient) Generate(_ context.Context, _ llm.GenerateRequest) (*llm.GenerateResponse, error) {
	probes := []struct {
		name  string
		input map[string]any
	}{
		{"capabilities", map[string]any{"query": "会什么"}},
		{"identity_check", map[string]any{}},
		{"extension_access", map[string]any{"action": "list"}},
	}
	if c.asked < len(probes) {
		probe := probes[c.asked]
		c.asked++
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{
			ID: fmt.Sprintf("ask-%d", c.asked), Name: probe.name, Arguments: probe.input,
		}}}, nil
	}
	if c.realWork < c.budget {
		c.realWork++
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{
			ID: fmt.Sprintf("work-%d", c.realWork), Name: "rare", Arguments: map[string]any{"query": fmt.Sprint(c.realWork)},
		}}}, nil
	}
	return &llm.GenerateResponse{Text: `{"action":"final","content":"done"}`}, nil
}

// 问「我能干什么」「这人是谁」不该算进干活的预算：线上抓到过 8 格里 5 格花在打听上。
func TestIntrospectionToolsDoNotConsumeStepBudget(t *testing.T) {
	client := &askThenWorkClient{budget: 2}
	rare := &countingTool{name: "rare"}
	runner, err := NewRunner(client, Config{MaxSteps: 2, CoreTools: []string{"rare", "capabilities", "identity_check", "extension_access"}}, NewToolRegistry(
		rare,
		&probeTool{countingTool: countingTool{name: "capabilities"}},
		&probeTool{countingTool: countingTool{name: "identity_check"}},
		&probeTool{countingTool: countingTool{name: "extension_access"}, onlyList: true},
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "先打听再干活"}},
	}); err != nil {
		t.Fatal(err)
	}
	if rare.calls != 2 {
		t.Fatalf("真工具应当跑满 %d 格预算，实际只跑了 %d 次（打听吃掉了预算？）", 2, rare.calls)
	}
}

// 判断交给工具自己：没声明的一律占预算，声明了的还能按入参分开看。
func TestIntrospectionIsDeclaredByTheToolItself(t *testing.T) {
	plain := &countingTool{name: "config"}
	if isIntrospectionCall(plain, map[string]any{"action": "list"}) {
		t.Fatal("没声明自省的工具不该因为入参长得像就放行")
	}
	listOnly := &probeTool{countingTool: countingTool{name: "extension_access"}, onlyList: true}
	if isIntrospectionCall(listOnly, map[string]any{"action": "bot_tier", "tier": "members"}) {
		t.Fatal("改档位是真动作，不该按只读自省放行")
	}
	if !isIntrospectionCall(listOnly, map[string]any{"action": "list"}) {
		t.Fatal("action=list 是只读的，应当放行")
	}
}

// 不占预算不等于无限量：反复打听必须被自己的配额拦住。
func TestIntrospectionQuotaStopsLoop(t *testing.T) {
	client := &askThenWorkClient{budget: 0}
	probe := &probeTool{countingTool: countingTool{name: "capabilities"}}
	runner, err := NewRunner(client, Config{MaxSteps: 2, CoreTools: []string{"capabilities", "identity_check", "extension_access"}}, NewToolRegistry(
		probe,
		&probeTool{countingTool: countingTool{name: "identity_check"}},
		&probeTool{countingTool: countingTool{name: "extension_access"}, onlyList: true},
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "一直打听"}},
	}); err != nil {
		t.Fatal(err)
	}
}
