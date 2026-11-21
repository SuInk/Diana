// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
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

func TestCodeModeToolIsResident(t *testing.T) {
	if !slices.Contains(replyAgentCoreTools, agent.CodeModeToolName) {
		t.Fatalf("Code Mode missing from resident tools: %v", replyAgentCoreTools)
	}
}

type imageFixVisionProvider struct {
	mu          sync.Mutex
	instruction string
	fail        bool
}

func (p *imageFixVisionProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(req.Messages) == 2 {
		p.instruction = req.Messages[1].Content
	}
	if p.fail {
		return nil, errors.New("vision unavailable")
	}
	return &llm.GenerateResponse{Text: "Q 版白发角色头像，据群友更正是舞萌的百合咲。"}, nil
}

func imageFixTestEvent(imagePath string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "12345", UserID: "10001", MessageID: "30010",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "这是舞萌的百合咲"}}},
		Quoted: &QuotedMessage{
			MessageID: "30009", UserID: "10002",
			Segments: []MessageSegment{{Type: "image", Data: map[string]string{"cached_file": imagePath}}},
		},
	}
}

// 群友纠正后，被引用那张图的缓存描述按更正重写，视觉模型拿到的要求里带着这条更正。
func TestImageFixRewritesCachedDescription(t *testing.T) {
	imagePath, hash := writeRecallImageFixture(t)
	store := newRecallImageTestStore()
	store.descriptions[hash] = ImageDescriptionRecord{ContentSHA256: hash, Description: "《蔚蓝档案》中的白洲梓", Source: "vision"}
	provider := &imageFixVisionProvider{}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetMessageHistoryStore(store)

	if fixed := runtime.rewriteImageDescriptions(context.Background(), imageFixTestEvent(imagePath), store, "这是舞萌的百合咲，不是白洲梓", "30009"); fixed != 1 {
		t.Fatalf("fixed = %d, want 1", fixed)
	}
	record := store.descriptions[hash]
	if !strings.Contains(record.Description, "百合咲") || strings.Contains(record.Description, "白洲梓") || record.Source != "correction" {
		t.Fatalf("record = %#v", record)
	}
	if !strings.Contains(provider.instruction, "这是舞萌的百合咲，不是白洲梓") {
		t.Fatalf("vision instruction lost the correction: %s", provider.instruction)
	}
}

// 视觉模型重写失败时，在原描述前面记下更正，不能让错的描述原样留着。
func TestImageFixFallsBackToPrefixWhenVisionFails(t *testing.T) {
	imagePath, hash := writeRecallImageFixture(t)
	store := newRecallImageTestStore()
	store.descriptions[hash] = ImageDescriptionRecord{ContentSHA256: hash, Description: "《蔚蓝档案》中的白洲梓", Source: "vision"}
	provider := &imageFixVisionProvider{fail: true}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetMessageHistoryStore(store)

	if fixed := runtime.rewriteImageDescriptions(context.Background(), imageFixTestEvent(imagePath), store, "这是舞萌的百合咲", "30009"); fixed != 1 {
		t.Fatalf("fixed = %d, want 1", fixed)
	}
	record := store.descriptions[hash]
	if !strings.HasPrefix(record.Description, "【群友更正：这是舞萌的百合咲】") || !strings.Contains(record.Description, "白洲梓") {
		t.Fatalf("record = %#v", record)
	}
}
