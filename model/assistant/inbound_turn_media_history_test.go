// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

// 线上复现：发一个文件，两秒后问「这是什么」。合并只该服务那一轮回复；以前合并后的
// 版本原样进了历史，同一个文件出现在两条消息里，查历史找文件时排在前面的是那句文字。
func TestMergedTurnMediaStaysOutOfHistory(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	media := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "image-1", Time: 100,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": "data:image/png;base64,YQ=="}}},
	}
	runtime.remember(media)
	question := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "question-1", Time: 102,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "这是什么"}}},
	}
	turn := attachInboundTurnMedia(question, []MessageEvent{media})
	// 这一轮回复照样图文一起看。
	if len(turn.Segments) != 2 || turn.Segments[1].Type != "image" {
		t.Fatalf("turn segments = %#v", turn.Segments)
	}
	runtime.remember(turn)

	history := runtime.contextHistory(MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-2", MessageID: "later"})
	images := 0
	for _, item := range history {
		images += historicalStillImageCount(item)
		if item.MessageID != "question-1" {
			continue
		}
		if len(item.Segments) != 1 || item.Segments[0].Type != "text" {
			t.Fatalf("question kept borrowed media in history: %#v", item.Segments)
		}
		if strings.Join(eventSemanticSourceMessageIDs(item), ",") != "image-1" {
			t.Fatalf("question lost its media source: %#v", item)
		}
	}
	if images != 1 {
		t.Fatalf("image appears %d times in history", images)
	}

	// 模型拿那句文字的 ID 来取图，顺着找到原来那条媒体消息。
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-2", MessageID: "later"})
	output, err := tool.Run(context.Background(), map[string]any{"message_id": "question-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"message_id":"image-1"`) || !strings.Contains(output, `"loaded":1`) || strings.Contains(output, `"failed":1`) {
		t.Fatalf("output = %s", output)
	}
	if parts := tool.ToolResultParts(output); len(parts) != 1 || parts[0].ImageURL != "data:image/png;base64,YQ==" {
		t.Fatalf("parts = %#v", parts)
	}
}

// 没有来源可顺的纯文字消息照旧报没有图，两条互相指着的消息也不会绕圈。
func TestHistoryMediaFollowsSourcesOnlyOnce(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	first := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "a", Time: 1,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "一"}}}}
	setEventSemanticSourceMessageIDs(&first, []string{"b"})
	second := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "b", Time: 2,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "二"}}}}
	setEventSemanticSourceMessageIDs(&second, []string{"a"})
	runtime.remember(first)
	runtime.remember(second)
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-2"})
	if _, err := tool.Run(context.Background(), map[string]any{"message_id": "a"}); err == nil || !strings.Contains(err.Error(), "没有原始图片") {
		t.Fatalf("err = %v", err)
	}
}
