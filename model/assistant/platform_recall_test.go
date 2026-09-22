// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// failingRecallChannel 让 delete_msg 固定失败，用来验证「撤不掉就得如实说」。
type failingRecallChannel struct {
	*recordingChannel
}

func (c *failingRecallChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if _, err := c.recordingChannel.CallAPI(ctx, action, params); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("retcode 200: message not found or expired")
}

func recallTestEvent() MessageEvent {
	return MessageEvent{Kind: EventKindGroup, UserID: "555", GroupID: "123", SelfID: "10000", Platform: PlatformOneBotV11}
}

// seedRecallHistory 往会话历史里放一条别人的消息和两条机器人自己的消息。
func seedRecallHistory(runtime *Runtime, event MessageEvent) {
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: event.GroupID, SelfID: event.SelfID, Platform: event.Platform, UserID: "555", MessageID: "inbound-1", RawMessage: "别人说的话"})
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: event.GroupID, SelfID: event.SelfID, Platform: event.Platform, UserID: event.SelfID, MessageID: "out-1", RawMessage: "第一条回复", Outbound: true})
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: event.GroupID, SelfID: event.SelfID, Platform: event.Platform, UserID: event.SelfID, MessageID: "out-2", RawMessage: "说错了的那条", Outbound: true})
}

func TestPlatformRecallDeletesLatestOwnMessage(t *testing.T) {
	channel := newModerationTestChannel("member")
	event := recallTestEvent()
	tool, runtime, logs := platformToolFor(t, BotConfig{BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)
	seedRecallHistory(runtime, event)

	// 省略 message_id 就撤最近那条：说错话的当下就是刚发出去的那一条。
	out, err := tool.Run(context.Background(), map[string]any{"operation": "recall"})
	if err != nil {
		t.Fatalf("recall error = %v", err)
	}
	if !strings.Contains(out, "out-2") {
		t.Fatalf("recall result = %s", out)
	}
	calls := recordedCallsByAction(channel.callsSnapshot(), "delete_msg")
	if len(calls) != 1 {
		t.Fatalf("delete_msg calls = %#v", calls)
	}
	if got := fmt.Sprint(calls[0].params["message_id"]); got != "out-2" {
		t.Fatalf("撤回打到了别的消息上：%s", got)
	}
	var recorded bool
	for _, entry := range logs.entriesSnapshot() {
		if entry.Target == "out-2" {
			recorded = true
		}
	}
	if !recorded {
		t.Fatal("撤回没有留下审计记录")
	}

	// 指定 ID 时按 ID 撤，不再退回最近一条。
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "recall", "message_id": "out-1"}); err != nil {
		t.Fatalf("recall by id error = %v", err)
	}
	calls = recordedCallsByAction(channel.callsSnapshot(), "delete_msg")
	if len(calls) != 2 || fmt.Sprint(calls[1].params["message_id"]) != "out-1" {
		t.Fatalf("delete_msg calls = %#v", calls)
	}
}

// TestPlatformRecallRefusesOtherPeoplesMessages 撤回只能作用于机器人自己发的消息。
// 这条边界由工具强制，不靠提示词：猜错一次就是删掉群友的发言。
func TestPlatformRecallRefusesOtherPeoplesMessages(t *testing.T) {
	channel := newModerationTestChannel("admin")
	event := recallTestEvent()
	tool, runtime, _ := platformToolFor(t, BotConfig{BotAccount: "10000", Platform: PlatformOneBotV11, OwnerID: "555"}, channel, event)
	seedRecallHistory(runtime, event)

	_, err := tool.Run(context.Background(), map[string]any{"operation": "recall", "message_id": "inbound-1"})
	if err == nil || !strings.Contains(err.Error(), "只能撤回我自己发出的消息") {
		t.Fatalf("撤回别人消息的错误 = %v", err)
	}
	if calls := recordedCallsByAction(channel.callsSnapshot(), "delete_msg"); len(calls) != 0 {
		t.Fatalf("拒绝之后仍然调用了平台接口：%#v", calls)
	}

	// 编出来的 ID 一律拒绝，并把真正能撤的几条列回去。
	_, err = tool.Run(context.Background(), map[string]any{"operation": "recall", "message_id": "凭印象编的"})
	if err == nil || !strings.Contains(err.Error(), "out-2") {
		t.Fatalf("未知 ID 的错误 = %v", err)
	}
	if calls := recordedCallsByAction(channel.callsSnapshot(), "delete_msg"); len(calls) != 0 {
		t.Fatalf("未知 ID 仍然调用了平台接口：%#v", calls)
	}
}

