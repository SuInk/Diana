// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func TestCoarseRelativeTimingIsStableAcrossNearbyTurns(t *testing.T) {
	for _, tc := range []struct {
		delta int64
		want  string
	}{
		{delta: 0, want: "不到 1 分钟"},
		{delta: 59, want: "不到 1 分钟"},
		{delta: 120, want: "约 2 分钟"},
		{delta: 4961, want: "约 1 小时"},
		{delta: 200000, want: "约 2 天"},
	} {
		if got := coarseRelativeTiming(tc.delta); got != tc.want {
			t.Fatalf("coarseRelativeTiming(%d) = %q, want %q", tc.delta, got, tc.want)
		}
	}
	// 同一条历史在相邻两轮里必须渲染成同一个字符串，否则整段历史无法命中前缀缓存。
	first := contextMessageTiming(1000, 1100)
	second := contextMessageTiming(1000, 1110)
	if first != second {
		t.Fatalf("history timing drifted between turns: %q vs %q", first, second)
	}
}

func TestAgentImageHistoryLineCarriesCachedDescriptions(t *testing.T) {
	event := MessageEvent{
		Time:       1000,
		SenderName: "轩诺",
		MessageID:  "image-message-1",
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{}},
		},
	}

	bare := agentImageHistoryPromptTextWithDescriptions(event, 1120, nil)
	if !strings.Contains(bare, "message_id=image-message-1；image_count=1；当前未附加原图") {
		t.Fatalf("bare line = %q", bare)
	}
	if strings.Contains(bare, "报纸与干花") || !strings.Contains(bare, dianaHistoryImagesToolName) {
		t.Fatalf("bare line should not invent a description: %q", bare)
	}

	described := agentImageHistoryPromptTextWithDescriptions(event, 1120, []string{"图片1摘要=报纸与干花拼贴的二次元人物"})
	if !strings.Contains(described, "image_count=1") {
		t.Fatalf("described line lost the image count: %q", described)
	}
	if !strings.Contains(described, "图片1摘要=报纸与干花拼贴的二次元人物") {
		t.Fatalf("described line = %q", described)
	}
}

func TestAgentImageHistoryPromptNeverSerializesImagePlaceholder(t *testing.T) {
	event := MessageEvent{
		MessageID: "image-with-placeholder",
		UserID:    "user-1",
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "说明 [图片] 之后的内容"}},
			{Type: "image", Data: map[string]string{"url": "data:image/png;base64,YQ=="}},
		},
		Quoted: &QuotedMessage{
			MessageID: "quoted-image",
			Segments: []MessageSegment{
				{Type: "text", Data: map[string]string{"text": "[图片] 引用说明"}},
				{Type: "image", Data: map[string]string{"url": "data:image/png;base64,Yg=="}},
			},
		},
	}
	text := agentImageHistoryPromptTextWithDescriptions(event, 0, nil)
	if strings.Contains(text, "[图片]") || !strings.Contains(text, "说明") || !strings.Contains(text, "引用说明") {
		t.Fatalf("history image prompt = %q", text)
	}
}
