// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
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

// 同一条历史在相邻两轮里必须渲染成同一个字符串，否则整段历史无法命中前缀缓存。
func TestHistoryLineIsByteStableAcrossTurns(t *testing.T) {
	event := MessageEvent{
		Time:       1000,
		SenderName: "轩诺",
		MessageID:  "m-1",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "旧问题"}}},
	}
	first := historyPromptTextAt(event, 1100)
	second := historyPromptTextAt(event, 90000)
	if first != second {
		t.Fatalf("history line drifted between turns: %q vs %q", first, second)
	}
	want := "[历史 " + time.Unix(1000, 0).Local().Format("2006-01-02 15:04:05") + "] 轩诺: 旧问题"
	if first != want {
		t.Fatalf("history line = %q, want %q", first, want)
	}
	if strings.Contains(first, "距当前") {
		t.Fatalf("history line must not carry request-relative timing: %q", first)
	}
	cross := event
	cross.crossGroupContext = true
	if got := historyPromptTextAt(cross, 1100); !strings.HasPrefix(got, "[跨群历史 ") {
		t.Fatalf("cross-group history line = %q", got)
	}
	media := agentImageHistoryPromptTextWithDescriptions(MessageEvent{
		Time:       1000,
		SenderName: "轩诺",
		MessageID:  "m-2",
		Segments:   []MessageSegment{{Type: "image", Data: map[string]string{}}},
	}, 1100, nil)
	if !strings.HasPrefix(media, "[历史 ") || strings.Contains(media, "距当前") {
		t.Fatalf("media history line = %q", media)
	}
}

// 真机跑一轮 QQ 群聊时发现：探针把所有 system 消息合成一段，于是每条消息都报
// 「system 分叉、可复用前缀 0 字节」，而实际缓存命中率有九成。开头的 system 才是
// 稳定段，靠后的（发言者尾部、实时时钟）本来就每轮变，要按位置进消息序列。
func TestPromptCacheProbeIgnoresExpectedTailChurn(t *testing.T) {
	build := func(clock string) llm.GenerateRequest {
		return llm.GenerateRequest{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "稳定人设与规则"},
			{Role: llm.RoleUser, Content: "[历史 2026-09-03 10:00:00] 小林: 旧问题"},
			{Role: llm.RoleAssistant, Content: "旧回复", CacheBreakpoint: true},
			{Role: llm.RoleSystem, Content: "关系等级：熟悉"},
			{Role: llm.RoleSystem, Content: clock},
			{Role: llm.RoleUser, Content: "【当前需要回复的消息】新问题"},
		}}
	}
	first := observePromptCachePayload(build("当前运行时钟：10:00:00"))
	second := observePromptCachePayload(build("当前运行时钟：10:00:31"))
	if first.SystemHash != second.SystemHash {
		t.Fatalf("leading system must stay stable across turns")
	}
	if first.StableBoundary != 1 {
		t.Fatalf("stable boundary = %d, want the marked history message", first.StableBoundary)
	}
	divergence := comparePromptCachePayload(first, second)
	if divergence.Segment != "messages" || divergence.MessageIndex <= first.StableBoundary {
		t.Fatalf("tail churn must be reported after the stable boundary: %#v", divergence)
	}
	if !divergence.Expected(first) {
		t.Fatalf("tail churn must not be logged as a cache problem: %#v", divergence)
	}
	if divergence.ReusablePrefixBytes <= 0 {
		t.Fatalf("reusable prefix must cover the stable head and history: %#v", divergence)
	}

	// 真正的前缀断裂仍然要报：历史被改写时命中率是真的会掉。
	broken := build("当前运行时钟：10:00:31")
	broken.Messages[1].Content = "[历史 2026-09-03 10:00:00] 小林: 改写过的旧问题"
	real := comparePromptCachePayload(first, observePromptCachePayload(broken))
	if real.Segment != "messages" || real.MessageIndex != 0 || real.Expected(first) {
		t.Fatalf("a real history rewrite must still be reported: %#v", real)
	}
	// 头部变了同样要报。
	headChanged := build("当前运行时钟：10:00:31")
	headChanged.Messages[0].Content = "换了人设"
	if got := comparePromptCachePayload(first, observePromptCachePayload(headChanged)); got.Segment != "system" || got.Expected(first) {
		t.Fatalf("a system head change must still be reported: %#v", got)
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
		"【媒体 message_id=video-message-1：视频×1、视频关键帧×1】",
		"视频关键帧1摘要=桌面上放着一台笔记本电脑",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("video history line missing %q: %q", want, text)
		}
	}
	// 为零的种类和工具调用说明不再逐行重复：那些在 system 头部只说一次。
	for _, stale := range []string{"语音", "文件", "count=", dianaHistoryImagesToolName, "当前未附加"} {
		if strings.Contains(text, stale) {
			t.Fatalf("video history line carries boilerplate %q: %q", stale, text)
		}
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
	if !strings.Contains(bare, "【媒体 message_id=image-message-1：图片×1】") {
		t.Fatalf("bare line = %q", bare)
	}
	if strings.Contains(bare, "报纸与干花") {
		t.Fatalf("bare line should not invent a description: %q", bare)
	}

	described := agentImageHistoryPromptTextWithDescriptions(event, 1120, []string{"图片1摘要=报纸与干花拼贴的二次元人物"})
	if !strings.Contains(described, "图片×1") {
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
		"【媒体 message_id=media-message-1：语音×1、文件×2】",
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
