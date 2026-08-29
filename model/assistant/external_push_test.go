// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

// 对外推送必须带上目标路由信息，否则多通道部署时消息会走错平台。
func TestPushExternalMessageRoutesToTarget(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)

	err := runtime.PushExternalMessage(context.Background(), ExternalMessageTarget{
		Platform: "telegram",
		GroupID:  "123456",
		UserID:   "10001",
	}, "构建完成")
	if err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("sent = %#v", channel.sent)
	}
	msg := channel.sent[0]
	if msg.Platform != "telegram" || msg.GroupID != "123456" {
		t.Fatalf("message not routed to target: %#v", msg)
	}
	// 群聊目标带 UserID 时按订阅推送的规则点名。
	if msg.MentionUserID != "10001" {
		t.Fatalf("group push should mention the target user: %#v", msg)
	}
}

func TestPushExternalMessagePrivateTarget(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)

	if err := runtime.PushExternalMessage(context.Background(), ExternalMessageTarget{UserID: "10001"}, "部署失败，请查看日志"); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].UserID != "10001" || channel.sent[0].GroupID != "" {
		t.Fatalf("private push = %#v", channel.sent)
	}
}

func TestPushExternalMessageValidatesInput(t *testing.T) {
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)

	if err := runtime.PushExternalMessage(context.Background(), ExternalMessageTarget{UserID: "10001"}, "  "); err == nil {
		t.Fatal("empty text should be rejected")
	}
	if err := runtime.PushExternalMessage(context.Background(), ExternalMessageTarget{}, "hi"); err == nil {
		t.Fatal("missing target should be rejected")
	}
	if len(channel.sent) != 0 {
		t.Fatalf("nothing should be sent on validation failure: %#v", channel.sent)
	}
}
