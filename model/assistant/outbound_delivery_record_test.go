// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

// 事件详情的「回复结果」只有一个文本字段，说不出还发了转发卡片、几张图或一个
// 视频；发媒体不发文字时它甚至是空的，前端于是显示「未保存回复正文」——可东西
// 明明发出去了。逐条投递路径去补是补不完的，所以在两个真正的收口处记账。
func TestOutboundTurnCountsWhatWasActuallySent(t *testing.T) {
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "m-1"}

	ctx := withOutboundTurn(context.Background(), "evt-1")
	turn := outboundTurnFromContext(ctx)
	if turn == nil {
		t.Fatal("出站账本没有挂到 ctx 上")
	}
	if !turn.delivery().Empty() {
		t.Fatalf("一开始就不是空的：%#v", turn.delivery())
	}

	// URL 字段写法（插件用这种）
	if err := runtime.sendOutgoing(ctx, event, OutgoingMessage{
		Text: "解析好了", ImageURLs: []string{"a.jpg", "b.jpg"}, VideoURLs: []string{"c.mp4"},
	}); err != nil {
		t.Fatal(err)
	}
	// segment 写法（Agent 工具和 CQ 码走这种）
	if err := runtime.sendOutgoing(ctx, event, OutgoingMessage{
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"url": "d.jpg"}},
			{Type: "record", Data: map[string]string{"file": "e.silk"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	got := turn.delivery()
	if got.Messages != 2 {
		t.Errorf("messages = %d, want 2", got.Messages)
	}
	if got.Images != 3 {
		t.Errorf("images = %d, want 3（两种写法都要认）", got.Images)
	}
	if got.Videos != 1 {
		t.Errorf("videos = %d, want 1", got.Videos)
	}
	if got.Audios != 1 {
		t.Errorf("audios = %d, want 1", got.Audios)
	}
	if got.Empty() {
		t.Error("发过东西还报空")
	}
}

func TestOutboundTurnCountsForwardCards(t *testing.T) {
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m-2"}

	ctx := withOutboundTurn(context.Background(), "evt-2")
	nodes := []map[string]any{
		{"type": "node", "data": map[string]any{"content": "一"}},
		{"type": "node", "data": map[string]any{"content": "二"}},
		{"type": "node", "data": map[string]any{"content": "三"}},
	}
	if _, err := runtime.sendForwardNodesWithResult(ctx, event, nodes); err != nil {
		t.Fatal(err)
	}
	got := outboundTurnFromContext(ctx).delivery()
	if got.ForwardCards != 1 || got.ForwardNodes != 3 {
		t.Fatalf("转发卡片没记上：%#v", got)
	}
	// 转发卡片不该同时被算成一条普通消息。
	if got.Messages != 0 {
		t.Fatalf("转发卡片被重复计入普通消息：%#v", got)
	}
}

// 没有出站账本时（后台子任务用运行时根 context）不能崩。
func TestOutboundDeliveryToleratesMissingTurn(t *testing.T) {
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "m-3"}
	if err := runtime.sendOutgoing(context.Background(), event, OutgoingMessage{Text: "你好"}); err != nil {
		t.Fatal(err)
	}
	if got := outboundTurnFromContext(context.Background()).delivery(); !got.Empty() {
		t.Fatalf("没有账本却算出了内容：%#v", got)
	}
}

func TestOutboundDeliveryEmpty(t *testing.T) {
	if !(OutboundDelivery{}).Empty() {
		t.Fatal("零值应当是空")
	}
	for name, delivery := range map[string]OutboundDelivery{
		"消息":   {Messages: 1},
		"图片":   {Images: 1},
		"视频":   {Videos: 1},
		"语音":   {Audios: 1},
		"转发卡片": {ForwardCards: 1},
	} {
		if delivery.Empty() {
			t.Errorf("%s 不应当算空：%#v", name, delivery)
		}
	}
}