// TestPlatformRecallKeepsLocalPlaceholdersOut 平台没回消息 ID 时历史里存的是本地占位，
// 拿它去调接口只会删错。
func TestPlatformRecallKeepsLocalPlaceholdersOut(t *testing.T) {
	channel := newModerationTestChannel("member")
	event := recallTestEvent()
	tool, runtime, _ := platformToolFor(t, BotConfig{BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: event.GroupID, SelfID: event.SelfID, Platform: event.Platform, UserID: event.SelfID, MessageID: localOutboundIDPrefix + "abc", RawMessage: "没有平台 ID 的回复", Outbound: true})

	_, err := tool.Run(context.Background(), map[string]any{"operation": "recall"})
	if err == nil || !strings.Contains(err.Error(), "没有可撤回的消息") {
		t.Fatalf("本地占位的错误 = %v", err)
	}
	_, err = tool.Run(context.Background(), map[string]any{"operation": "recall", "message_id": localOutboundIDPrefix + "abc"})
	if err == nil || !strings.Contains(err.Error(), "没有平台消息 ID") {
		t.Fatalf("指定本地占位的错误 = %v", err)
	}
	if calls := recordedCallsByAction(channel.callsSnapshot(), "delete_msg"); len(calls) != 0 {
		t.Fatalf("本地占位仍然调用了平台接口：%#v", calls)
	}
}

// TestPlatformRecallReportsFailureInsteadOfClaimingSuccess 撤不掉时原消息还在，
// 错误里必须写明这一点，否则模型会照旧说「已撤回」。
func TestPlatformRecallReportsFailureInsteadOfClaimingSuccess(t *testing.T) {
	channel := &failingRecallChannel{recordingChannel: &recordingChannel{apiResponses: map[string]map[string]any{}}}
	event := recallTestEvent()
	tool, runtime, _ := platformToolFor(t, BotConfig{BotAccount: "10000", Platform: PlatformOneBotV11}, channel, event)
	seedRecallHistory(runtime, event)

	_, err := tool.Run(context.Background(), map[string]any{"operation": "recall"})
	if err == nil {
		t.Fatal("平台报错时不能返回成功")
	}
	for _, want := range []string{"message not found or expired", "原消息仍在群里", "不要声称已经撤回"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("失败原因缺少 %q：%v", want, err)
		}
	}
}

func TestPlatformRecallUnsupportedPlatformSaysSo(t *testing.T) {
	channel := newModerationTestChannel("member")
	event := recallTestEvent()
	event.Platform = PlatformFeishu
	tool, runtime, _ := platformToolFor(t, BotConfig{BotAccount: "10000", Platform: PlatformFeishu}, channel, event)
	seedRecallHistory(runtime, event)

	_, err := tool.Run(context.Background(), map[string]any{"operation": "recall"})
	if err == nil || !strings.Contains(err.Error(), "不支持撤回") {
		t.Fatalf("不支持撤回的平台错误 = %v", err)
	}
	if calls := recordedCallsByAction(channel.callsSnapshot(), "delete_msg"); len(calls) != 0 {
		t.Fatalf("不支持的平台仍然调用了接口：%#v", calls)
	}
}

// TestPlatformRecallIsNotOwnerOnly 撤回自己的话不该要主人身份：说错的是机器人，
// 让它改口的可以是任何人。
func TestPlatformRecallIsNotOwnerOnly(t *testing.T) {
	channel := newModerationTestChannel("member")
	event := recallTestEvent()
	tool, _, _ := platformToolFor(t, BotConfig{BotAccount: "10000", Platform: PlatformOneBotV11, OwnerID: "owner-not-speaker"}, channel, event)
	schema := tool.InputSchema()
	properties, _ := schema["properties"].(map[string]any)
	operation, _ := properties["operation"].(map[string]any)
	values := fmt.Sprint(operation["enum"])
	if !strings.Contains(values, platformOpRecall) {
		t.Fatalf("非主人看不到 recall：%s", values)
	}
	if strings.Contains(values, platformOpKick) {
		t.Fatalf("非主人不该看到群管理操作：%s", values)
	}
}
