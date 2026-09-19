// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "rare", Name: ToolsExecuteToolName, Arguments: map[string]any{"name": "rare", "input": map[string]any{"query": "x"}}}}}, nil
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
	if first != "common,"+ToolsLoadToolName+","+ToolsExecuteToolName+","+finalizeToolName {
		t.Fatalf("first turn tools = %s", first)
	}
	second := strings.Join(toolDefinitionNames(client.requests[1].Tools), ",")
	if second != first {
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
	output, err := loader.Run(context.Background(), map[string]any{"names": []any{"rare"}})
	if err != nil || !strings.Contains(output, `"name":"rare"`) || !strings.Contains(output, `"inputSchema"`) {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if after := loader.catalog(); after != before {
		t.Fatalf("catalog changed after loading; prompt cache would break:\n%s\n---\n%s", before, after)
	}
	if names := strings.Join(toolDefinitionNames(loader.filter(registry.Definitions())), ","); names != "common,tools.load,tools.execute" {
		t.Fatalf("filtered = %s", names)
	}
}

func TestDeferredDefinitionsRemainByteStable(t *testing.T) {
	registry := NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: webSearchToolName}, &countingTool{name: "rare"})
	r, _ := NewRunner(&scriptedClient{}, Config{CoreTools: []string{"common", webSearchToolName}}, registry)
	r.loader = newDeferredToolLoader(registry, r.cfg.CoreTools)
	ledger := newClaimEvidenceLedger()
	before, _ := json.Marshal(r.turnDefinitions(ledger, false))
	if _, err := r.loader.Run(context.Background(), map[string]any{"names": []string{"rare"}}); err != nil {
		t.Fatal(err)
	}
	ledger.prepareSearch(map[string]any{"claims": []any{map[string]any{"id": "c1", "statement": "fact"}}, "claim_ids": []any{"c1"}})
	ledger.observeSearch(`{"status":"ok","sources":["https://example.org"]}`, nil)
	after, _ := json.Marshal(r.turnDefinitions(ledger, true))
	if string(before) != string(after) {
		t.Fatalf("definitions changed: %s -> %s", before, after)
	}
}

type dispatchTestClient struct {
	replies  []*llm.GenerateResponse
	requests []llm.GenerateRequest
	hook     func(int)
}

func (c *dispatchTestClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if c.hook != nil {
		c.hook(len(c.requests))
	}
	if len(c.replies) == 0 {
		return &llm.GenerateResponse{Text: `{"action":"final","content":"done"}`}, nil
	}
	result := c.replies[0]
	c.replies = c.replies[1:]
	return result, nil
}
func dispatchReply(native bool, name string, input map[string]any) *llm.GenerateResponse {
	if native {
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "call-" + name, Name: name, Arguments: input}}}
	}
	raw, _ := json.Marshal(map[string]any{"action": "tool", "tool": name, "input": input})
	return &llm.GenerateResponse{Text: string(raw)}
}
func loadReply(native bool, names ...string) *llm.GenerateResponse {
	return dispatchReply(native, ToolsLoadToolName, map[string]any{"names": names})
}
func executeReply(native bool, name string, input map[string]any) *llm.GenerateResponse {
	return dispatchReply(native, ToolsExecuteToolName, map[string]any{"name": name, "input": input})
}

type schemaDispatchTool struct {
	countingTool
	schema map[string]any
}

