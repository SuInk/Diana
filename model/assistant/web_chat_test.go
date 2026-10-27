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

func TestWebChatRequiresNumericOwner(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if info := runtime.WebChatInfo("bot"); !strings.Contains(info.Problem, "主人") {
		t.Fatalf("没有主人账号时应拒绝，info = %#v", info)
	}
	if _, err := runtime.WebChatSend(context.Background(), "bot", "hi", func(WebChatMessage) {}); err == nil {
		t.Fatal("没有主人账号时 WebChatSend 应报错")
	}
	runtime = NewRuntime(BotConfig{ID: "bot", OwnerID: "@someone"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if info := runtime.WebChatInfo("bot"); !strings.Contains(info.Problem, "数字 ID") {
		t.Fatalf("主人填用户名时应拒绝，info = %#v", info)
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
