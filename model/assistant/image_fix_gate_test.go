// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

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

type imageFixGateProvider struct {
	mu    sync.Mutex
	reply string
	calls int
	input string
}

func (p *imageFixGateProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.input = req.Messages[len(req.Messages)-1].Content
	return &llm.GenerateResponse{Text: p.reply}, nil
}

// 判为纠正时交出更正和那张图的消息；指到没给它看过的消息时不认。
func TestImageFixGateDetectsCorrectionOnCandidateImages(t *testing.T) {
	imagePath, hash := writeRecallImageFixture(t)
	store := newRecallImageTestStore()
	store.descriptions[hash] = ImageDescriptionRecord{ContentSHA256: hash, Description: "《蔚蓝档案》中的白洲梓", Source: "vision"}
	provider := &imageFixGateProvider{reply: `{"fix":"这是舞萌的百合咲，不是白洲梓","message_id":"30009"}`}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetMessageHistoryStore(store)
	event := imageFixTestEvent(imagePath)

	fix, messageID := runtime.detectImageFix(context.Background(), event, store)
	if fix != "这是舞萌的百合咲，不是白洲梓" || messageID != "30009" {
		t.Fatalf("fix=%q message_id=%q", fix, messageID)
	}
	if !strings.Contains(provider.input, "白洲梓") || !strings.Contains(provider.input, "这是舞萌的百合咲") {
		t.Fatalf("the gate must see the description and the message: %s", provider.input)
	}

	// 只有一张候选图时，模型填了别的消息 ID 也落到这张上；判为不是纠正就什么都不改。
	provider.reply = `{"fix":"这是舞萌的百合咲","message_id":"99999"}`
	if _, messageID := runtime.detectImageFix(context.Background(), event, store); messageID != "30009" {
		t.Fatalf("single candidate should be used, got %q", messageID)
	}
	provider.reply = `{"fix":"","message_id":""}`
	if fix, _ := runtime.detectImageFix(context.Background(), event, store); fix != "" {
		t.Fatalf("no correction expected, got %q", fix)
	}
}

// 近期没有带缓存描述的图时不发请求。
func TestImageFixGateSkipsWithoutDescribedImages(t *testing.T) {
	imagePath, _ := writeRecallImageFixture(t)
	store := newRecallImageTestStore()
	provider := &imageFixGateProvider{reply: `{"fix":"x","message_id":"30009"}`}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetMessageHistoryStore(store)
	if fix, _ := runtime.detectImageFix(context.Background(), imageFixTestEvent(imagePath), store); fix != "" || provider.calls != 0 {
		t.Fatalf("fix=%q calls=%d", fix, provider.calls)
	}
}
