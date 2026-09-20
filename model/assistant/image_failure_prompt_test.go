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

// blindVisionProvider 模拟线上那条链路：请求成功，但模型侧没有收到图片输入。
type blindVisionProvider struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
}

func (p *blindVisionProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	var body strings.Builder
	for _, message := range req.Messages {
		body.WriteString(message.Content)
		for _, part := range message.Parts {
			body.WriteString(part.Text)
		}
	}
	switch {
	case strings.Contains(body.String(), "请为这张图片生成可复用的客观中文描述"):
		return &llm.GenerateResponse{Model: "blind", Text: "未收到图片内容，无法生成描述。请重新发送需要记录的图片。"}, nil
	case strings.Contains(body.String(), "send_confidence"):
		return &llm.GenerateResponse{Model: "blind", Text: `{"send_confidence":0.99,"reason":"ok"}`}, nil
	}
	return &llm.GenerateResponse{Model: "blind", Text: "好的喵"}, nil
}

func (p *blindVisionProvider) currentMessages() []llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []llm.Message
	for _, request := range p.requests {
		for _, message := range request.Messages {
			if message.Role == llm.RoleUser && strings.Contains(message.Content, "【当前需要回复的消息】") {
				out = append(out, message)
			}
		}
	}
	return out
}

// TestReplyPromptExplainsImageFailure 端到端跑一遍真实回复路径：视觉模型说没收到图时，
// 提示词里必须留下说明。单测判定函数本身很容易过，但那段话有没有真的拼进提示词是另一
// 回事——第一版就漏在这里，提示词里那一段整块消失，模型照样会猜成「用户没发图」。
func TestReplyPromptExplainsImageFailure(t *testing.T) {
	imagePath, hash := writeRecallImageFixture(t)
	provider := &blindVisionProvider{}
	runtime := NewRuntime(BotConfig{BotAccount: "bot", AgentEnabled: true}, &recordingChannel{},
		NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetMessageHistoryStore(newRecallImageTestStore())

	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "u1", MessageID: "m1",
		SenderName: "Rim", RawMessage: "这张图什么意思", Time: 1_800_000_000,
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"cached_file": imagePath, imageContentSHA256Key: hash}},
			{Type: "text", Data: map[string]string{"text": "这张图什么意思"}},
		},
	}
	if _, err := runtime.replyTo(context.Background(), event, event.RawMessage); err != nil {
		t.Fatal(err)
	}

	messages := provider.currentMessages()
	if len(messages) == 0 {
		t.Fatal("the chat model never received the current message")
	}
	found := false
	for _, message := range messages {
		if !strings.Contains(message.Content, "【本轮图片处理失败】") {
			continue
		}
		found = true
		if !strings.Contains(message.Content, event.RawMessage) {
			t.Fatalf("the notice replaced the user's own text:\n%s", message.Content)
		}
		for _, want := range []string{"这条视觉链路不接受图片输入", "不要说没收到图片"} {
			if !strings.Contains(message.Content, want) {
				t.Fatalf("notice missing %q:\n%s", want, message.Content)
			}
		}
		// 原图还挂在这条消息上，所以措辞不能替模型断言它看不了图。
		if !llmMessageHasImagePart(message) {
			t.Fatalf("the raw image left the current message:\n%s", message.Content)
		}
		if !strings.Contains(message.Content, "原图仍附在本条消息里") {
			t.Fatalf("notice ignored the still-attached image:\n%s", message.Content)
		}
	}
	if !found {
		t.Fatalf("no image failure notice reached the prompt; messages=%d", len(messages))
	}
}