func (t *schemaDispatchTool) InputSchema() map[string]any { return t.schema }
func TestDeferredDispatchRejectsBypassAndInvalidInput(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, name := range []string{"diana.rare", "mcp__demo__rare"} {
			for _, kind := range []string{"direct", "direct_after_load", "unloaded", "missing", "type", "enum", "extra", "removed", "changed", "empty", "no_input", "array_input", "unknown", "valid"} {
				t.Run(fmt.Sprintf("native=%v/%s/%s", native, name, kind), func(t *testing.T) {
					tool := &schemaDispatchTool{countingTool: countingTool{name: name}, schema: toolObjectSchema([]string{"count"}, map[string]any{"count": map[string]any{"type": "integer", "enum": []int{1, 2}}})}
					registry := NewToolRegistry(&countingTool{name: "common"}, tool)
					input := map[string]any{"count": 1}
					client := &dispatchTestClient{}
					if kind != "unloaded" && kind != "direct" {
						client.replies = append(client.replies, loadReply(native, name))
					}
					reply := executeReply(native, name, input)
					switch kind {
					case "direct", "direct_after_load":
						reply = dispatchReply(native, name, input)
					case "missing":
						delete(input, "count")
					case "type":
						input["count"] = "PRIVATE-SECRET"
					case "enum":
						input["count"] = 3
					case "extra":
						input["extra"] = true
					case "removed":
						client.hook = func(n int) {
							if n == 2 {
								registry.Remove(name)
							}
						}
					case "changed":
						client.hook = func(n int) {
							if n == 2 {
								tool.schema["required"] = []string{"new"}
							}
						}
					case "empty":
						reply = executeReply(native, "", input)
					case "unknown":
						reply = executeReply(native, "unknown", input)
					case "no_input":
						reply = dispatchReply(native, ToolsExecuteToolName, map[string]any{"name": name})
					case "array_input":
						reply = dispatchReply(native, ToolsExecuteToolName, map[string]any{"name": name, "input": []any{}})
					}
					// Compatibility JSON is serialized eagerly, so capture final mutated input.
					if kind == "missing" || kind == "type" || kind == "enum" || kind == "extra" {
						reply = executeReply(native, name, input)
					}
					client.replies = append(client.replies, reply)
					runner, _ := NewRunner(client, Config{MaxSteps: 5, CoreTools: []string{"common"}}, registry)
					result, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "test"}}})
					if err != nil {
						t.Fatal(err)
					}
					want := 0
					if kind == "valid" {
						want = 1
					}
					if tool.calls != want {
						t.Fatalf("target calls=%d want=%d steps=%#v", tool.calls, want, result.Steps)
					}
					if want == 0 {
						last := result.Steps[len(result.Steps)-1]
						if !last.Skipped || strings.Contains(last.Error, "PRIVATE-SECRET") {
							t.Fatalf("invalid rejection: %#v", last)
						}
					}
					if native {
						// Rejected and successful execute envelopes must both be replayed unchanged.
						messages := client.requests[len(client.requests)-1].Messages
						found := false
						for i, m := range messages {
							if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 && reflect.DeepEqual(m.ToolCalls, reply.ToolCalls) {
								found = true
								if i+1 >= len(messages) || messages[i+1].ToolCallID != reply.ToolCalls[0].ID || messages[i+1].ToolName != reply.ToolCalls[0].Name {
									t.Fatal("unpaired native call")
								}
							}
						}
						if !found {
							t.Fatal("original native envelope missing")
						}
					}
				})
			}
		}
	}
}

func TestDeferredLoadedStateResetsEachRun(t *testing.T) {
	tool := &countingTool{name: "rare"}
	client := &dispatchTestClient{replies: []*llm.GenerateResponse{loadReply(true, "rare"), executeReply(true, "rare", map[string]any{})}}
	runner, _ := NewRunner(client, Config{MaxSteps: 4, CoreTools: []string{"common"}}, NewToolRegistry(&countingTool{name: "common"}, tool))
	req := Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "test"}}}
	if _, err := runner.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	client.replies = []*llm.GenerateResponse{executeReply(true, "rare", map[string]any{})}
	if _, err := runner.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if tool.calls != 1 {
		t.Fatalf("calls=%d", tool.calls)
	}
}

