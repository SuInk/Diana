// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 预览图只附给下一轮模型、取一次就清；预览都另请视觉模型挑毛病，到了轮数上限就让模型
// 照实发出去，不再无限改下去。
func TestRenderPreviewReviewsAndStopsAfterMaxRounds(t *testing.T) {
	provider := &capturingLLMProvider{reply: "1. 左脚没踩在踏板上"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	previewer := &renderPreviewer{}
	event := MessageEvent{MessageID: "30004"}
	png := []byte("\x89PNG\r\n\x1a\n")

	review, message := previewer.preview(context.Background(), runtime, event, png, "狗熊骑自行车，脚踩踏板")
	if review != "1. 左脚没踩在踏板上" || !strings.Contains(message, "按关键点重写") {
		t.Fatalf("first round should carry the review and ask for a structural rewrite: %q / %q", review, message)
	}
	if parts := previewer.ToolResultParts(""); len(parts) != 1 || !strings.HasPrefix(parts[0].ImageURL, "data:image/png;base64,") {
		t.Fatalf("preview image should be attached to the next turn: %#v", parts)
	}
	if parts := previewer.ToolResultParts(""); len(parts) != 0 {
		t.Fatal("preview image is attached only once")
	}
	if _, message = previewer.preview(context.Background(), runtime, event, png, "狗熊骑自行车，脚踩踏板"); !strings.Contains(message, "不要再预览") || !strings.Contains(message, "照实说") {
		t.Fatalf("after the last round the model should send and say what is still wrong: %q", message)
	}
}

// 预览是可选的：不传 preview 照常发出，传了才只给模型看；没写 expect 也照样检查。两个
// 渲染工具共用一份检查轮数。
func TestRenderPreviewOnlyWhenRequested(t *testing.T) {
	provider := &capturingLLMProvider{reply: "1. 车座被身体挡住"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	render := newDianaRenderTool(runtime, MessageEvent{MessageID: "30005"})
	media := newDianaRenderMediaTool(runtime, MessageEvent{MessageID: "30005"}, nil, RelationshipPolicy{})
	media.renderPreviewer = render.renderPreviewer

	if wantPreview(map[string]any{}) || wantPreview(map[string]any{"preview": false}) {
		t.Fatal("without preview=true the render is sent directly")
	}
	if !wantPreview(map[string]any{"preview": true}) {
		t.Fatal("preview=true previews")
	}
	review, _ := render.preview(context.Background(), runtime, render.event, []byte("\x89PNG"), "")
	if review != "1. 车座被身体挡住" {
		t.Fatalf("a preview without expect is still reviewed: %q", review)
	}
	if strings.Contains(renderPreviewReviewQuestion(""), "应当是") {
		t.Fatal("without expect the reviewer only looks for drawing mistakes")
	}
	if _, message := media.preview(context.Background(), runtime, media.event, []byte("\x89PNG"), ""); !strings.Contains(message, "不要再预览") {
		t.Fatalf("both tools share the round count: %q", message)
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
		if head := agent.CompactToolDescription(tool.Description(), agent.SystemPromptToolDescriptionBudget); !strings.Contains(head, "preview=true 预览") || !strings.Contains(head, "改好再不带 preview 发出去") {
			t.Fatalf("%T preview guidance must fit the catalog budget: %q", tool, head)
		}
	}
}
