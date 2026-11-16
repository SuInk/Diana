// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type describedTool struct {
	name, description string
}

func (t *describedTool) Name() string        { return t.name }
func (t *describedTool) Description() string { return t.description }
func (*describedTool) Run(context.Context, map[string]any) (string, error) {
	return "ok", nil
}

func TestToolSearchQueryFindsByKeyword(t *testing.T) {
	registry := NewToolRegistry(
		&describedTool{"platform", "读群资料、核验成员，以及禁言、踢人等群管操作。"},
		&describedTool{"llm_config", "切换 Diana 模型，不动供应商地址和密钥。"},
		&describedTool{"tts", "把文字合成语音发出去。"},
	)
	loader := newDeferredToolLoader(registry, []string{finalizeToolName}, nil)
	output, err := loader.Run(context.Background(), map[string]any{"query": "把他禁言"})
	if err != nil || !strings.Contains(output, `"name":"platform"`) || strings.Contains(output, `"name":"tts"`) {
		t.Fatalf("query output=%s err=%v", output, err)
	}
	if _, loaded := loader.loaded["platform"]; !loaded {
		t.Fatal("query hit was not marked loaded")
	}
	if _, err := loader.Run(context.Background(), map[string]any{"query": "完全无关的东西"}); err == nil {
		t.Fatal("unmatched query should report no tools")
	}
	if _, err := loader.Run(context.Background(), map[string]any{}); err == nil {
		t.Fatal("empty input accepted")
	}
}

func TestLegacyToolsLoadNameStillDispatches(t *testing.T) {
	registry := NewToolRegistry(&describedTool{"tts", "语音"})
	loader := newDeferredToolLoader(registry, []string{finalizeToolName}, nil)
	action, err := loader.dispatch(llmAction{Action: "tool", Tool: LegacyToolsLoadName, Input: map[string]any{"names": []any{"tts"}}})
	if err != nil || action.Tool != ToolsLoadToolName {
		t.Fatalf("legacy name dispatched to %q err=%v", action.Tool, err)
	}
	for _, definition := range loader.filter(registry.Definitions()) {
		if definition.Name == LegacyToolsLoadName {
			t.Fatal("legacy name must not be advertised")
		}
	}
}

// 前缀按工具全集渲染：主人和群友交替说话时，工具定义、Agent system、人设和已加载
// 契约逐字相同；谁能用哪些、预算多少只在每轮一行的权限说明里。
func TestSpeakerToolsShareOnePrefix(t *testing.T) {
	ownerTools := func() []Tool {
		return []Tool{&describedTool{"tts", "语音"}, &describedTool{"say", "中途说一句"}, &describedTool{"coding", "把编码工作交给外部 CLI，仅主人"}, &describedTool{"run_command", "执行白名单命令"}, &describedTool{"write_file", "写文件"}}
	}
	head := NewToolRegistry(ownerTools()...)
	persona := llm.Message{Role: llm.RoleSystem, Content: "人设", Priority: llm.MessagePrioritySystem}
	history := llm.Message{Role: llm.RoleUser, Content: "历史", CacheBreakpoint: true}
	current := llm.Message{Role: llm.RoleUser, Content: "当前", Priority: llm.MessagePriorityCurrent}
	capture := func(steps int, tools ...Tool) llm.GenerateRequest {
		client := &scriptedClient{}
		runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), CoreTools: []string{finalizeToolName}, HeadRegistry: head, MaxSteps: steps}, NewToolRegistry(tools...))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(t.Context(), Request{Messages: []llm.Message{persona, history, current}, LoadedTools: []string{"tts", "coding"}}); err != nil {
			t.Fatal(err)
		}
		return client.requests[0]
	}
	member := capture(20, &describedTool{"tts", "语音"}, &describedTool{"say", "中途说一句"})
	owner := capture(32, ownerTools()...)
	prefix := func(req llm.GenerateRequest) string {
		var builder strings.Builder
		for _, definition := range req.Tools {
			builder.WriteString(definition.Name + "|" + definition.Description + "\n")
		}
		for _, message := range req.Messages {
			builder.WriteString(message.Content + "\n")
			if message.CacheBreakpoint {
				break
			}
		}
		return builder.String()
	}
	if prefix(member) != prefix(owner) {
		t.Fatalf("owner and member prefixes differ:\n%s\n---\n%s", prefix(member), prefix(owner))
	}
	if !strings.Contains(prefix(owner), "- coding(") || !strings.Contains(prefix(owner), "编写或修复代码时") || strings.Contains(prefix(owner), "最多可调用") {
		t.Fatalf("the prefix must list every tool and no per-speaker budget:\n%s", prefix(owner))
	}
	note := func(req llm.GenerateRequest) string {
		for _, message := range req.Messages {
			if strings.HasPrefix(message.Content, speakerPermissionNotePrefix) {
				return message.Content
			}
		}
		return ""
	}
	if got := note(owner); !strings.Contains(got, "最多可调用 32 次") || !strings.Contains(got, "都能用") {
		t.Fatalf("owner note = %q", got)
	}
	if got := note(member); !strings.Contains(got, "最多可调用 20 次") || !strings.Contains(got, "coding、run_command、write_file") {
		t.Fatalf("member note = %q", got)
	}
	// 群友点名调用前缀里列着、自己用不了的工具：说没有权限，不说工具不存在。
	loader := newDeferredToolLoader(NewToolRegistry(&describedTool{"tts", "语音"}), []string{finalizeToolName}, nil)
	loader.head = head
	if _, err := loader.dispatch(llmAction{Action: "tool", Tool: ToolsExecuteToolName, Input: map[string]any{"name": "coding", "input": map[string]any{}}}); err == nil || !strings.Contains(err.Error(), "没有权限") {
		t.Fatalf("calling a listed tool without permission: %v", err)
	}
}

// 模型没先 tool_search 就直接点名调用时，参数合法就当场执行，不再多走一步；
// 参数不合法才交回契约。
func TestDeferredToolRunsWithoutSeparateSearch(t *testing.T) {
	registry := NewToolRegistry(&describedTool{"say", "中途说一句"}, &countingTool{name: "strict"})
	loader := newDeferredToolLoader(registry, []string{finalizeToolName}, nil)
	if action, err := loader.dispatch(llmAction{Action: "tool", Tool: "say", Input: map[string]any{}}); err != nil || action.Tool != "say" {
		t.Fatalf("direct call: tool=%q err=%v", action.Tool, err)
	}
	if _, loaded := loader.loaded["say"]; !loaded {
		t.Fatal("direct call was not recorded as loaded")
	}
	if action, err := loader.dispatch(llmAction{Action: "tool", Tool: ToolsExecuteToolName, Input: map[string]any{"name": "strict", "input": map[string]any{}}}); err != nil || action.Tool != "strict" {
		t.Fatalf("tools_execute without search: tool=%q err=%v", action.Tool, err)
	}
	if _, err := loader.dispatch(llmAction{Action: "tool", Tool: "missing", Input: map[string]any{}}); err == nil {
		t.Fatal("unknown tool accepted")
	}
}
