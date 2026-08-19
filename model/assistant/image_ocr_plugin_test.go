// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	manager := NewPluginManager(NewImageOCRPlugin(nil))
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

// 本地后端完全离线：不碰任何 LLM provider，把图片写成临时文件交给本地命令。
func TestImageOCRLocalBackendRunsCommandOffline(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-tesseract")
	// 桩脚本按 tesseract 约定收 <file> stdout -l <langs>，校验文件真实存在。
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntest -s \"$1\" || exit 3\ntest \"$2\" = stdout || exit 4\necho \"本地识别的文字\"\necho \"警告信息\" >&2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	provider := &imageOCRFakeProvider{response: "不该被调用"}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{"backend": imageOCRBackendLocal, "local_command": script, "local_languages": "chi_sim+eng"})

	imageURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("fake-png-bytes"))
	notice := runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage(imageURL))
	if !strings.Contains(notice, "本地识别的文字") {
		t.Fatalf("notice = %q", notice)
	}
	// stderr 的警告不能混进转写文本。
	if strings.Contains(notice, "警告信息") {
		t.Fatalf("stderr leaked into transcript: %q", notice)
	}
	if provider.calls.Load() != 0 {
		t.Fatalf("llm provider calls = %d, want 0 (offline)", provider.calls.Load())
	}
	// 缓存同样生效。
	_ = runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage(imageURL))
}

// 不是 data URL 的图片本地后端解不出字节，跳过且不影响其他图。
func TestImageOCRLocalBackendSkipsNonDataURL(t *testing.T) {
	provider := &imageOCRFakeProvider{}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{"backend": imageOCRBackendLocal, "local_command": "/nonexistent-ocr"})

	notice := runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage("https://example.com/a.png"))
	if notice != "" {
		t.Fatalf("notice = %q, want empty", notice)
	}
}

// HTTP 后端调自托管传统 OCR 服务：PaddleOCR hub serving 风格的响应要能解出
// 全部文字行，且全程不碰 LLM provider。
func TestImageOCRHTTPBackendParsesPaddleStyleResponse(t *testing.T) {
	var gotAuth string
	var gotImages int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		var payload struct {
			Images []string `json:"images"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotImages = len(payload.Images)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[[{"text":"第一行","confidence":0.99},{"text":"第二行","confidence":0.98}]],"status":"000"}`))
	}))
	defer server.Close()

	provider := &imageOCRFakeProvider{response: "不该被调用"}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{
		"backend": imageOCRBackendHTTP, "http_endpoint": server.URL, "http_api_key": "paddle-key",
	})
	imageURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png-bytes"))

	notice := runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage(imageURL))
	if !strings.Contains(notice, "第一行\n第二行") {
		t.Fatalf("notice = %q", notice)
	}
	if gotAuth != "Bearer paddle-key" || gotImages != 1 {
		t.Fatalf("auth = %q images = %d", gotAuth, gotImages)
	}
	if provider.calls.Load() != 0 {
		t.Fatalf("llm provider calls = %d, want 0", provider.calls.Load())
	}
}

// RapidOCR 风格（rec_txt 字段）的响应同样能解。
func TestImageOCRHTTPBackendParsesRecTxtFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"rec_txt":"识别行A"},{"rec_txt":"识别行B"}]}`))
	}))
	defer server.Close()

	runtime := newImageOCRTestRuntime(t, &imageOCRFakeProvider{}, map[string]any{
		"backend": imageOCRBackendHTTP, "http_endpoint": server.URL,
	})
	imageURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("jpg"))
	notice := runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage(imageURL))
	if !strings.Contains(notice, "识别行A\n识别行B") {
		t.Fatalf("notice = %q", notice)
	}
}

// 服务返回空结果按「无可辨文字」处理并缓存，不往上下文塞空块。
func TestImageOCRHTTPBackendEmptyResultMeansNoText(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[[]]}`))
	}))
	defer server.Close()

	runtime := newImageOCRTestRuntime(t, &imageOCRFakeProvider{}, map[string]any{
		"backend": imageOCRBackendHTTP, "http_endpoint": server.URL,
	})
	imageURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("blank"))
	message := imageOCRTestMessage(imageURL)
	if notice := runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, message); notice != "" {
		t.Fatalf("notice = %q, want empty", notice)
	}
	_ = runtime.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, message)
	if calls.Load() != 1 {
		t.Fatalf("service calls = %d, want 1 (cached)", calls.Load())
	}
}
