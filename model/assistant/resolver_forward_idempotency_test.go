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
	if got := countRecordingCalls(channel, "send_private_msg"); got != len(resp.ForwardMessages) {
		t.Fatalf("replay re-staged forward nodes: %d staging calls, want %d", got, len(resp.ForwardMessages))
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

// WebUI 关闭「合并转发发送」后，解析结果逐条直发，节点内容一条不丢。
func TestResolverMergedForwardToggleSendsNodesDirectly(t *testing.T) {
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
	if len(sent) != len(resp.ForwardMessages) {
		t.Fatalf("sent %d direct messages, want %d", len(sent), len(resp.ForwardMessages))
	}
	if sent[0].Text != "某站图集 · 标题" || len(sent[1].ImageURLs) != 1 || len(sent[2].ImageURLs) != 1 {
		t.Fatalf("node content lost in direct delivery: %#v", sent)
	}
}
