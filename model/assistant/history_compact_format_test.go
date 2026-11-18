// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

// 被引用的那条就在这一份历史窗口里时，历史行只留引用标记，不再把原文抄一遍；滑出窗口
// 的才展开。
func TestHistoryQuoteExpandsOnlyOutsideWindow(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "10000"}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	quoted := MessageEvent{Kind: EventKindGroup, GroupID: "30001", UserID: "10002", SenderName: "甲", SelfID: "10000", MessageID: "50001", RawMessage: "今天晚饭吃火锅", Time: 1700000000, ProfileID: "p1",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "今天晚饭吃火锅"}}}}
	reply := MessageEvent{Kind: EventKindGroup, GroupID: "30001", UserID: "10003", SenderName: "乙", SelfID: "10000", MessageID: "50002", RawMessage: "算我一个", Time: 1700000060, ProfileID: "p1",
		Segments: []MessageSegment{{Type: "reply", Data: map[string]string{"id": "50001"}}, {Type: "text", Data: map[string]string{"text": "算我一个"}}},
		Quoted:   &QuotedMessage{MessageID: "50001", UserID: "10002", SenderName: "甲", Segments: quoted.Segments}}
	current := MessageEvent{Kind: EventKindGroup, GroupID: "30001", UserID: "10004", SelfID: "10000", MessageID: "50003", RawMessage: "在吗", Time: 1700000120, ProfileID: "p1"}
	render := func(history []MessageEvent) string {
		messages, _ := runtime.stableGroupHistory(context.Background(), current, runtime.effectiveConfigForEvent(current), history, false, nil)
		var builder strings.Builder
		for _, message := range messages {
			builder.WriteString(message.Content + "\n")
		}
		return builder.String()
	}
	inWindow := render([]MessageEvent{quoted, reply})
	if strings.Contains(inWindow, "【被引用的消息】") || strings.Count(inWindow, "今天晚饭吃火锅") != 1 {
		t.Fatalf("a quote already in the window must not be expanded again:\n%s", inWindow)
	}
	if !strings.Contains(inWindow, "<diana-quote ") {
		t.Fatalf("the reply line must still point at the quoted message:\n%s", inWindow)
	}
	outside := render([]MessageEvent{reply})
	if !strings.Contains(outside, "【被引用的消息】") || !strings.Contains(outside, "今天晚饭吃火锅") {
		t.Fatalf("a quote outside the window must be expanded:\n%s", outside)
	}
}

// 历史行里带图的消息：行首已有「#消息ID」时媒体行只写种类和数量。
func TestHistoryMediaLineDoesNotRepeatMessageID(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "30001", UserID: "10002", SenderName: "甲", MessageID: "50001", Platform: PlatformOneBotV11, Time: 1700000000, ProfileID: "p1",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.com/a.png"}}}}
	line := agentImageHistoryPromptTextWithDescriptions(event, 0, []string{"图片1摘要=一只猫"})
	if !strings.Contains(line, "#50001") || strings.Count(line, "50001") != 1 || !strings.Contains(line, "【图片×1】") {
		t.Fatalf("media line = %q", line)
	}
}
