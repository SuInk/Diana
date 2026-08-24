// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func TestRuntimeModelToolReadsActualProfileSelection(t *testing.T) {
	failover, err := newProfileFailoverLLMProvider([]llm.Profile{
		{ID: "first", Name: "主力配置", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIFormat: llm.APIFormatResponses, Model: "gpt-5.3"}},
		{ID: "second", Name: "备用配置", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderAnthropic, Model: "claude-sonnet"}},
	}, func(llm.ProviderConfig) (LLMProvider, error) { return &capturingLLMProvider{reply: "ok"}, nil }, false, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	failover.current = 1
	provider := &runtimeAgentLLMProvider{providers: map[string]LLMProvider{llm.GroupChat: failover}, lastGroup: llm.GroupChat}
	result, err := newDianaRuntimeModelTool(provider).Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"provider":"备用配置"`, `"model":"claude-sonnet"`, `"group":"default"`} {
		if !strings.Contains(result, want) {
			t.Fatalf("result %q does not contain %q", result, want)
		}
	}
}

func TestRuntimeModelToolIsSemanticToolWithoutPromptMatching(t *testing.T) {
	tool := newDianaRuntimeModelTool(nil)
	if tool.Name() != dianaRuntimeModelToolName || !strings.Contains(tool.Description(), "用户询问") {
		t.Fatalf("unexpected tool metadata: %q %q", tool.Name(), tool.Description())
	}
}

// 模型对「我是谁」有很强的先验，被问到就顺口答「我是 ChatGPT」。工具存在还不够，
// 提示词得明说必须去查——而且要和「改配置」那个工具区分开。
func TestSystemPromptInjectsRuntimeModelRule(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	registry := agent.NewToolRegistry(newDianaRuntimeModelTool(nil))
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(
		MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1"},
		nil, false, RelationshipPolicy{}, true, registry,
	)
	if !strings.Contains(prompt, promptToolRuntimeModel) {
		t.Fatalf("prompt missing the runtime model rule: %s", prompt)
	}
	// 这条规则对普通成员也注入，所以不能提 owner 专属的配置工具：那等于告诉模型
	// 有个它调不动的工具，反而会去试。
	if strings.Contains(promptToolRuntimeModel, "diana.llm_config") {
		t.Fatal("runtime model rule must not name the owner-only config tool")
	}
	for _, want := range []string{"模型 ID", "不得凭训练记忆", "只读不改"} {
		if !strings.Contains(promptToolRuntimeModel, want) {
			t.Fatalf("runtime model rule is missing %q", want)
		}
	}
	// 没注册这个工具的场景里一个字都不该出现。
	bare := runtime.systemPromptWithRelationshipAndAgentTools(
		MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1"},
		nil, false, RelationshipPolicy{}, true, agent.NewToolRegistry(),
	)
	if strings.Contains(bare, dianaRuntimeModelToolName) {
		t.Fatalf("prompt mentions an unregistered tool: %s", bare)
	}
}

// 换到识图用途时报的是那一档实际用的模型，不是对话模型。
func TestRuntimeModelToolFollowsTheGroupInUse(t *testing.T) {
	build := func(model string) LLMProvider {
		failover, err := newProfileFailoverLLMProvider([]llm.Profile{
			{ID: model, Name: model, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: model}},
		}, func(llm.ProviderConfig) (LLMProvider, error) { return &capturingLLMProvider{reply: "ok"}, nil }, false, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		return failover
	}
	provider := &runtimeAgentLLMProvider{
		providers: map[string]LLMProvider{llm.GroupChat: build("chat-model"), llm.GroupVision: build("vision-model")},
		lastGroup: llm.GroupVision,
	}
	result, err := newDianaRuntimeModelTool(provider).Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, `"model":"vision-model"`) || !strings.Contains(result, `"group":"vision"`) {
		t.Fatalf("result = %q", result)
	}
}
