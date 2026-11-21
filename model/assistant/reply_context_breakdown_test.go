// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func replyContextTestRequest(history ...string) llm.GenerateRequest {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "Runner 协议"},
		{Role: llm.RoleSystem, Content: "人设和规则"},
	}
	for _, line := range history {
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "[历史 12:00] " + line})
	}
	messages = append(messages,
		llm.Message{Role: llm.RoleSystem, Content: "【当前消息的对话对象】小明"},
		llm.Message{Role: llm.RoleUser, Content: "现在几点"},
	)
	return llm.GenerateRequest{Messages: messages, Tools: []llm.ToolDefinition{{Name: "send", Parameters: map[string]any{"type": "object"}}}}
}

// 图上的块按请求顺序分三类，各块合计等于整份请求；同一会话下一轮从头相同的部分不算新发。
func TestReplyContextStoreSegmentsAndCountsNewTokens(t *testing.T) {
	var store replyContextStore
	now := time.Now()
	first := replyContextTestRequest("小红: 早")
	store.note("bot\x00group:1", first, llm.Usage{InputTokens: 900, CachedInputTokens: 0}, 64000, now)
	breakdown, ok := store.overview("bot", now)
	if !ok {
		t.Fatal("no breakdown after the first request")
	}
	var names, kinds []string
	for _, segment := range breakdown.Segments {
		names, kinds = append(names, segment.Name), append(kinds, segment.Kind)
	}
	wantNames := []string{replyContextToolsName, "Runner 系统提示", "头部（人设、规则）", replyContextHistoryName, "【当前消息的对话对象】", replyContextCurrentName}
	wantKinds := []string{"prefix", "prefix", "prefix", "history", "changing", "changing"}
	if len(names) != len(wantNames) {
		t.Fatalf("segments = %v", names)
	}
	for index := range wantNames {
		if names[index] != wantNames[index] || kinds[index] != wantKinds[index] {
			t.Fatalf("segment %d = %s/%s, want %s/%s", index, names[index], kinds[index], wantNames[index], wantKinds[index])
		}
	}
	whole := llm.EstimateToolDefinitionsTokens(first.Tools) + estimateMessagesTokens(first.Messages)
	if breakdown.TotalTokens != whole || breakdown.NewTokens != whole {
		t.Fatalf("first round total=%d new=%d, want both %d", breakdown.TotalTokens, breakdown.NewTokens, whole)
	}
	if breakdown.ContextWindow != 64000 || breakdown.InputTokens != 900 {
		t.Fatalf("window=%d input=%d", breakdown.ContextWindow, breakdown.InputTokens)
	}

	// 下一轮：头部和第一条历史不变，新历史、尾部和当前消息是新的。
	second := replyContextTestRequest("小红: 早", "小明: 在吗")
	store.note("bot\x00group:1", second, llm.Usage{}, 64000, now.Add(time.Minute))
	breakdown, _ = store.overview("bot", now.Add(time.Minute))
	for _, segment := range breakdown.Segments {
		switch segment.Kind {
		case "prefix":
			if segment.NewTokens != 0 {
				t.Fatalf("unchanged prefix %s counted as new: %d", segment.Name, segment.NewTokens)
			}
		case "history":
			if segment.NewTokens <= 0 || segment.NewTokens >= segment.Tokens {
				t.Fatalf("history new=%d of %d, want only the appended line", segment.NewTokens, segment.Tokens)
			}
		}
	}
}

// 多个会话平均成一次；别的机器人和 24 小时前的记录不算。
func TestReplyContextOverviewAveragesRecentSessionsOfOneProfile(t *testing.T) {
	var store replyContextStore
	now := time.Now()
	store.note("bot\x00group:1", replyContextTestRequest("a: 1"), llm.Usage{InputTokens: 100}, 64000, now)
	store.note("bot\x00group:2", replyContextTestRequest("a: 1", "b: 2", "c: 3"), llm.Usage{InputTokens: 300}, 32000, now)
	store.note("other\x00group:1", replyContextTestRequest("x: 1"), llm.Usage{InputTokens: 9999}, 8000, now)
	breakdown, ok := store.overview("bot", now)
	if !ok || breakdown.Sessions != 2 {
		t.Fatalf("sessions = %d ok=%v", breakdown.Sessions, ok)
	}
	if breakdown.InputTokens != 200 || breakdown.ContextWindow != 32000 {
		t.Fatalf("input=%d window=%d, want the average and the smallest window", breakdown.InputTokens, breakdown.ContextWindow)
	}
	if _, ok := store.overview("bot", now.Add(replyContextMaxAge+time.Minute)); ok {
		t.Fatal("records older than 24 hours still shown")
	}
}

// 记录器只记第一个成功的请求：后面的工具步骤不覆盖。
func TestReplyRequestRecorderKeepsFirstSuccessfulRequest(t *testing.T) {
	ctx, recorder := withReplyRequestRecorder(context.Background())
	provider := &replyRequestRecordProvider{provider: replyContextEchoProvider{}}
	for _, text := range []string{"第一步", "第二步"} {
		if _, err := provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: text}}}); err != nil {
			t.Fatal(err)
		}
	}
	request, _, ok := recorder.first()
	if !ok || request.Messages[0].Content != "第一步" {
		t.Fatalf("recorded %#v ok=%v", request.Messages, ok)
	}
}

type replyContextEchoProvider struct{}

func (replyContextEchoProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return &llm.GenerateResponse{Text: "好"}, nil
}
