// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// forwardTimeoutChannel 让打包发送在真实投递结果未知的情况下超时。
type forwardTimeoutChannel struct {
	*recordingChannel
}

func (c *forwardTimeoutChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if action == "send_group_forward_msg" {
		return nil, context.DeadlineExceeded
	}
	return c.recordingChannel.CallAPI(ctx, action, params)
}

func resolverForwardTestResponse() PluginResponse {
	return PluginResponse{
		Handled: true,
		Reply:   "某站图集 · 标题",
		Forward: true,
		ForwardMessages: []OutgoingMessage{
			{Text: "某站图集 · 标题"},
			{ImageURLs: []string{"https://example.com/1.jpg"}},
			{ImageURLs: []string{"https://example.com/2.jpg"}},
		},
		ImageURLs: []string{"https://example.com/1.jpg", "https://example.com/2.jpg"},
	}
}

func resolverForwardChannel() *recordingChannel {
	return &recordingChannel{apiResponses: map[string]map[string]any{
		"send_private_msg":       {"message_id": 90001},
		"send_group_forward_msg": {"message_id": 90100},
		"send_group_msg":         {"message_id": 90200},
	}}
}

// 入站事件因为后续步骤失败整条重跑时，已经送达的合并转发不能再发第二遍——
// 线上表现就是同一个图集被连发两份。
func TestResolverForwardIsNotResentOnInboundReplay(t *testing.T) {
	channel := resolverForwardChannel()
	store := &memoryInboundEventStore{}
	runtime := NewRuntime(BotConfig{BotQQ: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetInboundEventStore(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "m1", SelfID: "42"}
	resp := resolverForwardTestResponse()
	cfg := runtime.effectiveConfigForEvent(event)
	ctx := withOutboundTurn(context.Background(), "turn-1")

	if err := runtime.sendForwardPluginResponse(ctx, event, resp, cfg); err != nil {
		t.Fatalf("first send failed: %v", err)
	}
	firstForwardCalls := countRecordingCalls(channel, "send_group_forward_msg")
	if firstForwardCalls == 0 {
		t.Fatal("merged forward was never sent")
	}

	// 模拟整条事件重跑：同一个 turn 重新建立处理上下文（步骤序号从头计数）、
	// 相同内容再走一遍。
	ctx = withOutboundTurn(context.Background(), "turn-1")
	if err := runtime.sendForwardPluginResponse(ctx, event, resp, cfg); err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	if got := countRecordingCalls(channel, "send_group_forward_msg"); got != firstForwardCalls {
		t.Fatalf("replay sent the forward again: %d -> %d calls", firstForwardCalls, got)
	}
	if got := countRecordingCalls(channel, "send_private_msg"); got != 0 {
		t.Fatalf("resolver forward staged %d private messages", got)
	}
}

func TestResolverForwardUsesCustomNodesWithoutPrivateStaging(t *testing.T) {
	channel := resolverForwardChannel()
	runtime := NewRuntime(BotConfig{BotQQ: "42", Name: "Diana"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "m1", SelfID: "42"}

	if err := runtime.sendForwardPluginResponse(context.Background(), event, resolverForwardTestResponse(), runtime.effectiveConfigForEvent(event)); err != nil {
		t.Fatalf("send forward: %v", err)
	}
	if got := countRecordingCalls(channel, "send_private_msg"); got != 0 {
		t.Fatalf("private staging calls = %d, want 0", got)
	}
	calls := recordedCallsByAction(channel.callsSnapshot(), "send_group_forward_msg")
	if len(calls) != 1 {
		t.Fatalf("group forward calls = %d, want 1", len(calls))
	}
	nodes, ok := calls[0].params["messages"].([]map[string]any)
	if !ok || len(nodes) != 3 {
		t.Fatalf("forward nodes = %#v", calls[0].params["messages"])
	}
	firstData, _ := nodes[0]["data"].(map[string]any)
	firstContent, _ := firstData["content"].([]map[string]any)
	if len(firstContent) != 1 || firstContent[0]["type"] != "text" {
		t.Fatalf("text node = %#v", nodes[0])
	}
	imageData, _ := nodes[1]["data"].(map[string]any)
	imageContent, _ := imageData["content"].([]map[string]any)
	if len(imageContent) != 1 || imageContent[0]["type"] != "image" {
		t.Fatalf("image node = %#v", nodes[1])
	}
}

func TestResolverForwardFallbackIsRecorded(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{BotQQ: "42"}, resolverForwardChannel(), NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "m1", SelfID: "42"}

	runtime.recordResolverForwardFallback(context.Background(), event, errors.New("forward unsupported"))

	entries := logs.entriesSnapshot()
	if len(entries) != 1 {
		t.Fatalf("fallback log entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Action != "qqbot.resolver_forward_fallback" || entry.Detail != "forward unsupported" {
		t.Fatalf("fallback log = %#v", entry)
	}
	if entry.Metadata["group_id"] != "20001" || entry.Metadata["message_id"] != "m1" {
		t.Fatalf("fallback metadata = %#v", entry.Metadata)
	}
}

// 打包请求超时时消息可能已被 QQ 投递，此时兜底直发就是「图集来两份」。
// 超时错误必须原样上抛，交给入站队列按账本重跑。
func TestResolverForwardDoesNotFallBackToDirectSendOnTimeout(t *testing.T) {
	channel := resolverForwardChannel()
	runtime := NewRuntime(BotConfig{BotQQ: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "m1", SelfID: "42"}
	resp := resolverForwardTestResponse()
	cfg := runtime.effectiveConfigForEvent(event)

	timeoutChannel := &forwardTimeoutChannel{recordingChannel: channel}
	runtime.channel = timeoutChannel

	err := runtime.sendForwardPluginResponse(context.Background(), event, resp, cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the timeout to surface as an error, got %v", err)
	}
	for _, msg := range channel.sentSnapshot() {
		if len(msg.ImageURLs) > 0 || strings.Contains(msg.Text, "图集") {
			t.Fatalf("timeout fell back to a direct send: %#v", msg)
		}
	}
}

func countRecordingCalls(channel *recordingChannel, action string) int {
	count := 0
	for _, call := range channel.callsSnapshot() {
		if call.action == action {
			count++
		}
	}
	return count
}

// WebUI 关闭「合并转发发送」后恢复普通消息投递，不生成转发卡片。
func TestResolverMergedForwardToggleUsesOriginalDirectDelivery(t *testing.T) {
	channel := resolverForwardChannel()
	runtime := NewRuntime(BotConfig{BotQQ: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "m1", SelfID: "42"}
	resp := resolverForwardTestResponse()
	resp.Forward = false

	if _, err := runtime.deliverResolverResponse(context.Background(), event, resp); err != nil {
		t.Fatalf("direct delivery failed: %v", err)
	}
	if got := countRecordingCalls(channel, "send_group_forward_msg"); got != 0 {
		t.Fatalf("toggle off still sent a merged forward: %d calls", got)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 {
		t.Fatalf("sent %d direct messages, want 1", len(sent))
	}
	if sent[0].Text != "某站图集 · 标题" || len(sent[0].ImageURLs) != 2 {
		t.Fatalf("original direct delivery was not restored: %#v", sent)
	}
}
