// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestRepeatedRunsKeepStablePrefix(t *testing.T) {
	persona := llm.Message{Role: llm.RoleSystem, Content: "固定 caller persona", Priority: llm.MessagePrioritySystem}
	current := llm.Message{Role: llm.RoleUser, Content: "当前输入", Priority: llm.MessagePriorityCurrent}
	for _, tc := range []struct {
		name     string
		messages []llm.Message
		stable   int
	}{
		{"caller_breakpoint", []llm.Message{persona,
			{Role: llm.RoleUser, Content: "历史问题"},
			{Role: llm.RoleAssistant, Content: "历史回复", CacheBreakpoint: true},
			{Role: llm.RoleUser, Content: "本轮身份资料", Priority: llm.MessagePrioritySystem}, current}, 3},
		{"inferred_breakpoint", []llm.Message{persona,
			{Role: llm.RoleUser, Content: "历史问题"},
			{Role: llm.RoleAssistant, Content: "历史回复"}, current}, 3},
		{"no_history", []llm.Message{persona, current}, 1},
		{"current_only", []llm.Message{current}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &scriptedClient{}
			runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), CoreTools: []string{"common"}},
				NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "alpha"}, &countingTool{name: "beta"}))
			if err != nil {
				t.Fatal(err)
			}
			original := append([]llm.Message(nil), tc.messages...)
			var stableBytes, schemaBytes []byte
			for repeat := 0; repeat < 3; repeat++ {
				if _, err := runner.Run(t.Context(), Request{Messages: tc.messages}); err != nil {
					t.Fatal(err)
				}
				request := client.requests[len(client.requests)-1]
				messages := request.Messages
				prefix, err := json.Marshal(messages[:tc.stable+1])
				if err != nil {
					t.Fatal(err)
				}
				schema, err := json.Marshal(request.Tools)
				if err != nil {
					t.Fatal(err)
				}
				if stableBytes == nil {
					stableBytes, schemaBytes = prefix, schema
				} else if string(prefix) != string(stableBytes) || string(schema) != string(schemaBytes) {
					t.Fatalf("repeat=%d changed Agent/persona/history prefix or provider schema", repeat)
				}
				if tc.stable > 1 && !messages[tc.stable].CacheBreakpoint {
					t.Fatal("stable history breakpoint lost")
				}
				if !strings.Contains(messages[0].Content, "- alpha:") || !strings.Contains(messages[0].Content, "- beta:") {
					t.Fatal("fixed deferred catalog lost")
				}
				if messages[len(messages)-1].Content != current.Content || !messages[len(messages)-1].CacheBreakpoint {
					t.Fatal("current input must remain last with rolling cache breakpoint")
				}
				if !reflect.DeepEqual(tc.messages, original) {
					t.Fatal("Run mutated caller messages")
				}
			}
		})
	}
}

func TestDiscoveredContractsRespectCurrentAuthorization(t *testing.T) {
	for _, direct := range []bool{false, true} {
		allowed, revoked := &countingTool{name: "alpha"}, &countingTool{name: "beta"}
		registry := NewToolRegistry(&countingTool{name: "common"}, allowed, revoked)
		registry.Retain(map[string]bool{"common": true, "alpha": true})
		reply := func(name string) *llm.GenerateResponse {
			if direct {
				return dispatchReply(true, name, map[string]any{})
			}
			return executeReply(true, name, map[string]any{})
		}
		client := &dispatchTestClient{replies: []*llm.GenerateResponse{loadReply(true, "alpha"), reply("beta"), reply("alpha")}}
		runner, err := NewRunner(client, Config{CoreTools: []string{"common"}, MaxSteps: 4}, registry)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = runner.Run(t.Context(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "调用工具"}}}); err != nil {
			t.Fatal(err)
		}
		if revoked.calls != 0 || allowed.calls != 1 {
			t.Fatalf("authority changed: allowed=%d revoked=%d", allowed.calls, revoked.calls)
		}
		denied := false
		for _, m := range client.requests[2].Messages {
			denied = denied || strings.Contains(m.Content, "没有权限")
		}
		if !denied {
			t.Fatal("permission feedback missing")
		}
	}
}

func TestMidRunToolsLoadKeepsContractsInToolResult(t *testing.T) {
	client := &dispatchTestClient{replies: []*llm.GenerateResponse{
		loadReply(true, "beta"), executeReply(true, "beta", map[string]any{}),
	}}
	beta := &countingTool{name: "beta"}
	runner, err := NewRunner(client, Config{CoreTools: []string{"common"}, MaxSteps: 4},
		NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "alpha"}, beta))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "固定 persona"},
			{Role: llm.RoleUser, Content: "历史问题"},
			{Role: llm.RoleAssistant, Content: "历史回复", CacheBreakpoint: true},
			{Role: llm.RoleUser, Content: "加载并执行 beta"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if beta.calls != 1 || len(client.requests) != 3 {
		t.Fatalf("mid-run loading changed: calls=%d turns=%d", beta.calls, len(client.requests))
	}
	first := client.requests[0]
	for _, request := range client.requests[1:] {
		if !reflect.DeepEqual(first.Messages[:4], request.Messages[:4]) || !reflect.DeepEqual(first.Tools, request.Tools) {
			t.Fatal("tools_load changed stable prefix or native schemas")
		}
		found := false
		for _, message := range request.Messages {
			if strings.Contains(message.Content, `"name":"beta"`) && strings.Contains(message.Content, `"inputSchema":`) {
				if message.Role != llm.RoleTool || message.ToolName != ToolsLoadToolName {
					t.Fatal("new contract must arrive only in tools_load result, not rewrite the restored tail")
				}
				found = true
			}
		}
		if !found {
			t.Fatal("tools_load result missing complete beta contract")
		}
	}
}

// Skill 的开关按机器人、按群分档，装一个卸一个也会改这份清单。它进系统提示词就意味着
// 换个群、装个 skill 就把整条 prompt 的前缀缓存作废，和上面那一段是同一个坑。
//
// 判据：系统提示词对当前有哪些 skill 完全不敏感。
func TestSkillsCatalogDoesNotEnterStablePrompt(t *testing.T) {
	registry := NewToolRegistry(&SkillsReadTool{}, &countingTool{name: "agent_finalize"})
	runner, err := NewRunner(&scriptedClient{}, Config{WorkDir: t.TempDir()}, registry)
	if err != nil {
		t.Fatal(err)
	}

	empty := runner.systemPrompt()
	registry.SetSkills([]SkillMetadata{
		{Name: "demo-skill", Description: "Use demo.", Path: "/tmp/demo/SKILL.md"},
	})
	loaded := runner.systemPrompt()
	if empty != loaded {
		t.Fatalf("装上 skill 改动了系统提示词，前缀缓存会作废\n之前: %q\n之后: %q", empty, loaded)
	}
	if strings.Contains(loaded, "demo-skill") || strings.Contains(loaded, "/tmp/demo/SKILL.md") {
		t.Fatalf("系统提示词里出现了 skill 名称或路径:\n%s", loaded)
	}
	if catalog := RenderSkillsCatalog(registry.Skills(), 8000); !strings.Contains(catalog, "demo-skill") {
		t.Fatalf("目录没渲染出 skill: %s", catalog)
	}
}
