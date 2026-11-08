// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 内置机器人不用填任何连接就能保存、一直在线；往平台上发东西明确报错。
func TestLocalBotNeedsNoConnectionAndStaysOnline(t *testing.T) {
	cfg := BotConfig{ID: "helper", Name: "小助手", Platform: "builtin"}.WithDefaults()
	if NormalizePlatformID(cfg.Platform) != PlatformLocal {
		t.Fatalf("builtin 应归一成 %q，得到 %q", PlatformLocal, NormalizePlatformID(cfg.Platform))
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("内置机器人不该要求连接配置：%v", err)
	}
	if !profileRunnable(cfg) {
		t.Fatal("内置机器人应能启动")
	}
	channel := NewChannelForConfig(cfg)
	status := channel.Status()
	if !status.Connected || status.Platform != PlatformLocal || status.SelfID != "local-helper" {
		t.Fatalf("status = %#v", status)
	}
	if err := channel.Send(context.Background(), OutgoingMessage{UserID: "10001", Text: "hi"}); !errors.Is(err, errLocalBotNoPlatform) {
		t.Fatalf("Send error = %v, want errLocalBotNoPlatform", err)
	}
}

// 内置机器人能在聊天页私聊：回复交给页面，不走通道。
func TestLocalBotRepliesInWebChat(t *testing.T) {
	cfg := BotConfig{ID: "helper", Name: "小助手", Platform: PlatformLocal}
	channel := newLocalChannel(cfg)
	provider := &capturingLLMProvider{reply: "我在，只住在 Diana 里"}
	runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	var replies []string
	if _, err := runtime.WebChatSend(context.Background(), "helper", "你在哪", func(msg WebChatMessage) {
		replies = append(replies, msg.Text)
	}); err != nil {
		t.Fatalf("WebChatSend() error = %v", err)
	}
	if len(replies) == 0 || !strings.Contains(replies[0], "Diana") {
		t.Fatalf("replies = %q", replies)
	}
	if info := runtime.WebChatInfo("helper"); info.Problem != "" || info.Platform != PlatformLocal {
		t.Fatalf("WebChatInfo = %#v", info)
	}
}
