// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 回放真实的先文字后图片事件，只调用模型，不向生产群发送消息。
func TestLiveFollowUpImageGrounding(t *testing.T) {
	path := os.Getenv("DIANA_LIVE_FOLLOWUP_SAMPLE")
	if path == "" {
		t.Skip("DIANA_LIVE_FOLLOWUP_SAMPLE is required")
	}
	client := liveLLMClient(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []MessageEvent
	if err := json.Unmarshal(data, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatal("expected text and image events")
	}
	for _, otherSender := range []bool{false, true} {
		t.Run(fmt.Sprintf("other_sender=%v", otherSender), func(t *testing.T) {
			current := events[0]
			if otherSender {
				current.UserID = "replay-user"
				current.SenderName = "回放提问者"
			}
			runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
			runtime.SetMessageHistoryStore(newRecallImageTestStore())
			images := runtime.replyDependencyImages([]MessageEvent{current, events[1]}, current, nil, current.SelfID)
			if len(images) != 1 {
				t.Fatalf("follow-up images=%d", len(images))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			runtime.beginHistoryImageDescriptionForeground()
			defer runtime.endHistoryImageDescriptionForeground()
			message := runtime.senderDependencyMessage(ctx, current, &senderDependencyContext{images: images})
			if strings.Contains(message.Content, "【还没读到的图】") {
				t.Fatal("image description unavailable")
			}
			response, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: "根据提供的图片描述，简短回答用户。不要假装认识图中具体人物。"},
				message, {Role: llm.RoleUser, Content: historyPlainText(current)},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(response.Text) == "" || strings.Contains(response.Text, "没看到图") || strings.Contains(response.Text, "没收到图") {
				t.Fatalf("ungrounded reply: %s", response.Text)
			}
			t.Logf("model=%s description=%s reply=%s", response.Model, message.Content, response.Text)

		})
	}
}

// 仅回放原图选择，不生成图片、不向群里发送消息。
func TestLiveEditSourceSelection(t *testing.T) {
	path := os.Getenv("DIANA_LIVE_EDIT_SAMPLE")
	if path == "" {
		t.Skip("DIANA_LIVE_EDIT_SAMPLE is required")
	}
	client := liveLLMClient(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sample struct {
		Current  MessageEvent
		History  []MessageEvent
		Expected string
	}
	if err := json.Unmarshal(data, &sample); err != nil {
		t.Fatal(err)
	}
	// 生产请求本身有引用；第二种扰动去掉引用、插入较近的无关图片，用真实图片摘要中的特征指认旧图。
	for _, quoted := range []bool{true, false} {
		t.Run(fmt.Sprintf("quoted=%v", quoted), func(t *testing.T) {
			current := sample.Current
			text := historyPlainText(current)
			history := append([]MessageEvent(nil), sample.History...)
			if !quoted && len(history) > 1 {
				history[len(history)-1].Time = current.Time - 1
			}
			runtime := NewRuntime(BotConfig{BotAccount: current.SelfID}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
			for _, event := range history {
				runtime.remember(event)
			}
			if !quoted {
				current.Quoted = nil
				current.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "把临雀发的那张被窝里的女孩图改成黑白，保留原来的字。不是最近那张。"}}}
				text = historyPlainText(current)
			}
			decision, ok := runtime.classifyVisualIntent(context.Background(), current, text)
			if !ok || decision.Action != visualIntentEditImage || (!slices.Contains(decision.SourceMessageIDs, sample.Expected) || (!quoted && len(decision.SourceMessageIDs) != 1)) {
				t.Fatalf("incorrect source: %+v", decision)
			}
			t.Logf("selected=%s action=%s", sample.Expected, decision.Action)
		})
	}
}
