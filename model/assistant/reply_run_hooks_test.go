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

// 【输出前检查】每步贴在请求最后（agent.Request.TailReminder），不重复头部已有的括号用法；
// 群聊才带篇幅规则。钩子在没执行过工具时什么都不加，请求原样发出。
func TestReplyTailReminderCarriesChecks(t *testing.T) {
	group := MessageEvent{Kind: EventKindGroup}
	for _, rule := range []string{replyUnclosedBracketRule, "只调整括号里的补充，保留句末语气符号。"} {
		cfg := BotConfig{PromptOverrides: PromptOverrides{promptReplyUnclosedBracketSpec.Key: rule}}
		check := replyTailReminder(cfg, group)
		if !strings.HasPrefix(check, "【输出前检查】\n") || strings.Contains(check, rule) {
			t.Fatalf("output check = %q", check)
		}
		for _, want := range []string{replyVerifyRule, promptNoteGroupBrevitySpec.Default, promptReplyNoEchoSpec.Default, promptReplyUnknownReferenceSpec.Default, promptReplyClaimedLookupSpec.Default, promptReplyArithmeticSpec.Default} {
			if strings.Count(check, want) != 1 {
				t.Fatalf("output check must carry %.20q once: %q", want, check)
			}
		}
		req := llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "old context", CacheBreakpoint: true},
			{Role: llm.RoleUser, Content: "current"},
		}}
		got, err := newReplyRunHooks().PrepareRequest(context.Background(), req)
		if err != nil || len(got.Messages) != 2 || got.Messages[1].Content != "current" {
			t.Fatalf("hooks must leave a request without tool calls untouched: %+v err=%v", got, err)
		}
	}
	private := replyTailReminder(BotConfig{}, MessageEvent{Kind: EventKindPrivate})
	if strings.Contains(private, promptNoteGroupBrevitySpec.Default) || !strings.Contains(private, promptReplyNoEchoSpec.Default) {
		t.Fatalf("private output check = %q", private)
	}
}

func TestReplyRunHooksUseActualExecutionAndKeepPrivateInputsOut(t *testing.T) {
	hooks := newReplyRunHooks()
	ctx := context.Background()
	request := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "stable", CacheBreakpoint: true}, {Role: llm.RoleUser, Content: "current"}}}
	// 还没执行过工具时不加回执：第一步插在当前消息前面的话，第二步当前消息起全要重发。
	first, err := hooks.PrepareRequest(ctx, request)
	if err != nil || len(first.Messages) != 2 || first.Messages[1].Content != "current" {
		t.Fatalf("request=%+v err=%v", first, err)
	}
	hooks.AfterToolCall(ctx, agent.Step{Tool: "chat_history", Input: map[string]any{"scope": "current", "group_id": "group-1", "token": "secret", "query": "private question"}, Error: "denied", Skipped: true})
	second, err := hooks.PrepareRequest(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	note := second.Messages[1].Content
	if !strings.Contains(note, `"status":"not_executed"`) || !strings.Contains(note, `"group_id":"group-1"`) || strings.Contains(note, "secret") || strings.Contains(note, "private question") {
		t.Fatalf("bad ledger: %s", note)
	}
	if len(request.Messages) != 2 || request.Messages[1].Content != "current" || second.Messages[len(second.Messages)-1].Content != "current" || !second.Messages[0].CacheBreakpoint {
		t.Fatal("changed canonical history or cache prefix")
	}
	for i := 0; i < 20; i++ {
		hooks.AfterToolCall(ctx, agent.Step{Tool: "lookup"})
	}
	bounded, _ := hooks.PrepareRequest(ctx, request)
	// 附注发出去就留在对话里，下一次只写新执行的：这 20 条里列最后 12 条，前面的已经写过。
	if note := bounded.Messages[1].Content; !strings.Contains(note, `"total":21`) || !strings.Contains(note, `"omitted":8`) || strings.Contains(note, "chat_history") {
		t.Fatalf("execution note must list only the new, bounded records: %s", note)
	}
	if again, _ := hooks.PrepareRequest(ctx, request); len(again.Messages) != 2 {
		t.Fatal("nothing new ran: no second note")
	}
	hooks.AfterToolCall(ctx, agent.Step{Tool: "lookup"})
	batch := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "one", Name: "lookup"}, {ID: "two", Name: "lookup"}}},
		{Role: llm.RoleTool, ToolCallID: "one"},
		{Role: llm.RoleTool, ToolCallID: "two"},
	}}
	prepared, _ := hooks.PrepareRequest(ctx, batch)
	if prepared.Messages[1].ToolCallID != "one" || prepared.Messages[2].ToolCallID != "two" || prepared.Messages[3].Role != llm.RoleSystem {
		t.Fatal("execution context split a native tool batch")
	}
}
