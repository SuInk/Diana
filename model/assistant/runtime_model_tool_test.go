// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

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
