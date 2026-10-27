// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestWebChatSendRepliesInPageNotPlatform(t *testing.T) {
	runtime, channel, provider := newVoiceCallTestRuntime(t, "在呢，刚吃完饭")
	var mu sync.Mutex
	var emitted []WebChatMessage
	user, err := runtime.WebChatSend(context.Background(), "bot", "在干嘛", func(msg WebChatMessage) {
		mu.Lock()
		emitted = append(emitted, msg)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("WebChatSend() error = %v", err)
	}
	if user.Role != "user" || user.Text != "在干嘛" || !strings.HasPrefix(user.ID, webChatMessagePrefix) {
		t.Fatalf("user = %#v", user)
	}
	if len(emitted) == 0 || emitted[0].Role != "assistant" || !strings.Contains(emitted[0].Text, "在呢") {
		t.Fatalf("emitted = %#v", emitted)
	}
	channel.mu.Lock()
	sent := len(channel.sent)
	channel.mu.Unlock()
	if sent != 0 {
		t.Fatalf("聊天页的回复不该发到平台上，sent = %#v", channel.sent)
	}
	// 打字聊天就是普通私聊，不带通话的口语提示。
	for _, message := range provider.requestSnapshot().Messages {
		if message.Role == llm.RoleSystem && strings.Contains(message.Content, "实时语音通话") {
			t.Fatal("聊天页不该注入语音通话提示")
		}
	}

	history, err := runtime.WebChatHistory(context.Background(), "bot", 50)
	if err != nil {
		t.Fatalf("WebChatHistory() error = %v", err)
	}
	var sawUser, sawBot bool
	for _, msg := range history {
		if msg.Role == "user" && msg.Text == "在干嘛" {
			sawUser = true
		}
		if msg.Role == "assistant" && strings.Contains(msg.Text, "在呢") {
			sawBot = true
		}
	}
	if !sawUser || !sawBot {
		t.Fatalf("私聊记录里缺聊天页的内容：user=%v bot=%v history=%#v", sawUser, sawBot, history)
	}
}

// 登录 WebUI 的人就是主人：没填主人、Telegram 主人填的是用户名，都照样能聊，而且按主人对待。
func TestWebChatTreatsLoggedInUserAsOwnerForEveryBot(t *testing.T) {
	for _, cfg := range []BotConfig{
		{ID: "bot"},
		{ID: "bot", Platform: PlatformTelegram, OwnerID: "@someone_here"},
		{ID: "bot", Platform: "qq-official", OwnerID: "A1B2C3D4E5F6"},
	} {
		runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		if info := runtime.WebChatInfo("bot"); info.Problem != "" {
			t.Fatalf("%+v: info = %#v", cfg, info)
		}
		gotCfg, event, err := runtime.ownerChatEvent("bot")
		if err != nil {
			t.Fatalf("%+v: ownerChatEvent() error = %v", cfg, err)
		}
		if !gotCfg.IsOwnerEvent(event) {
			t.Fatalf("%+v: 聊天页的登录者没被当成主人，event = %#v", cfg, event)
		}
	}
	// 平台上的陌生人不会因为机器人没填主人就被当成主人。
	cfg := BotConfig{ID: "bot"}
	if cfg.IsOwnerEvent(MessageEvent{ProfileID: "bot", UserID: webChatOwnerID, Kind: EventKindPrivate}) {
		t.Fatal("平台消息冒用 webui-owner 被当成了主人")
	}
}

func TestWebChatUsesRememberedTelegramOwnerID(t *testing.T) {
	cfg := BotConfig{ID: "bot", Platform: PlatformTelegram, OwnerID: "@someone_here"}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.telegramOwnerIDs.Store("bot", "12345")
	gotCfg, event, err := runtime.ownerChatEvent("bot")
	if err != nil {
		t.Fatal(err)
	}
	// 用记下的数字 ID，聊天页和 Telegram 上的私聊是同一段记录。
	if event.UserID != "12345" || !gotCfg.IsOwnerEvent(event) {
		t.Fatalf("event = %#v", event)
	}
}

func TestDeliverToWebChatKeepsOnlyWebImages(t *testing.T) {
	var got []WebChatMessage
	ctx := withWebChatSink(context.Background(), func(msg WebChatMessage) { got = append(got, msg) })
	result, ok := deliverToWebChat(ctx, OutgoingMessage{Text: "看这个", ImageURLs: []string{"https://example.com/a.png", "file:///tmp/b.png", "base64://AAAA"}})
	if !ok || result["message_id"] == "" {
		t.Fatalf("deliverToWebChat() = %v, %v", result, ok)
	}
	if len(got) != 1 || got[0].Text != "看这个" || len(got[0].Images) != 1 || got[0].Images[0] != "https://example.com/a.png" {
		t.Fatalf("got = %#v", got)
	}
	if _, ok := deliverToWebChat(context.Background(), OutgoingMessage{Text: "x"}); ok {
		t.Fatal("没有聊天页时不该拦截发送")
	}
}
