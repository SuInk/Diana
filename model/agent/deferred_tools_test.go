// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type deferredLoadClient struct {
	requests []llm.GenerateRequest
}

func (c *deferredLoadClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	switch len(c.requests) {
	case 1:
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "load", Name: ToolsLoadToolName, Arguments: map[string]any{"names": []any{"rare"}}}}}, nil
	case 2:
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "rare", Name: "rare", Arguments: map[string]any{"query": "x"}}}}, nil
	default:
		return &llm.GenerateResponse{Text: `{"action":"final","content":"done"}`}, nil
	}
}

func toolDefinitionNames(definitions []llm.ToolDefinition) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return names
}

func TestRunnerDefersNonCoreToolsUntilLoaded(t *testing.T) {
	client := &deferredLoadClient{}
	common := &countingTool{name: "common"}
	rare := &countingTool{name: "rare"}
	runner, err := NewRunner(client, Config{MaxSteps: 4, CoreTools: []string{"common"}}, NewToolRegistry(common, rare))
	if err != nil {
		t.Fatal(err)
	}
	response, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "用一下冷门工具"}}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "done" || rare.calls != 1 || len(client.requests) != 3 {
		t.Fatalf("response=%#v rare calls=%d requests=%d", response, rare.calls, len(client.requests))
	}
	first := strings.Join(toolDefinitionNames(client.requests[0].Tools), ",")
	if first != "common,"+ToolsLoadToolName+","+finalizeToolName {
		t.Fatalf("first turn tools = %s", first)
	}
	second := strings.Join(toolDefinitionNames(client.requests[1].Tools), ",")
	if second != "common,rare,"+finalizeToolName {
		t.Fatalf("after loading, tools = %s", second)
	}
	system := client.requests[0].Messages[0].Content
	if !strings.Contains(system, "按需加载的工具") || !strings.Contains(system, "- rare:") {
		t.Fatalf("system prompt lacks deferred catalog:\n%s", system)
	}
	if strings.Contains(system, "- common:") {
		t.Fatalf("core tool repeated in system catalog:\n%s", system)
	}
}

func TestRunnerKeepsAllToolDefinitionsWithoutCoreTools(t *testing.T) {
	client := &scriptedClient{}
	runner, err := NewRunner(client, Config{MaxSteps: 2}, NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "rare"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(toolDefinitionNames(client.requests[0].Tools), ","); names != "common,rare,"+finalizeToolName {
		t.Fatalf("tools = %s", names)
	}
	if system := client.requests[0].Messages[0].Content; strings.Contains(system, "按需加载的工具") {
		t.Fatal("deferred catalog present without CoreTools")
	}
}

func TestToolsLoadRejectsUnknownNamesAndSystemCatalogStaysStable(t *testing.T) {
	registry := NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "rare"})
	loader := newDeferredToolLoader(registry, []string{"common"})
	before := loader.catalog()
	if _, err := loader.Run(context.Background(), map[string]any{"names": []any{"missing"}}); err == nil {
		t.Fatal("unknown tool accepted")
	}
	output, err := loader.Run(context.Background(), map[string]any{"names": []any{"rare", "missing"}})
	if err != nil || !strings.Contains(output, "已加载：rare") || !strings.Contains(output, "没有找到：missing") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if after := loader.catalog(); after != before {
		t.Fatalf("catalog changed after loading; prompt cache would break:\n%s\n---\n%s", before, after)
	}
	if names := strings.Join(toolDefinitionNames(loader.filter(registry.Definitions())), ","); names != "common,rare" {
		t.Fatalf("filtered = %s", names)
	}
}
