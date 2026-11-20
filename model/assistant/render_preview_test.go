// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 预览图只附给下一轮模型、取一次就清；带 expect 时另请视觉模型挑毛病，到了轮数上限
// 就让模型照实发出去，不再无限改下去。
func TestRenderPreviewReviewsAndStopsAfterMaxRounds(t *testing.T) {
	provider := &capturingLLMProvider{reply: "1. 左脚没踩在踏板上"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	previewer := &renderPreviewer{}
	event := MessageEvent{MessageID: "30004"}
	png := []byte("\x89PNG\r\n\x1a\n")

	review, message := previewer.preview(context.Background(), runtime, event, png, "狗熊骑自行车，脚踩踏板", false)
	if review != "1. 左脚没踩在踏板上" || !strings.Contains(message, "按关键点重写") {
		t.Fatalf("first round should carry the review and ask for a structural rewrite: %q / %q", review, message)
	}
	if parts := previewer.ToolResultParts(""); len(parts) != 1 || !strings.HasPrefix(parts[0].ImageURL, "data:image/png;base64,") {
		t.Fatalf("preview image should be attached to the next turn: %#v", parts)
	}
	if parts := previewer.ToolResultParts(""); len(parts) != 0 {
		t.Fatal("preview image is attached only once")
	}
	if _, message = previewer.preview(context.Background(), runtime, event, png, "狗熊骑自行车，脚踩踏板", false); !strings.Contains(message, "不要再预览") || !strings.Contains(message, "照实说") {
		t.Fatalf("after the last round the model should send and say what is still wrong: %q", message)
	}

	if review, message = previewer.preview(context.Background(), runtime, event, png, "", false); review != "" || strings.Contains(message, "review 是") {
		t.Fatalf("a requested preview without expect is not reviewed: %q / %q", review, message)
	}
}

// SVG 图片一轮里第一次渲染由运行时强制预览，没写 expect 也照样检查；之后才真的发。
// 两个渲染工具共用一份状态，用 render 预览过，再用 render_media 发也不会被拦第二次。
func TestRenderPreviewForcedOnFirstSVGImage(t *testing.T) {
	provider := &capturingLLMProvider{reply: "1. 车座被身体挡住"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	render := newDianaRenderTool(runtime, MessageEvent{MessageID: "30005"})
	media := newDianaRenderMediaTool(runtime, MessageEvent{MessageID: "30005"}, nil, RelationshipPolicy{})
	media.renderPreviewer = render.renderPreviewer

	if preview, forced := render.wantPreview(false, map[string]any{}); preview || forced {
		t.Fatal("markdown and mermaid are sent directly")
	}
	preview, forced := render.wantPreview(true, map[string]any{"preview": false})
	if !preview || !forced {
		t.Fatal("the first SVG image must be previewed even when the model asks to send")
	}
	review, message := render.preview(context.Background(), runtime, render.event, []byte("\x89PNG"), "", forced)
	if review != "1. 车座被身体挡住" || !strings.HasPrefix(message, "SVG 第一次渲染先给你看") {
		t.Fatalf("forced preview should be reviewed without expect: %q / %q", review, message)
	}
	if strings.Contains(renderPreviewReviewQuestion(""), "应当是") {
		t.Fatal("without expect the reviewer only looks for drawing mistakes")
	}
	if preview, _ := media.wantPreview(true, map[string]any{}); preview {
		t.Fatal("after one preview the next SVG render is sent")
	}
	if preview, forced := media.wantPreview(true, map[string]any{"preview": true}); !preview || forced {
		t.Fatal("an explicit preview request still previews")
	}
}

// 两个渲染工具都收 preview/expect，按需目录截的说明前 120 字里要看得到预览用法。
func TestRenderToolsOfferPreview(t *testing.T) {
	for _, tool := range []interface {
		Description() string
		InputSchema() map[string]any
	}{&dianaRenderTool{}, &dianaRenderMediaTool{}} {
		properties := tool.InputSchema()["properties"].(map[string]any)
		if properties["preview"] == nil || properties["expect"] == nil {
			t.Fatalf("%T schema lacks preview/expect", tool)
		}
		if head := agent.CompactToolDescription(tool.Description(), agent.SystemPromptToolDescriptionBudget); !strings.Contains(head, "第一次渲染只给你看") || !strings.Contains(head, "改好再调一次才发出去") {
			t.Fatalf("%T preview guidance must fit the catalog budget: %q", tool, head)
		}
	}
}
