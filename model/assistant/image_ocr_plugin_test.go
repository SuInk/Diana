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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type imageOCRFakeProvider struct {
	calls   atomic.Int32
	lastReq llm.GenerateRequest
	// responses 按调用顺序逐个返回（仅文字模式下先描述后转写）；耗尽后回落到 response。
	responses []string
	response  string
	err       error
}

func (p *imageOCRFakeProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	n := int(p.calls.Add(1))
	p.lastReq = req
	if p.err != nil {
		return nil, p.err
	}
	if n <= len(p.responses) {
		return &llm.GenerateResponse{Text: p.responses[n-1]}, nil
	}
	return &llm.GenerateResponse{Text: p.response}, nil
}

func newImageOCRTestRuntime(t *testing.T, provider LLMProvider, settings map[string]any) *Runtime {
	t.Helper()
	return newImageOCRTestRuntimeWithStore(t, provider, settings, nil)
}

func newImageOCRTestRuntimeWithStore(t *testing.T, provider LLMProvider, settings map[string]any, store MessageHistoryStore) *Runtime {
	t.Helper()
	manager := NewPluginManager(NewImageOCRPlugin(nil))
	if settings != nil {
		if _, err := manager.UpdateSettings(imageOCRPluginID, settings); err != nil {
			t.Fatal(err)
		}
	}
	runtime := NewRuntime(BotConfig{}, nil, manager, nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	if store != nil {
		runtime.SetMessageHistoryStore(store)
	}
	return runtime
}

// 按内容哈希缓存识别结果的最小存储，模拟 SQLite 那层。
type fakeImageRecognitionStore struct {
	semanticTimelineStore
	mu      sync.Mutex
	records map[string]ImageRecognitionRecord
	loads   int
}

func newFakeImageRecognitionStore() *fakeImageRecognitionStore {
	return &fakeImageRecognitionStore{
		semanticTimelineStore: semanticTimelineStore{events: map[string][]MessageEvent{}},
		records:               map[string]ImageRecognitionRecord{},
	}
}

func (s *fakeImageRecognitionStore) LoadImageRecognition(_ context.Context, cacheKey string) (ImageRecognitionRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	record, ok := s.records[cacheKey]
	return record, ok, nil
}

func (s *fakeImageRecognitionStore) SaveImageRecognition(_ context.Context, record ImageRecognitionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[record.CacheKey] = record
	return nil
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

func llmMessageHasImages(message llm.Message) bool {
	for _, part := range message.Parts {
		if part.Type == llm.ContentPartImageURL {
			return true
		}
	}
	return false
}

// 仅文字模式（LLM 描述 + OCR 组合）：图片从消息里摘掉，换成画面描述加转写文
// 本，消息不再含图片，也就不会再路由到 vision 分组。
func TestImageOCRTextOnlyCombinesDescriptionAndTranscript(t *testing.T) {
	provider := &imageOCRFakeProvider{responses: []string{"一张对话截图", "你好，在吗？"}}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{
		"backend": imageOCRBackendLLM, "delivery": imageOCRDeliveryText,
	})
	message := imageOCRTestMessage("data:image/png;base64,AAA")

	adjusted := runtime.imageOCRAdjustMessage(context.Background(), MessageEvent{Kind: EventKindGroup}, message)
	if llmMessageHasImages(adjusted) {
		t.Fatalf("text-only message still carries images: %#v", adjusted.Parts)
	}
	if messagesContainImages([]llm.Message{adjusted}) {
		t.Fatal("adjusted message would still route to vision group")
	}
	content := adjusted.Content
	if !strings.Contains(content, "画面描述：一张对话截图") || !strings.Contains(content, "图中文字：\n你好，在吗？") {
		t.Fatalf("content = %q", content)
	}
	if !strings.Contains(content, "【图片消息】") {
		t.Fatalf("missing text-only header: %q", content)
	}
	// 识别文本长得就像一段现成答复，块头必须说清它只是理解材料，不能复述给用户。
	if !strings.Contains(content, "不要复述") {
		t.Fatalf("text-only header must forbid reciting the recognition text: %q", content)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2 (describe + transcribe)", provider.calls.Load())
	}
	// 原始文本要保留在替换后的消息里。
	if !strings.Contains(content, "看看这张图") {
		t.Fatalf("original text lost: %q", content)
	}
}

// 仅文字模式 + 关闭画面描述 = 纯 OCR：只烧一次转写调用。
func TestImageOCRTextOnlyOCROnly(t *testing.T) {
	provider := &imageOCRFakeProvider{response: "只有转写文字"}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{
		"backend": imageOCRBackendLLM, "delivery": imageOCRDeliveryText, "describe_enabled": false,
	})

	adjusted := runtime.imageOCRAdjustMessage(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage("data:image/png;base64,BBB"))
	if llmMessageHasImages(adjusted) {
		t.Fatal("images not stripped")
	}
	if !strings.Contains(adjusted.Content, "图中文字：\n只有转写文字") || strings.Contains(adjusted.Content, "画面描述") {
		t.Fatalf("content = %q", adjusted.Content)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls.Load())
	}
}

