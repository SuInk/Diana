// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

// 当前消息注解的覆盖要真的进到拼好的当前消息里，占位符换成实际数字。
func TestPromptNoteOverridesReachCurrentMessage(t *testing.T) {
	event := MessageEvent{
		Kind: EventKindGroup, SelfID: "42", GroupID: "g", UserID: "10001",
		Segments: []MessageSegment{{Type: "at", Data: map[string]string{"qq": "10002"}}, {Type: "text", Data: map[string]string{"text": "看看这几条"}}},
	}
	sources := semanticReferenceContext{RequestedSourceCount: 3, TextSourceCount: 2, MissingSourceCount: 1}

	defaults := currentPromptTextWithSemanticContext(event, "看看这几条", sources, promptAnnotation{})
	for _, want := range []string{promptNoteAtOtherSpec.Default, "语义指代已定位到 3 条历史来源，其中 2 条包含文字", "其中 1 条来源未能从持久化历史解析"} {
		if !strings.Contains(defaults, want) {
			t.Fatalf("零值注解没有渲染默认文案 %q：\n%s", want, defaults)
		}
	}

	overrides := PromptOverrides{
		promptNoteAtOtherSpec.Key:        "别漏了那个 @。",
		promptNoteSourcesTextSpec.Key:    "共 {sources} 条来源，{text_sources} 条有字。",
		promptNoteSourcesMissingSpec.Key: "缺 {missing} 条。",
	}
	got := currentPromptTextWithSemanticContext(event, "看看这几条", sources, promptAnnotation{Overrides: overrides})
	if !strings.Contains(got, "\n\n别漏了那个 @。") || strings.Contains(got, promptNoteAtOtherSpec.Default) {
		t.Fatalf("@ 注解的覆盖没有生效：\n%s", got)
	}
	if !strings.Contains(got, "\n\n共 3 条来源，2 条有字。缺 1 条。") {
		t.Fatalf("来源注解的占位符没有渲染：\n%s", got)
	}
}

// 同轮补充和只发图这两处不走 promptAnnotation，覆盖分别从参数和 ctx 上取。
func TestPromptNoteOverridesReachSupplementAndImageOnly(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, UserID: "10001", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "再补一句"}}}}
	supplement := proactiveTurnPromptTextAt(event, "", 0, PromptOverrides{promptNoteSupplementSpec.Key: "和下一条一起答"})
	if !strings.HasPrefix(supplement, "【当前同轮补充消息，和下一条一起答】") {
		t.Fatalf("同轮补充的覆盖没有生效：%s", supplement)
	}

	image := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	photos := MessageEvent{Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": image}}, {Type: "image", Data: map[string]string{"url": image + "="}}}}
	ctx := withPromptOverrides(context.Background(), PromptOverrides{promptNoteImageOnlyMultiSpec.Key: "看这 {images} 张"})
	message, _ := llmMessageFromEventWithImagesForContextDiagnostics(ctx, photos, "", nil)
	if message.Content != "看这 2 张" {
		t.Fatalf("只发多张图的覆盖没有生效：%q", message.Content)
	}
}