func TestDeferredContractBoundsAreAtomicAndNeverTruncated(t *testing.T) {
	tool := &longDescriptionTestTool{description: strings.Repeat("完整描述", 3000)}
	registry := NewToolRegistry(&countingTool{name: "common"}, tool)
	loader := newDeferredToolLoader(registry, []string{"common"})
	if _, err := loader.Run(context.Background(), map[string]any{"names": []string{tool.Name(), "missing"}}); err == nil || len(loader.loaded) != 0 {
		t.Fatal("partial load committed")
	}
	client := &dispatchTestClient{replies: []*llm.GenerateResponse{loadReply(true, tool.Name())}}
	runner, _ := NewRunner(client, Config{CoreTools: []string{"common"}, MaxToolOutputChars: 100}, registry)
	result, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if json.Unmarshal([]byte(result.Steps[0].Output), &output) != nil || !strings.Contains(result.Steps[0].Output, tool.description) {
		t.Fatal("contract truncated")
	}
	tool.description = strings.Repeat("x", maxLoadedContractChars)
	if _, err := loader.Run(context.Background(), map[string]any{"names": []string{tool.Name()}}); err == nil || len(loader.loaded) != 0 {
		t.Fatal("oversized contract committed")
	}
	if _, err := loader.Run(context.Background(), map[string]any{"names": []string{"common", "common", "common", "common", "common", "common", "common", "common", "common"}}); err == nil {
		t.Fatal("load count not bounded")
	}
}

func TestDeferredRunnerPreservesCrossCuttingBehavior(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, kind := range []string{"terminal", "rich", "image", "permission", "duplicate", "timeout", "error", "search"} {
			t.Run(fmt.Sprintf("%v/%s", native, kind), func(t *testing.T) {
				var tool Tool
				switch kind {
				case "terminal":
					tool = &terminalTestTool{}
				case "rich":
					tool = &richResultTestTool{imageURL: "data:image/png;base64,YQ=="}
				case "image":
					tool = &queuedImageTestTool{}
				case "permission":
					tool = &guardedMutationTool{}
				case "duplicate":
					tool = &countingTool{name: "rare"}
				case "timeout":
					tool = &blockingTool{}
				case "error":
					tool = &errorTestTool{message: "upstream failed"}
				case "search":
					tool = &countingWebSearchTool{}
				}
				client := &dispatchTestClient{replies: []*llm.GenerateResponse{loadReply(native, tool.Name()), executeReply(native, tool.Name(), map[string]any{})}}
				if kind == "duplicate" {
					client.replies = append(client.replies, executeReply(native, tool.Name(), map[string]any{}))
				}
				if kind == "image" {
					client.replies = append(client.replies, &llm.GenerateResponse{Text: `{"action":"final","content":"done"}`}, &llm.GenerateResponse{Text: `{"action":"final","task_state":"pending","content":"queued"}`})
				}
				if kind == "search" {
					for i := 0; i < maxWebSearchCallsPerAgentRun+1; i++ {
						client.replies = append(client.replies, executeReply(native, tool.Name(), map[string]any{"query": fmt.Sprint(i)}))
					}
				}
				var events []RunEvent
				runner, _ := NewRunner(client, Config{CoreTools: []string{"common"}, MaxSteps: 10, ToolTimeoutMS: 1}, NewToolRegistry(&countingTool{name: "common"}, tool))
				result, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "test"}}, Observer: func(_ context.Context, e RunEvent) { events = append(events, e) }})
				if err != nil {
					t.Fatal(err)
				}
				if result.Steps[1].Tool != tool.Name() {
					t.Fatal("step lost real target name")
				}
				switch kind {
				case "terminal":
					if result.FinishReason != "terminal_tool" {
						t.Fatalf("%#v", result)
					}
				case "rich":
					found := false
					for _, m := range client.requests[len(client.requests)-1].Messages {
						for _, p := range m.Parts {
							if p.Type == llm.ContentPartImageURL {
								found = true
							}
						}
					}
					if !found {
						t.Fatal("image lost")
					}
				case "image":
					if result.Text != "queued" {
						t.Fatalf("pending guard lost: %#v", result)
					}
				case "permission":
					if !result.Steps[1].Skipped || tool.(*guardedMutationTool).calls != 0 {
						t.Fatal("permission bypassed")
					}
				case "duplicate":
					if tool.(*countingTool).calls != 1 || !result.Steps[2].Skipped {
						t.Fatal("duplicate guard bypassed")
					}
				case "timeout", "error":
					if result.Steps[1].Error == "" {
						t.Fatal("error lost")
					}
				case "search":
					if tool.(*countingWebSearchTool).calls != maxWebSearchCallsPerAgentRun {
						t.Fatal("search cap bypassed")
					}
				}
				for _, e := range events {
					if e.Tool == ToolsExecuteToolName {
						t.Fatal("observer received envelope name")
					}
				}
			})
		}
	}
}

