// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
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
	if countRecordingCalls(channel, "send_group_forward_msg") == 0 {
		t.Fatal("merged forward was never sent")
	}
	// 断言与投递机制无关：重跑不该产生任何新的「发送」调用，无论合并转发内部
	// 走的是自定义节点还是暂存 message_id。只数发送类动作，把写回历史时的
	// get_msg 这类读调用排除在外。
	firstSends := countRecordingSends(channel)

	// 模拟整条事件重跑：同一个 turn 重新建立处理上下文（步骤序号从头计数）、
	// 相同内容再走一遍。
	ctx = withOutboundTurn(context.Background(), "turn-1")
	if err := runtime.sendForwardPluginResponse(ctx, event, resp, cfg); err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	if got := countRecordingSends(channel); got != firstSends {
		t.Fatalf("replay produced %d extra outbound sends (%d -> %d)", got-firstSends, firstSends, got)
	}
	if len(channel.sentSnapshot()) != 0 {
		t.Fatalf("replay fell back to direct sends: %#v", channel.sentSnapshot())
	}
}

// 打包请求超时时消息可能已被平台投递，此时兜底直发就是「图集来两份」。
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

// countRecordingSends 只统计真正会在群里产生消息的动作。
func countRecordingSends(channel *recordingChannel) int {
	count := 0
	for _, call := range channel.callsSnapshot() {
		if strings.HasPrefix(call.action, "send_") {
			count++
		}
	}
	return count + len(channel.sentSnapshot())
}

// 合并转发优先用自定义节点：一个请求发完，且不给机器人自己发私聊——不少 OneBot 实现
// 协议实现根本不允许自发私聊，暂存方式在那里必然失败并静默退回散装。
func TestResolverForwardPrefersCustomNodesOverSelfStaging(t *testing.T) {
	channel := resolverForwardChannel()
	runtime := NewRuntime(BotConfig{BotQQ: "42", Name: "Diana"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "m1", SelfID: "42"}
	resp := resolverForwardTestResponse()

	if err := runtime.sendForwardPluginResponse(context.Background(), event, resp, runtime.effectiveConfigForEvent(event)); err != nil {
		t.Fatalf("merged forward failed: %v", err)
	}
	if got := countRecordingCalls(channel, "send_group_forward_msg"); got != 1 {
		t.Fatalf("expected exactly one merged-forward call, got %d", got)
	}
	if got := countRecordingCalls(channel, "send_private_msg"); got != 0 {
		t.Fatalf("custom nodes must not stage via self private messages, got %d", got)
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("merged forward degraded to direct sends: %#v", sent)
	}
}

// 实现不支持自定义节点里的图片时（SnowLuma 这类），退回暂存 message_id，
// 而不是直接放弃合并转发。
type customNodeRejectingChannel struct {
	*recordingChannel
}

func (c *customNodeRejectingChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if action == "send_group_forward_msg" {
		if messages, ok := params["messages"].([]map[string]any); ok && len(messages) > 0 {
			if data, ok := messages[0]["data"].(map[string]any); ok {
				if _, custom := data["content"]; custom {
					return nil, fmt.Errorf("unsupported forward node content")
				}
			}
		}
	}
	return c.recordingChannel.CallAPI(ctx, action, params)
}

func TestResolverForwardFallsBackToStagingWhenCustomNodesRejected(t *testing.T) {
	base := resolverForwardChannel()
	channel := &customNodeRejectingChannel{recordingChannel: base}
	runtime := NewRuntime(BotConfig{BotQQ: "42", Name: "Diana"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "m1", SelfID: "42"}
	resp := resolverForwardTestResponse()

	if err := runtime.sendForwardPluginResponse(context.Background(), event, resp, runtime.effectiveConfigForEvent(event)); err != nil {
		t.Fatalf("staged fallback failed: %v", err)
	}
	if got := countRecordingCalls(base, "send_private_msg"); got != len(resp.ForwardMessages) {
		t.Fatalf("staged fallback made %d staging calls, want %d", got, len(resp.ForwardMessages))
	}
	if sent := base.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("fallback degraded all the way to direct sends: %#v", sent)
	}
}
