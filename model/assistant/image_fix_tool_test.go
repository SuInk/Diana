// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// 主模型调用 image_fix 后立刻返回，被指到那张图的缓存描述在后台按更正重写。
func TestImageFixToolRewritesDescriptionInBackground(t *testing.T) {
	imagePath, hash := writeRecallImageFixture(t)
	store := newRecallImageTestStore()
	store.descriptions[hash] = ImageDescriptionRecord{ContentSHA256: hash, Description: "《蔚蓝档案》中的白洲梓", Source: "vision"}
	provider := &imageFixVisionProvider{}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetMessageHistoryStore(store)

	tool := newDianaImageFixTool(runtime, imageFixTestEvent(imagePath))
	if _, err := tool.Run(context.Background(), map[string]any{"message_id": "30009", "fix": "这是舞萌的百合咲，不是白洲梓"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		record, _, _ := store.GetImageDescription(context.Background(), hash)
		if record.Source == "correction" {
			if !strings.Contains(record.Description, "百合咲") {
				t.Fatalf("record = %#v", record)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("description was not rewritten: %#v", record)
		}
		time.Sleep(10 * time.Millisecond)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if !strings.Contains(provider.instruction, "这是舞萌的百合咲，不是白洲梓") {
		t.Fatalf("vision instruction lost the correction: %s", provider.instruction)
	}
}

// 指到没有图的消息、或者没填更正时直接报错，让模型改参数，不在后台白跑一次视觉模型。
func TestImageFixToolRejectsMessagesWithoutImages(t *testing.T) {
	imagePath, _ := writeRecallImageFixture(t)
	store := newRecallImageTestStore()
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	tool := newDianaImageFixTool(runtime, imageFixTestEvent(imagePath))
	for _, input := range []map[string]any{
		{"message_id": "99999", "fix": "这是舞萌的百合咲"},
		{"message_id": "30009", "fix": " "},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("input %v should be rejected", input)
		}
	}
	if store.saves != 0 {
		t.Fatalf("saves = %d, want 0", store.saves)
	}
}

// 要在模型口头认错的同一步被调用，所以和收尾一起常驻，不进按需目录。
func TestImageFixToolIsResident(t *testing.T) {
	if !slices.Contains(replyAgentCoreTools, dianaImageFixToolName) {
		t.Fatalf("replyAgentCoreTools = %v", replyAgentCoreTools)
	}
}
