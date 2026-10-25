// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// Uses the deployed model/persona, but isolated history fixtures and no delivery
// tools or production writes. The host check is deliberately task-specific: it
// demonstrates the harness contract, not a universal factuality classifier.
func TestLiveHistoryScopeReplay(t *testing.T) {
	client := liveLLMClient(t)
	cfg := BotConfig{}
	if path := os.Getenv("DIANA_TEST_BOT_CONFIG"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
	}
	cfg.OwnerID = "owner"
	cfg.CrossGroupMemoryEnabled = boolPointer(true)
	cfg = cfg.WithDefaults()
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetMessageHistoryStore(newSemanticTimelineStore())
	old := "小星活跃等级68.6，总消息2378条，和望月優雪互动最多。"
	event := MessageEvent{ProfileID: cfg.ID, Platform: cfg.Platform, Kind: EventKindPrivate, UserID: "owner", Time: time.Now().Unix(), RawMessage: "就是这个，查 group-1 群的星ほしの，核对之前那些统计有没有依据。"}
	original := chatHistoryTextEvent(event.Time-30, "star", "星ほしの", "original", "今晚刚到家，准备睡觉了。")
	original.ProfileID, original.Platform = cfg.ID, cfg.Platform
	r.remember(original)
	previous := event
	previous.Time, previous.MessageID, previous.RawMessage, previous.Outbound = event.Time-20, "old-answer", old, true
	r.remember(previous)
	registry := agent.NewToolRegistry(newDianaChatHistoryTool(r, event))
	runner, err := agent.NewRunner(client, agent.Config{MaxSteps: 5}, registry)
	if err != nil {
		t.Fatal(err)
	}
	productionHooks := newReplyRunHooks()
	verified := false
	rejected := 0
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	resp, err := runner.Run(ctx, agent.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: r.systemPromptWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicy{Owner: true}, true, registry)},
			{Role: llm.RoleAssistant, Content: old},
			{Role: llm.RoleUser, Content: event.RawMessage},
		},
		Hooks: agent.RunHooks{
			PrepareRequest: productionHooks.PrepareRequest,
			AfterToolCall: func(ctx context.Context, step agent.Step) {
				productionHooks.AfterToolCall(ctx, step)
				if step.Error != "" || step.Skipped {
					return
				}
				var result dianaChatHistoryResult
				if json.Unmarshal([]byte(step.Output), &result) != nil {
					return
				}
				for _, item := range result.Items {
					if item.MessageID == "original" {
						verified = true
					}
				}
			},
			BeforeFinish: func(_ context.Context, candidate agent.FinishCandidate) (agent.FinishDecision, error) {
				if !verified {
					rejected++
					return agent.FinishDecision{Feedback: "尚未读取目标群本人原始记录。请用 chat_history 的 search，group_id=group-1，scope=current，关键词星ほしの。旧回复不支持其中的统计。"}, nil
				}
				return agent.FinishDecision{}, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Fatal("finished without reading the original record")
	}
	t.Logf("model=%s rejected=%d tools=%d answer=%s", resp.Model, rejected, len(resp.Steps), resp.Text)
}

func TestHistorySearchExplicitGroupFromPrivateDoesNotSearchOldAnswers(t *testing.T) {
	r := NewRuntime(BotConfig{OwnerID: "owner", CrossGroupMemoryEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetMessageHistoryStore(newSemanticTimelineStore())
	r.remember(chatHistoryTextEvent(110, "star", "星ほしの", "original", "小星本人发言"))
	r.remember(MessageEvent{Kind: EventKindPrivate, UserID: "owner", Time: 111, MessageID: "old-answer", RawMessage: "小星有2378条消息", Outbound: true})
	tool := newDianaChatHistoryTool(r, MessageEvent{Kind: EventKindPrivate, UserID: "owner", Time: 200})
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "search", "query": "小星", "scope": "current", "group_id": "group-1", "all_time": true})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeHistoryPage(t, raw)
	if len(got.Items) != 1 || got.Items[0].MessageID != "original" || got.GroupID != "group-1" || got.Scope != "current" {
		t.Fatalf("wrong actual scope: %+v", got)
	}
	if !strings.Contains(got.Message, "不是其中人物资料") {
		t.Fatal("missing provenance guidance")
	}
}

func TestHistorySearchExplicitGroupPermissions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		kind    EventKind
		user    string
		member  bool
		allowed bool
	}{
		{"private owner", true, EventKindPrivate, "owner", false, true},
		{"disabled", false, EventKindPrivate, "owner", false, false},
		{"private member", true, EventKindPrivate, "member", true, false},
		{"group member", true, EventKindGroup, "member", true, true},
		{"group stranger", true, EventKindGroup, "member", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &crossGroupMembershipChannel{allowed: map[string]bool{"group-1|member": tc.member}}
			r := NewRuntime(BotConfig{OwnerID: "owner", CrossGroupMemoryEnabled: boolPointer(tc.enabled)}, channel, NewPluginManager(), nil, nil, nil, nil)
			r.SetMessageHistoryStore(newSemanticTimelineStore())
			r.remember(chatHistoryTextEvent(110, "star", "星", "original", "小星"))
			event := MessageEvent{Kind: tc.kind, UserID: tc.user, Time: 200}
			if tc.kind == EventKindGroup {
				event.GroupID = "group-2"
			}
			_, err := newDianaChatHistoryTool(r, event).Run(context.Background(), map[string]any{"operation": "search", "query": "小星", "group_id": "group-1", "all_time": true})
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v error=%v", tc.allowed, err)
			}
		})
	}
}

func TestHistoryRejectsConflictingOrIgnoredGroupScope(t *testing.T) {
	r := NewRuntime(BotConfig{OwnerID: "owner", CrossGroupMemoryEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaChatHistoryTool(r, MessageEvent{Kind: EventKindPrivate, UserID: "owner"})
	for _, input := range []map[string]any{
		{"operation": "search", "query": "小星", "group_id": "group-1", "scope": "all_groups"},
		{"operation": "recent", "group_id": "group-1"},
		{"operation": "search", "scope": "all_groups"},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("silently accepted ambiguous scope: %v", input)
		}
	}
}
