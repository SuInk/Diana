// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// Agent 每一轮的 ctx 来自 Runner，不带 generateReply 打的用途。以前这些调用全落进
// unlabeled——线上 3 天 2252 次，主回复的成本没法归因，事件详情里也看不到回复模型。
func TestRuntimeAgentLLMProviderLabelsModelCallsWithReplyPurpose(t *testing.T) {
	cases := []struct {
		name    string
		purpose string
		want    string
	}{
		{name: "chat reply", want: PurposeReply},
		{name: "scheduled query keeps its own label", purpose: PurposeScheduledQuery, want: PurposeScheduledQuery},
		{name: "event trigger keeps its own label", purpose: PurposeEventTrigger, want: PurposeEventTrigger},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := &captureAppLogs{}
			provider := &usageCountingProvider{}
			runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			runtime.SetAppLogWriter(logs)
			messageID := fmt.Sprintf("agent-%d", index)
			base := withLLMUsageContext(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", MessageID: messageID})
			// generateReply 的做法：调用方打过标签就沿用，没打才记成主回复。
			client := newRuntimeAgentLLMProvider(runtime, withDefaultLLMUsagePurpose(withLLMUsagePurpose(base, tc.purpose), PurposeReply))
			// Runner 传进来的 ctx 只继承了消息上下文，没有用途。
			if _, err := client.Generate(base, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}}); err != nil {
				t.Fatal(err)
			}
			entries := usageEntriesFor(logs, messageID)
			if len(entries) != 1 || entries[0]["purpose"] != tc.want {
				t.Fatalf("usage entries = %#v, want purpose %q", entries, tc.want)
			}
		})
	}
}

// 单次调用自己带了用途时以它为准，Agent 的默认用途只补空缺。
func TestRuntimeAgentLLMProviderKeepsPerCallPurpose(t *testing.T) {
	logs := &captureAppLogs{}
	provider := &usageCountingProvider{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetAppLogWriter(logs)
	base := withLLMUsageContext(context.Background(), MessageEvent{MessageID: "agent-own"})
	client := newRuntimeAgentLLMProvider(runtime, withLLMUsagePurpose(base, PurposeReply))
	if _, err := client.Generate(withLLMUsagePurpose(base, PurposeSubtask), llm.GenerateRequest{}); err != nil {
		t.Fatal(err)
	}
	if entries := usageEntriesFor(logs, "agent-own"); len(entries) != 1 || entries[0]["purpose"] != PurposeSubtask {
		t.Fatalf("usage entries = %#v", entries)
	}
}