// 仅文字模式 + 关闭 OCR 后端 = 纯 LLM 描述：不支持看图的主模型只拿画面描述。
func TestImageOCRTextOnlyDescriptionOnly(t *testing.T) {
	provider := &imageOCRFakeProvider{response: "夕阳下的海边照片"}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{
		"backend": imageOCRBackendDisabled, "delivery": imageOCRDeliveryText,
	})

	adjusted := runtime.imageOCRAdjustMessage(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage("data:image/png;base64,CCC"))
	if llmMessageHasImages(adjusted) {
		t.Fatal("images not stripped")
	}
	if !strings.Contains(adjusted.Content, "画面描述：夕阳下的海边照片") || strings.Contains(adjusted.Content, "图中文字") {
		t.Fatalf("content = %q", adjusted.Content)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls.Load())
	}
}

// 仅文字模式下描述和 OCR 都关掉等于没配置，插件不动消息。
func TestImageOCRTextOnlyInactiveWithoutAnyRecognizer(t *testing.T) {
	provider := &imageOCRFakeProvider{response: "不该被调用"}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{
		"backend": imageOCRBackendDisabled, "delivery": imageOCRDeliveryText, "describe_enabled": false,
	})
	message := imageOCRTestMessage("data:image/png;base64,DDD")

	adjusted := runtime.imageOCRAdjustMessage(context.Background(), MessageEvent{Kind: EventKindGroup}, message)
	if !llmMessageHasImages(adjusted) || provider.calls.Load() != 0 {
		t.Fatalf("message was touched: %#v, calls = %d", adjusted.Parts, provider.calls.Load())
	}
}

// 识别全挂时也要把图摘掉并留占位说明——不支持看图的模型不能收到图片，
// 也不该对着凭空消失的图片瞎猜。
func TestImageOCRTextOnlyKeepsPlaceholderOnFailure(t *testing.T) {
	provider := &imageOCRFakeProvider{err: errors.New("vision down")}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{
		"backend": imageOCRBackendLLM, "delivery": imageOCRDeliveryText,
	})

	adjusted := runtime.imageOCRAdjustMessage(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage("data:image/png;base64,EEE"))
	if llmMessageHasImages(adjusted) {
		t.Fatal("images not stripped on failure")
	}
	if !strings.Contains(adjusted.Content, "未能识别出这张图的内容") {
		t.Fatalf("content = %q", adjusted.Content)
	}
}

// 仅文字模式下多图逐张编号，超出上限的图要交代数量。
func TestImageOCRTextOnlyNumbersImagesAndReportsOverflow(t *testing.T) {
	provider := &imageOCRFakeProvider{response: "内容"}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{
		"backend": imageOCRBackendLLM, "delivery": imageOCRDeliveryText, "describe_enabled": false, "max_images": 2,
	})

	adjusted := runtime.imageOCRAdjustMessage(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage("data:a", "data:b", "data:c"))
	content := adjusted.Content
	if !strings.Contains(content, "第 1 张图") || !strings.Contains(content, "第 2 张图") {
		t.Fatalf("content = %q", content)
	}
	if !strings.Contains(content, "另有 1 张图") {
		t.Fatalf("overflow note missing: %q", content)
	}
	if llmMessageHasImages(adjusted) {
		t.Fatal("overflow images must be stripped too")
	}
}

// 默认交付方式（图片+文字）不受画面描述开关影响，仍是原来的随图附带转写。
func TestImageOCRAttachModeKeepsImages(t *testing.T) {
	provider := &imageOCRFakeProvider{response: "随图文字"}
	runtime := newImageOCRTestRuntime(t, provider, map[string]any{"backend": imageOCRBackendLLM})

	adjusted := runtime.imageOCRAdjustMessage(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage("data:image/png;base64,FFF"))
	if !llmMessageHasImages(adjusted) {
		t.Fatal("attach mode must keep images")
	}
	if !strings.Contains(adjusted.Content, "图片文字转写") || !strings.Contains(adjusted.Content, "随图文字") {
		t.Fatalf("content = %q", adjusted.Content)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1 (no describe call in attach mode)", provider.calls.Load())
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

func imageOCRDataURL(payload string) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(payload))
}

// 同一张表情包被不同的人再发一次，甚至重启换了个进程，都不该重新识别一遍。
func TestImageOCRReusesRecognitionAcrossRuntimesByContentHash(t *testing.T) {
	store := newFakeImageRecognitionStore()
	settings := map[string]any{"backend": imageOCRBackendLLM, "model": "vl"}
	sticker := imageOCRDataURL("same-sticker-bytes")

	first := &imageOCRFakeProvider{response: "狗狗大喊"}
	runtimeA := newImageOCRTestRuntimeWithStore(t, first, settings, store)
	noticeA := runtimeA.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "1"}, imageOCRTestMessage(sticker))
	if !strings.Contains(noticeA, "狗狗大喊") || first.calls.Load() != 1 {
		t.Fatalf("first notice = %q calls = %d", noticeA, first.calls.Load())
	}

	// 换一个 Runtime（等价于重启，进程内缓存是空的），但共用同一份持久化记录。
	second := &imageOCRFakeProvider{response: "不该被调用"}
	runtimeB := newImageOCRTestRuntimeWithStore(t, second, settings, store)
	noticeB := runtimeB.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "2"}, imageOCRTestMessage(sticker))
	if noticeB != noticeA {
		t.Fatalf("cached notice = %q, want %q", noticeB, noticeA)
	}
	if second.calls.Load() != 0 {
		t.Fatalf("second runtime called the model %d times, want 0", second.calls.Load())
	}
}

