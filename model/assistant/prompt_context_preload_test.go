// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestPromptContextPreloadFetchesLayersConcurrently(t *testing.T) {
	memory := &testStructuredMemoryStore{items: []StructuredMemoryItem{
		{
			Key: ThreadMemoryKey("group:group-1"), Kind: MemoryKindThread,
			Topic: "会话状态", Content: "正在排查上下文变短",
		},
		{
			ID: "mem-1", Key: "instruction.reply_style.emoji", Kind: MemoryKindInstruction,
			Topic: "回复风格", Content: "回复时句尾带颜文字", SubjectUserID: "u2",
			Confidence: 0.98, Importance: 0.8, Visibility: MemoryVisibilityUser,
		},
	}}
	runtime := mediaGateRuntime(t,
		gateImageEvent("img-old", 1),
		gateTextEvent("m2", 2, "第二条"),
		gateTextEvent("m3", 3, "第三条"),
	)
	runtime.SetStructuredMemoryStore(memory)
	event := gateCurrentEvent()
	event.UserID = "u2"

	preload := runtime.startPromptContextPreload(context.Background(), event, "哈哈哈",
		UserMemoryProfile{UserID: "u2", DisplayName: "当前用户"}, RelationshipPolicy{Name: "熟人", Tone: "自然"}, true)
	preload.wait()

	if !strings.Contains(preload.sessionThread, "排查上下文变短") {
		t.Fatalf("session thread = %q", preload.sessionThread)
	}
	if !strings.Contains(preload.memoryContext, "颜文字") {
		t.Fatalf("memory context = %q", preload.memoryContext)
	}
	if !strings.Contains(preload.mediaIndex, "message_id=img-old") {
		t.Fatalf("media index = %q", preload.mediaIndex)
	}
}

func TestPromptContextPreloadSkipsMediaIndexWhenAgentCannotFetch(t *testing.T) {
	runtime := mediaGateRuntime(t,
		gateImageEvent("img-old", 1),
		gateTextEvent("m2", 2, "第二条"),
		gateTextEvent("m3", 3, "第三条"),
	)
	preload := runtime.startPromptContextPreload(context.Background(), gateCurrentEvent(), "哈哈哈",
		UserMemoryProfile{UserID: "u2"}, RelationshipPolicy{Name: "熟人"}, false)
	preload.wait()

	// 取不到原图的部署走的是前置指代解析，索引白算一次就是白花的开销。
	if preload.mediaIndex != "" {
		t.Fatalf("media index was built without tool access: %q", preload.mediaIndex)
	}
}

func TestPromptContextPreloadWaitIsSafeOnNil(t *testing.T) {
	var preload *promptContextPreload
	preload.wait()
}