func TestDeferredParallelCallsPreserveOriginalBatch(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			first, second := &countingTool{name: "first"}, &countingTool{name: "second"}
			batch := executeReply(true, "first", map[string]any{})
			other := executeReply(true, "second", map[string]any{}).ToolCalls[0]
			other.ID = "second-id"
			batch.ToolCalls = append(batch.ToolCalls, other)
			client := &dispatchTestClient{}
			if !reject {
				client.replies = append(client.replies, loadReply(true, "first", "second"))
			}
			client.replies = append(client.replies, batch)
			runner, _ := NewRunner(client, Config{CoreTools: []string{"common"}}, NewToolRegistry(&countingTool{name: "common"}, first, second))
			if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "test"}}}); err != nil {
				t.Fatal(err)
			}
			want := 1
			if reject {
				want = 0
			}
			if first.calls != want || second.calls != 0 {
				t.Fatal("parallel execution rule broken")
			}
			messages := client.requests[len(client.requests)-1].Messages
			found := false
			for i, m := range messages {
				if reflect.DeepEqual(m.ToolCalls, batch.ToolCalls) {
					found = true
					for j, call := range batch.ToolCalls {
						result := messages[i+1+j]
						if result.ToolCallID != call.ID || result.ToolName != call.Name || (j == 1 && !result.ToolError) {
							t.Fatal("batch pairing lost")
						}
					}
				}
			}
			if !found {
				t.Fatal("original batch lost")
			}
		})
	}
}

func TestDeferredProtocolRepairLimitDoesNotExecuteTargets(t *testing.T) {
	tool := &countingTool{name: "rare"}
	client := &dispatchTestClient{replies: []*llm.GenerateResponse{executeReply(true, "rare", map[string]any{}), executeReply(true, "rare", map[string]any{})}}
	runner, _ := NewRunner(client, Config{CoreTools: []string{"common"}, ProtocolRepairLimit: 2}, NewToolRegistry(&countingTool{name: "common"}, tool))
	result, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 0 || result.FinishReason != "protocol_repair_exhausted" || len(result.Steps) != 2 {
		t.Fatalf("%#v", result)
	}
}

func TestDeferredNewCapabilityStillRequiresLoad(t *testing.T) {
	registry := NewToolRegistry(&countingTool{name: "common"})
	loader := newDeferredToolLoader(registry, []string{"common"})
	if loader == nil {
		t.Fatal("dispatcher disabled for future capabilities")
	}
	rare := &countingTool{name: "new"}
	registry.Register(rare)
	if _, err := loader.dispatch(llmAction{Action: "tool", Tool: rare.Name(), Input: map[string]any{}}); err == nil {
		t.Fatal("new deferred tool bypassed dispatcher")
	}
	if !strings.Contains(loader.catalog(), "- new:") {
		t.Fatal("new capability missing from catalog")
	}
}

func TestDeferredTargetCannotMutateProviderEnvelope(t *testing.T) {
	loader := newDeferredToolLoader(NewToolRegistry(&countingTool{name: "rare"}), []string{"common"})
	if _, err := loader.Run(context.Background(), map[string]any{"names": []string{"rare"}}); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"nested": []any{map[string]any{"user": "original"}}}
	action, err := loader.dispatch(llmAction{Action: "tool", Tool: ToolsExecuteToolName, Input: map[string]any{"name": "rare", "input": input}})
	if err != nil {
		t.Fatal(err)
	}
	action.Input["added"] = "local"
	action.Input["nested"].([]any)[0].(map[string]any)["user"] = "mutated"
	if input["added"] != nil || input["nested"].([]any)[0].(map[string]any)["user"] != "original" {
		t.Fatal("provider envelope mutated")
	}
}
