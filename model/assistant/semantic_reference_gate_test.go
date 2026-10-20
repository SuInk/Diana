// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

// mediaGateRuntime 带一个持久时间线：只有存储层留着原文，才可能出现「媒体还在
// 库里、但已经掉出模型看得到的窗口」这个本方案要处理的状态。
func mediaGateRuntime(t *testing.T, events ...MessageEvent) *Runtime {
	t.Helper()
	store := newSemanticTimelineStore()
	runtime := NewRuntime(BotConfig{RecentContextLimit: 2}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	for _, event := range events {
		if err := store.AppendMessageEvent(context.Background(), sessionKey(event), event); err != nil {
			t.Fatalf("seed timeline: %v", err)
		}
		runtime.remember(event)
	}
	return runtime
}

func gateTextEvent(id string, at int64, text string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, Time: at, GroupID: "group-1", UserID: "u1", SenderName: "小明",
		MessageID: id, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

func gateImageEvent(id string, at int64) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, Time: at, GroupID: "group-1", UserID: "u1", SenderName: "小明",
		MessageID: id, RawMessage: "[图片]",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file": id + ".png"}}},
	}
}

func gateCurrentEvent() MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, Time: 10_000, GroupID: "group-1", UserID: "u2", SenderName: "当前用户",
		MessageID: "now", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "哈哈哈"}}},
	}
}

func TestSemanticReferenceSkippedForTextOnlySessions(t *testing.T) {
	runtime := mediaGateRuntime(t,
		gateTextEvent("m1", 1, "第一条"),
		gateTextEvent("m2", 2, "第二条"),
		gateTextEvent("m3", 3, "第三条"),
	)
	ctx := context.Background()
	event := gateCurrentEvent()

	// 纯文字会话没有任何可解析的媒体，两种模式都不该付一次前置调用。
	// 非 agent 模式以前是短路成「每条消息都跑」，这正是要修掉的固定开销。
	for _, agentEnabled := range []bool{true, false} {
		cfg := BotConfig{AgentEnabled: agentEnabled, RecentContextLimit: 2}.WithDefaults()
		if runtime.shouldResolveSemanticReference(ctx, cfg, event, false) {
			t.Fatalf("router fired on a text-only session (agent=%v)", agentEnabled)
		}
	}
}

func TestSemanticReferenceStillRunsWithoutAgentToolAccess(t *testing.T) {
	runtime := mediaGateRuntime(t,
		gateImageEvent("img-old", 1),
		gateTextEvent("m2", 2, "第二条"),
		gateTextEvent("m3", 3, "第三条"),
	)
	ctx := context.Background()
	event := gateCurrentEvent()

	// 非 agent 模式下 historyPromptTextAt 会整条丢掉纯图片历史，模型连图存在都不
	// 知道，所以只要历史里有媒体就必须前置解析。
	nonAgent := BotConfig{AgentEnabled: false, RecentContextLimit: 2}.WithDefaults()
	if !runtime.shouldResolveSemanticReference(ctx, nonAgent, event, false) {
		t.Fatal("non-agent mode skipped resolution while history still holds media")
	}

	// agent 模式但工具被关系等级挡掉时同样没有取图手段，仍要回退到路由器。
	agent := BotConfig{AgentEnabled: true, RecentContextLimit: 2}.WithDefaults()
	if !runtime.shouldResolveSemanticReference(ctx, agent, event, false) {
		t.Fatal("agent without tool access skipped resolution")
	}

	// 能自己取图时按需查历史和媒体，不再付前置调用。
	if runtime.shouldResolveSemanticReference(ctx, agent, event, true) {
		t.Fatal("agent that can fetch media still paid for the router")
	}
}

func TestOldMediaStaysAvailableWithoutAutomaticIndex(t *testing.T) {
	provider := &capturingLLMProvider{reply: "继续聊当前话题"}
	runtime := NewRuntime(BotConfig{BotAccount: "42", AgentEnabled: true, RecentHistoryTokenBudget: 20}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	store := newSemanticTimelineStore()
	runtime.SetMessageHistoryStore(store)
	old := gateImageEvent("old-media-only-on-demand", 1)
	old.SelfID = "42"
	old.Segments[0].Data["url"] = "data:image/png;base64,YQ=="
	latest := gateTextEvent("recent-text", 1000, strings.Repeat("正在讨论的是新的文字话题。", 20))
	latest.SelfID = "42"
	for _, item := range []MessageEvent{old, latest} {
		if err := store.AppendMessageEvent(context.Background(), sessionKey(item), item); err != nil {
			t.Fatal(err)
		}
		runtime.remember(item)
	}
	event := gateCurrentEvent()
	event.SelfID, event.ToMe = "42", true
	if _, err := runtime.replyTo(context.Background(), event, "继续聊当前话题"); err != nil {
		t.Fatal(err)
	}
	request := provider.requestSnapshot()
	for _, unwanted := range []string{"更早的图片与文件索引", old.MessageID, "data:image/png;base64,YQ=="} {
		if requestMessagesContain(request.Messages, unwanted) {
			t.Fatalf("old media automatically injected %q", unwanted)
		}
	}
	tool := newDianaHistoryImagesTool(runtime, event)
	output, err := tool.Run(context.Background(), map[string]any{"message_ids": []any{old.MessageID}})
	if err != nil || len(tool.ToolResultParts(output)) != 1 {
		t.Fatalf("archived media unavailable on demand: output=%s err=%v", output, err)
	}
}

func TestSemanticReferenceCachesNegativeDecisions(t *testing.T) {
	// 判成「没有指代」也是花了一次调用得到的结论；第二次问同一句话不该重新付费。
	provider := &sequenceLLMProvider{replies: []string{
		`{"message_ids":[],"confidence":0.2,"reason":"措辞没有指向任何历史消息"}`,
	}}
	calls := 0
	runtime := NewRuntime(BotConfig{RecentContextLimit: 20}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		calls++
		return provider, nil
	})
	runtime.remember(gateImageEvent("img-1", 1))
	ask := func() MessageEvent {
		return MessageEvent{
			Kind: EventKindGroup, Time: 1000, GroupID: "group-1", UserID: "u2", SenderName: "当前用户",
			MessageID: "q1", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "今天天气不错"}}},
		}
	}

	runtime.enrichSemanticReference(context.Background(), ask(), "今天天气不错")
	if calls != 1 {
		t.Fatalf("first resolution made %d provider calls, want 1", calls)
	}
	runtime.enrichSemanticReference(context.Background(), ask(), "今天天气不错")
	if calls != 1 {
		t.Fatalf("negative decision was not cached: %d provider calls", calls)
	}
}

func TestSemanticReferenceRejectsLowConfidenceSelections(t *testing.T) {
	// 0.6 在旧阈值（0.55）下会被采纳。指代判错的代价是贴错图、模型据此笃定地答错，
	// 所以拿不准时宁可退回「没有指代」。
	provider := &sequenceLLMProvider{replies: []string{
		`{"message_ids":["img-1"],"confidence":0.6,"reason":"可能是这张"}`,
	}}
	runtime := NewRuntime(BotConfig{RecentContextLimit: 20}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.remember(gateImageEvent("img-1", 1))

	event := runtime.enrichSemanticReference(context.Background(), MessageEvent{
		Kind: EventKindGroup, Time: 1000, GroupID: "group-1", UserID: "u2", SenderName: "当前用户",
		MessageID: "q1", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "那个是什么"}}},
	}, "那个是什么")

	if event.Quoted != nil {
		t.Fatalf("low-confidence selection was adopted: %#v", event.Quoted)
	}
}
