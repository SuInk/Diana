// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

const testDisableReason = "当前是安全模式，这个操作被关掉了"

// 整体关掉的工具：取不到、不能被重新注册，点名调用时回的是配置给的原因，
// 而不是「查无此工具」或「需要主人来做」——主人本人也会撞上这道闸。
func TestDisabledToolReportsConfiguredReason(t *testing.T) {
	registry := NewToolRegistry(&countingTool{name: "alpha"}, &countingTool{name: "run_command"}, &countingTool{name: "agent_finalize"})
	registry.DisableTools(testDisableReason, "run_command", "write_file")
	registry.Register(&countingTool{name: "run_command"})
	if _, ok := registry.Get("run_command"); ok {
		t.Fatal("关掉的工具被重新注册回来了")
	}
	if !registry.PolicyDenied("write_file") {
		t.Fatal("从没注册过、但被关掉的工具应当算权限问题")
	}
	loader := newDeferredToolLoader(registry, []string{"agent_finalize"})
	_, err := loader.Run(t.Context(), map[string]any{"names": []any{"run_command"}})
	if err == nil || !strings.Contains(err.Error(), testDisableReason) || strings.Contains(err.Error(), "只对主人开放") {
		t.Fatalf("tools_load 报错 = %v", err)
	}
	if _, ok := registry.Get("alpha"); !ok {
		t.Fatal("没关掉的工具不该受影响")
	}
	summary := registry.DisabledSummary()
	if !strings.Contains(summary, "run_command") || !strings.Contains(summary, "write_file") || !strings.Contains(summary, testDisableReason) {
		t.Fatalf("提示词里的停用清单 = %q", summary)
	}
}

// 只关掉部分操作的工具照常出现在目录里，读操作照常执行；被关掉的操作不执行，
// 拒绝理由作为工具报错交回模型。大小写不同的操作名也要拦住。
func TestDisabledOperationIsRefusedWithoutRunningTool(t *testing.T) {
	tool := &countingTool{name: "platform"}
	registry := NewToolRegistry(tool, &countingTool{name: "agent_finalize"})
	registry.DisableOperations(testDisableReason, DisabledOperation{Tool: "platform", Field: "operation", Values: []string{"kick"}})
	if err := registry.OperationDisabledError("platform", map[string]any{"operation": "group_info"}); err != nil {
		t.Fatalf("只读操作被拦了: %v", err)
	}
	if err := registry.OperationDisabledError("platform", map[string]any{"operation": " KICK "}); err == nil {
		t.Fatal("大小写不同的操作名绕过了拦截")
	}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"platform","input":{"operation":"kick"}}`,
		`{"action":"final","content":"安全模式下踢不了"}`,
	}}
	runner := &Runner{client: client, cfg: Config{}.WithDefaults(), registry: registry}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "踢了他"}}})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 0 {
		t.Fatalf("被关掉的操作仍然执行了 %d 次", tool.calls)
	}
	if len(resp.Steps) == 0 || !strings.Contains(resp.Steps[0].Error, testDisableReason) {
		t.Fatalf("步骤记录 = %+v", resp.Steps)
	}
}

// MCP 工具按前缀整体关掉：之后才发现的工具也一样取不到，报的是配置给的原因。
func TestDisableMCPToolsBlocksEveryMCPTool(t *testing.T) {
	registry := NewToolRegistry(&MCPTool{modelName: "mcp__notes__search", serverName: "notes"})
	registry.DisableMCPTools(testDisableReason)
	if _, ok := registry.Get("mcp__notes__search"); ok {
		t.Fatal("MCP 工具在关掉之后仍然可取")
	}
	if len(registry.Names()) != 0 {
		t.Fatalf("目录里还有 MCP 工具: %v", registry.Names())
	}
	if reason, ok := registry.DisabledReason("mcp__later__tool"); !ok || reason != testDisableReason {
		t.Fatalf("后来的 MCP 工具原因 = %q, %v", reason, ok)
	}
	if !registry.PolicyDenied("mcp__notes__search") {
		t.Fatal("关掉的 MCP 工具应当算权限问题")
	}
}
