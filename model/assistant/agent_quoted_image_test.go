// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func quotedImageSource(messageID, url string) MessageEvent {
	return MessageEvent{
		MessageID: messageID,
		Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"url": url}}},
	}
}

// 原图已经附给模型时，不能再补一句「尚无缓存描述」——模型同时看到图和这句话只会
// 自相矛盾。这正是「引用带图消息追问」答不出来的那类问题的收口。
func TestSourceImagesAllAttached(t *testing.T) {
	source := quotedImageSource("m1", "https://example.test/a.jpg")

	if !sourceImagesAllAttached(source, map[string]bool{"https://example.test/a.jpg": true}) {
		t.Fatal("attached original should suppress the summary line")
	}
	// 只附上一部分时仍要给摘要，剩下那张不能让模型空手猜。
	two := MessageEvent{MessageID: "m2", Segments: []MessageSegment{
		{Type: "image", Data: map[string]string{"url": "https://example.test/a.jpg"}},
		{Type: "image", Data: map[string]string{"url": "https://example.test/b.jpg"}},
	}}
	if sourceImagesAllAttached(two, map[string]bool{"https://example.test/a.jpg": true}) {
		t.Fatal("partially attached source still needs a summary")
	}
	// 一张都没附上。
	if sourceImagesAllAttached(source, map[string]bool{}) {
		t.Fatal("unattached source must keep its summary")
	}
	// 取不到 URL 的来源按「没附上」处理。
	noURL := MessageEvent{MessageID: "m3", Segments: []MessageSegment{{Type: "image", Data: map[string]string{}}}}
	if sourceImagesAllAttached(noURL, map[string]bool{"https://example.test/a.jpg": true}) {
		t.Fatal("source without resolvable urls must not be treated as attached")
	}
}

// Agent 模式下引用带图消息时，图片摘要还没算出来也必须能拿到原图。
// 回归的是：识图异步超时 -> 摘要为空 -> 原图又被剥掉 -> 模型只能回答「图片没加载到」。
func TestAgentQuotedImagesSurviveMissingDescriptions(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "10497",
		UserID:     "someone",
		MessageID:  "current",
		RawMessage: "看得到吗",
		Quoted: &QuotedMessage{
			MessageID: "quoted",
			UserID:    "other",
			Segments: []MessageSegment{
				{Type: "image", Data: map[string]string{"url": "https://example.test/a.jpg"}},
				{Type: "image", Data: map[string]string{"url": "https://example.test/b.jpg"}},
			},
		},
	}

	// 摘要一条都没有（识图未完成）时，仍要为引用来源保留文字说明。
	reference := runtime.agentCurrentHistoricalImageReference(t.Context(), event, nil)
	if reference == "" {
		t.Fatal("unattached quoted images must still be described")
	}

	// 原图附上之后，这段说明就该消失，避免和图片自相矛盾。
	attached := []string{"https://example.test/a.jpg", "https://example.test/b.jpg"}
	if got := runtime.agentCurrentHistoricalImageReference(t.Context(), event, attached); got != "" {
		t.Fatalf("attached originals should suppress the description: %q", got)
	}
}
