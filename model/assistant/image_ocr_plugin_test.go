// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type imageOCRFakeProvider struct {
	calls    atomic.Int32
	lastReq  llm.GenerateRequest
	response string
	err      error
}

func (p *imageOCRFakeProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls.Add(1)
	p.lastReq = req
	if p.err != nil {
		return nil, p.err
	}
	return &llm.GenerateResponse{Text: p.response}, nil
}

func newImageOCRTestRuntime(t *testing.T, provider LLMProvider, settings map[string]any) *Runtime {
	t.Helper()
	manager := NewPluginManager(NewImageOCRPlugin())
	if settings != nil {
		if _, err := manager.UpdateSettings(imageOCRPluginID, settings); err != nil {
			t.Fatal(err)
		}
	}
	return NewRuntime(BotConfig{}, nil, manager, nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
}

func imageOCRTestMessage(imageURLs ...string) llm.Message {
	parts := []llm.ContentPart{{Type: llm.ContentPartText, Text: "看看这张图"}}
	for _, imageURL := range imageURLs {
		parts = append(parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: imageURL, Detail: "high"})
	}
	return llm.Message{Role: llm.RoleUser, Content: "看看这张图", Parts: parts}
}

// 每张图多一次视觉调用，必须默认关闭。
func TestImageOCRDisabledByDefault(t *testing.T) {
	provider := &imageOCRFakeProvider{response: "不该被调用"}
	runtime := newImageOCRTestRuntime(t, provider, nil)

	notice := runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage("data:image/jpeg;base64,AAA"))
	if notice != "" {
		t.Fatalf("notice = %q, want empty", notice)
	}
	if provider.calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls.Load())
	}
}

func TestImageOCRTranscribesAndCaches(t *testing.T) {
	provider := &imageOCRFakeProvider{response: "第一行文字\n第二行文字"}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{"backend": imageOCRBackendLLM, "model": "qwen-vl-ocr"})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "1"}
	message := imageOCRTestMessage("data:image/jpeg;base64,AAA")

	notice := runtime.imageOCRContextText(context.Background(), event, message)
	if !strings.Contains(notice, "图片文字转写") || !strings.Contains(notice, "第一行文字\n第二行文字") {
		t.Fatalf("notice = %q", notice)
	}
	// 单图不加「第 N 张图」前缀。
	if strings.Contains(notice, "第 1 张图") {
		t.Fatalf("single image should not be numbered: %q", notice)
	}
	if provider.lastReq.Model != "qwen-vl-ocr" {
		t.Fatalf("model override = %q", provider.lastReq.Model)
	}
	// 转写请求必须带上图片本身。
	foundImage := false
	for _, msg := range provider.lastReq.Messages {
		for _, part := range msg.Parts {
			if part.Type == llm.ContentPartImageURL && part.ImageURL == "data:image/jpeg;base64,AAA" {
				foundImage = true
			}
		}
	}
	if !foundImage {
		t.Fatalf("transcription request missed the image: %#v", provider.lastReq.Messages)
	}

	// 同一张图第二次进上下文（引用、追问）直接吃缓存，不再烧视觉调用。
	if again := runtime.imageOCRContextText(context.Background(), event, message); again != notice {
		t.Fatalf("cached notice = %q, want %q", again, notice)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls.Load())
	}
}

func TestImageOCRNumbersMultipleImagesAndCapsCount(t *testing.T) {
	provider := &imageOCRFakeProvider{response: "一些文字"}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{"backend": imageOCRBackendLLM, "max_images": 2})
	message := imageOCRTestMessage("data:a", "data:b", "data:c")

	notice := runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, message)
	if !strings.Contains(notice, "第 1 张图") || !strings.Contains(notice, "第 2 张图") {
		t.Fatalf("notice = %q", notice)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2 (capped)", provider.calls.Load())
	}
}

// 图片没有文字时不要往上下文里塞一个空转写块。
func TestImageOCRSkipsNoTextImages(t *testing.T) {
	provider := &imageOCRFakeProvider{response: imageOCRNoTextMarker}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{"backend": imageOCRBackendLLM})

	notice := runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage("data:x"))
	if notice != "" {
		t.Fatalf("notice = %q, want empty", notice)
	}
	// 「无文字」结论要缓存，别对同一张表情包反复确认。
	_ = runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage("data:x"))
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls.Load())
	}
}

// 转写失败只是少了辅助信息，不缓存失败、下次重试，也绝不阻断回复。
func TestImageOCRFailureIsNonFatalAndNotCached(t *testing.T) {
	provider := &imageOCRFakeProvider{err: errors.New("vision backend down")}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{"backend": imageOCRBackendLLM})
	message := imageOCRTestMessage("data:y")

	if notice := runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, message); notice != "" {
		t.Fatalf("notice = %q, want empty", notice)
	}
	provider.err = nil
	provider.response = "恢复后的文字"
	notice := runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, message)
	if !strings.Contains(notice, "恢复后的文字") {
		t.Fatalf("notice = %q", notice)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls.Load())
	}
}

func TestImageOCRRespectsPrivateToggle(t *testing.T) {
	provider := &imageOCRFakeProvider{response: "私聊文字"}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{"backend": imageOCRBackendLLM, "private_enabled": false})

	notice := runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "1"}, imageOCRTestMessage("data:z"))
	if notice != "" || provider.calls.Load() != 0 {
		t.Fatalf("notice = %q calls = %d", notice, provider.calls.Load())
	}
}
