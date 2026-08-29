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

func TestAgentVideoHistoryLineCarriesCachedFrameDescriptions(t *testing.T) {
	event := MessageEvent{
		Time:       1000,
		SenderName: "轩诺",
		MessageID:  "video-message-1",
		Segments: []MessageSegment{
			{Type: "video", Data: map[string]string{"file": "clip.mp4"}},
			{Type: "image", Data: map[string]string{"source_type": "video_frame"}},
		},
	}

	text := agentImageHistoryPromptTextWithDescriptions(event, 1120, []string{"视频关键帧1摘要=桌面上放着一台笔记本电脑"})
	for _, want := range []string{
		"message_id=video-message-1",
		"video_count=1",
		"video_frame_count=1",
		"audio_count=0",
		"file_count=0",
		"视频关键帧1摘要=桌面上放着一台笔记本电脑",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("video history line missing %q: %q", want, text)
		}
	}
	if !strings.Contains(text, dianaHistoryImagesToolName) {
		t.Fatalf("video history must advertise the historical media tool: %q", text)
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
	if !strings.Contains(bare, "message_id=image-message-1；image_count=1；video_count=0；video_frame_count=0；audio_count=0；file_count=0；当前未附加原图、视频帧或其他媒体原件") {
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

func TestAgentHistoryLineCarriesVoiceAndSupportedFileDescriptions(t *testing.T) {
	event := MessageEvent{
		MessageID: "media-message-1",
		Segments: []MessageSegment{
			{Type: "record", Data: map[string]string{voiceSTTTranscriptKey: "明天下午三点开会"}},
			{Type: "file", Data: map[string]string{"name": "会议纪要.pdf"}},
			{Type: "file", Data: map[string]string{"name": "数据.csv", "summary": "本月订单共 42 条"}},
		},
	}
	descriptions := historicalNonImageMediaDescriptions(event.Segments)
	text := agentImageHistoryPromptTextWithDescriptions(event, 0, descriptions)
	for _, want := range []string{
		"audio_count=1",
		"file_count=2",
		"语音1转写=明天下午三点开会",
		"文件1摘要=文件名：会议纪要.pdf；格式：pdf；正文尚未解析",
		"文件2摘要=文件名：数据.csv；格式：csv；内容摘要：本月订单共 42 条",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("history media line missing %q: %q", want, text)
		}
	}
}

func TestEverySupportedFileFormatGetsHistoryMetadataDescription(t *testing.T) {
	for extension := range supportedFileExts {
		name := "fixture" + extension
		lines := historicalNonImageMediaDescriptions([]MessageSegment{{Type: "file", Data: map[string]string{"name": name}}})
		if len(lines) != 1 || !strings.Contains(lines[0], "文件名："+name) || !strings.Contains(lines[0], "正文尚未解析") {
			t.Fatalf("supported file %q description = %#v", extension, lines)
		}
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
