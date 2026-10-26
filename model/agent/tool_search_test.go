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
	loader := newDeferredToolLoader(registry, []string{finalizeToolName}, nil, nil)
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
	loader := newDeferredToolLoader(registry, []string{finalizeToolName}, nil, nil)
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

// 公共工具进系统提示词，发言者专属工具的目录、规则、契约和预算都在历史之后：
// 主人和群友交替说话时，工具定义、Agent system 和人设逐字相同。
func TestSpeakerToolsStayOutOfSharedPrefix(t *testing.T) {
	shared := map[string]bool{"tts": true, "say": true}
	persona := llm.Message{Role: llm.RoleSystem, Content: "人设", Priority: llm.MessagePrioritySystem}
	history := llm.Message{Role: llm.RoleUser, Content: "历史", CacheBreakpoint: true}
	current := llm.Message{Role: llm.RoleUser, Content: "当前", Priority: llm.MessagePriorityCurrent}
	capture := func(steps int, tools ...Tool) llm.GenerateRequest {
		client := &scriptedClient{}
		runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), CoreTools: []string{finalizeToolName}, SharedTools: shared, MaxSteps: steps}, NewToolRegistry(tools...))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(t.Context(), Request{Messages: []llm.Message{persona, history, current}, LoadedTools: []string{"tts", "coding"}}); err != nil {
			t.Fatal(err)
		}
		return client.requests[0]
	}
	member := capture(20, &describedTool{"tts", "语音"}, &describedTool{"say", "中途说一句"})
	owner := capture(32, &describedTool{"tts", "语音"}, &describedTool{"say", "中途说一句"}, &describedTool{"coding", "把编码工作交给外部 CLI，仅主人"}, &describedTool{"run_command", "执行白名单命令"}, &describedTool{"write_file", "写文件"})
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
	if strings.Contains(prefix(owner), "coding") || strings.Contains(prefix(owner), "最多可调用 32") {
		t.Fatal("speaker-only catalog or budget leaked into the shared prefix")
	}
	var tail string
	for _, message := range owner.Messages {
		if strings.Contains(message.Content, "【本轮工具预算】") {
			tail = message.Content
		}
	}
	for _, want := range []string{"最多可调用 32 次", "- coding:", "编写或修复代码时", "当前发言者已经加载"} {
		if !strings.Contains(tail, want) {
			t.Fatalf("owner tail misses %q:\n%s", want, tail)
		}
	}
}

// 模型没先 tool_search 就直接点名调用时，参数合法就当场执行，不再多走一步；
// 参数不合法才交回契约。
func TestDeferredToolRunsWithoutSeparateSearch(t *testing.T) {
	registry := NewToolRegistry(&describedTool{"say", "中途说一句"}, &countingTool{name: "strict"})
	loader := newDeferredToolLoader(registry, []string{finalizeToolName}, nil, nil)
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
