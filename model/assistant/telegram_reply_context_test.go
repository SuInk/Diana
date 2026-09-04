// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Telegram 在同一条更新里就把被引用消息的正文和发送者送来了，不该丢掉再去回查——
// 被引用的若是重启前的旧消息，回查必然落空，模型就没法针对引用做语义分析。
func TestTelegramQuotedMessageUsesInlineReplyContent(t *testing.T) {
	quoted := telegramQuotedMessage(&telegramMessage{
		MessageID: 4242,
		Text:      "明天七点老地方",
		From:      &telegramUser{ID: 10086, FirstName: "轩", LastName: "诺"},
		Chat:      &telegramChat{ID: -100200, Type: "supergroup"},
	})
	if quoted == nil {
		t.Fatal("引用内容整个丢了")
	}
	if quoted.MessageID != "4242" || quoted.UserID != "10086" || quoted.GroupID != "-100200" {
		t.Fatalf("引用标识不对：%#v", quoted)
	}
	if quoted.SenderName != "轩 诺" {
		t.Fatalf("发送者 = %q", quoted.SenderName)
	}
	if quoted.RawMessage != "明天七点老地方" {
		t.Fatalf("引用正文 = %q", quoted.RawMessage)
	}
	if len(quoted.Segments) != 1 || quoted.Segments[0].Data["text"] != "明天七点老地方" {
		t.Fatalf("引用段 = %#v", quoted.Segments)
	}
}

// 私聊没有群号，不能把对方的 chat id 当成 group_id 填进去。
func TestTelegramQuotedMessageOmitsGroupForPrivateChat(t *testing.T) {
	quoted := telegramQuotedMessage(&telegramMessage{
		MessageID: 7,
		Text:      "在吗",
		From:      &telegramUser{ID: 1, FirstName: "甲"},
		Chat:      &telegramChat{ID: 1, Type: "private"},
	})
	if quoted == nil || quoted.GroupID != "" {
		t.Fatalf("私聊不该带群号：%#v", quoted)
	}
}

// 引用的是图片时要留下可见的占位，否则模型只看到一条空引用。
func TestTelegramQuotedMessageKeepsPhotoPlaceholder(t *testing.T) {
	quoted := telegramQuotedMessage(&telegramMessage{
		MessageID: 9,
		From:      &telegramUser{ID: 2, FirstName: "乙"},
		Chat:      &telegramChat{ID: -1, Type: "supergroup"},
		Photo:     []telegramPhoto{{FileID: "small", FileSize: 10}, {FileID: "large", FileSize: 99}},
	})
	if quoted == nil {
		t.Fatal("图片引用被整个丢掉了")
	}
	if len(quoted.Segments) != 1 || quoted.Segments[0].Type != "image" {
		t.Fatalf("引用段 = %#v", quoted.Segments)
	}
	// Telegram 的 photo 数组由小到大，最后一个才是原图。
	if quoted.Segments[0].Data["file_id"] != "large" {
		t.Fatalf("应当取最大尺寸：%#v", quoted.Segments[0])
	}
}

func TestTelegramQuotedMessageIgnoresMissingReply(t *testing.T) {
	if quoted := telegramQuotedMessage(nil); quoted != nil {
		t.Fatalf("没有引用时应当为空：%#v", quoted)
	}
	if quoted := telegramQuotedMessage(&telegramMessage{MessageID: 0}); quoted != nil {
		t.Fatalf("没有消息 id 时应当为空：%#v", quoted)
	}
}

// Telegram 必须实现 ResultChannel，否则 Diana 自己发出的消息会以空 message_id 入库，
// 别人引用它时按 id 回查必然落空——这正是「引用 bot 消息 bot 不知道」的根因。
func TestTelegramChannelImplementsResultChannel(t *testing.T) {
	var channel Channel = NewTelegramChannel(TelegramConfig{BotToken: "t"})
	if _, ok := channel.(ResultChannel); !ok {
		t.Fatal("TelegramChannel 没有实现 ResultChannel")
	}
}

// 发送之后要把 Telegram 返回的 message_id 交回上层，上层据此把这条发言记进历史。
func TestTelegramSendWithResultReturnsMessageID(t *testing.T) {
	var method string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":20260904,"text":"hi"}}`))
	}))
	defer server.Close()

	channel := NewTelegramChannel(TelegramConfig{BotToken: "test-token", APIBaseURL: server.URL})
	result, err := channel.SendWithResult(context.Background(), OutgoingMessage{GroupID: "-100", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(method, "/sendMessage") {
		t.Fatalf("调用的方法 = %q", method)
	}
	// 走 apiMessageID 这条上层实际使用的路径，而不是自己解一遍。
	if id := apiMessageID(result); id != "20260904" {
		t.Fatalf("message_id = %q，期望 20260904", id)
	}
}

// 纯媒体消息也要能拿到 id：没有正文时用第一条媒体的返回值。
func TestTelegramSendWithResultFallsBackToMediaMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":555}}`))
	}))
	defer server.Close()

	channel := NewTelegramChannel(TelegramConfig{BotToken: "test-token", APIBaseURL: server.URL})
	result, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID:   "-100",
		ImageURLs: []string{"https://example.com/a.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id := apiMessageID(result); id != "555" {
		t.Fatalf("message_id = %q，期望 555", id)
	}
}

// Send 只是 SendWithResult 的薄封装，行为不能有差别。
func TestTelegramSendStillWorksAfterResultRefactor(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	channel := NewTelegramChannel(TelegramConfig{BotToken: "test-token", APIBaseURL: server.URL})
	if err := channel.Send(context.Background(), OutgoingMessage{UserID: "42", Text: "你好"}); err != nil {
		t.Fatal(err)
	}
	if payload["chat_id"] != "42" || payload["text"] != "你好" {
		t.Fatalf("payload = %#v", payload)
	}
}

// 走完整的消息转换，盯住「引用内容真的接进了事件」——只测 helper 的话，
// 把 telegramMessageToEvent 里那一行接线删掉，测试照样会过。
func TestTelegramMessageToEventCarriesQuotedContent(t *testing.T) {
	event := telegramMessageToEvent(&telegramMessage{
		MessageID: 100,
		Date:      1767225600,
		Text:      "这个几点？",
		From:      &telegramUser{ID: 555, FirstName: "丙"},
		Chat:      &telegramChat{ID: -100200, Type: "supergroup", Title: "测试群"},
		ReplyTo: &telegramMessage{
			MessageID: 99,
			Text:      "明天七点老地方",
			From:      &telegramUser{ID: 10086, FirstName: "轩诺"},
			Chat:      &telegramChat{ID: -100200, Type: "supergroup"},
		},
	}, "777", "diana_bot")

	if event.Quoted == nil {
		t.Fatal("事件里没有带上引用内容——模型因此没法针对引用做语义分析")
	}
	if event.Quoted.RawMessage != "明天七点老地方" {
		t.Fatalf("引用正文 = %q", event.Quoted.RawMessage)
	}
	if event.Quoted.MessageID != "99" || event.Quoted.SenderName != "轩诺" {
		t.Fatalf("引用来源不对：%#v", event.Quoted)
	}
	// reply 段仍然要保留：出站和跨平台的引用标记都依赖它。
	var hasReplySegment bool
	for _, segment := range event.Segments {
		if segment.Type == "reply" && segment.Data["id"] == "99" {
			hasReplySegment = true
		}
	}
	if !hasReplySegment {
		t.Fatalf("reply 段丢了：%#v", event.Segments)
	}
}