// 「这张图没字」同样要落库：表情包大多没字，反复确认最浪费。
func TestImageOCRPersistsNoTextResult(t *testing.T) {
	store := newFakeImageRecognitionStore()
	settings := map[string]any{"backend": imageOCRBackendLLM}
	sticker := imageOCRDataURL("blank-sticker")

	first := &imageOCRFakeProvider{response: imageOCRNoTextMarker}
	runtimeA := newImageOCRTestRuntimeWithStore(t, first, settings, store)
	_ = runtimeA.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage(sticker))

	second := &imageOCRFakeProvider{response: "不该被调用"}
	runtimeB := newImageOCRTestRuntimeWithStore(t, second, settings, store)
	if notice := runtimeB.imageOCRContextText(context.Background(), MessageEvent{Kind: EventKindGroup}, imageOCRTestMessage(sticker)); notice != "" {
		t.Fatalf("notice = %q, want empty", notice)
	}
	if second.calls.Load() != 0 {
		t.Fatalf("no-text conclusion was not reused: calls = %d", second.calls.Load())
	}
}

// 换了识别后端或模型就得重算，不能拿上一套引擎的输出顶上。
func TestImageOCRCacheKeySplitsByBackendAndModel(t *testing.T) {
	sticker := imageOCRDataURL("cfg-sensitive")
	digest := imageOCRContentDigest(sticker)
	llm := imageOCRConfig{Backend: imageOCRBackendLLM, Model: "vl-a"}
	llmOtherModel := imageOCRConfig{Backend: imageOCRBackendLLM, Model: "vl-b"}
	local := imageOCRConfig{Backend: imageOCRBackendLocal, LocalCommand: "tesseract", LocalLanguages: "chi_sim"}
	localOtherLang := imageOCRConfig{Backend: imageOCRBackendLocal, LocalCommand: "tesseract", LocalLanguages: "eng"}

	keys := map[string]string{
		"llm":        imageOCRCacheKey(imageRecognitionKindOCR, digest, llm),
		"llm-model":  imageOCRCacheKey(imageRecognitionKindOCR, digest, llmOtherModel),
		"local":      imageOCRCacheKey(imageRecognitionKindOCR, digest, local),
		"local-lang": imageOCRCacheKey(imageRecognitionKindOCR, digest, localOtherLang),
		"describe":   imageOCRCacheKey(imageRecognitionKindDescribe, digest, llm),
		"describe-b": imageOCRCacheKey(imageRecognitionKindDescribe, digest, llmOtherModel),
	}
	seen := map[string]string{}
	for name, key := range keys {
		if other, clash := seen[key]; clash {
			t.Fatalf("%s and %s share a cache key", name, other)
		}
		seen[key] = name
	}
	// 同一套配置必须稳定命中同一个键。
	if imageOCRCacheKey(imageRecognitionKindOCR, digest, llm) != keys["llm"] {
		t.Fatal("cache key is not stable for the same config")
	}
	// 描述不受 OCR 后端设置影响：它只走 vision 分组。
	describeWithLocalOCR := imageOCRCacheKey(imageRecognitionKindDescribe, digest, imageOCRConfig{Backend: imageOCRBackendLocal, Model: "vl-a"})
	if describeWithLocalOCR != keys["describe"] {
		t.Fatal("describe cache key should not depend on the OCR backend")
	}
}

// 内容哈希只看图片字节：同一张图换个 data URL 写法也要命中同一条缓存，
// 不同图片则必须分开。
func TestImageOCRContentDigestFollowsBytes(t *testing.T) {
	same := imageOCRDataURL("identical-bytes")
	if imageOCRContentDigest(same) != imageOCRContentDigest(same) {
		t.Fatal("digest is not stable")
	}
	if imageOCRContentDigest(same) == imageOCRContentDigest(imageOCRDataURL("other-bytes")) {
		t.Fatal("different images share a digest")
	}
	// 同样的字节配不同 MIME 前缀仍是同一张图。
	jpeg := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("identical-bytes"))
	if imageOCRContentDigest(same) != imageOCRContentDigest(jpeg) {
		t.Fatal("same bytes should hash the same regardless of the mime prefix")
	}
	// 拿不到字节的远程地址退回按地址哈希，仍然可用且互不串味。
	if imageOCRContentDigest("https://example.com/a.png") == imageOCRContentDigest("https://example.com/b.png") {
		t.Fatal("different remote URLs share a digest")
	}
}
