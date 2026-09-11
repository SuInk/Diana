// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

// inlinePart 描述一段构造出来的响应内容：要么是文字，要么是一段带 MIME 的字节。
type inlinePart struct {
	text string
	mime string
	data []byte
}

func geminiResponseWithParts(t *testing.T, items []inlinePart) *genai.GenerateContentResponse {
	t.Helper()
	parts := make([]*genai.Part, 0, len(items))
	for _, item := range items {
		if item.text != "" {
			parts = append(parts, &genai.Part{Text: item.text})
			continue
		}
		parts = append(parts, &genai.Part{InlineData: &genai.Blob{MIMEType: item.mime, Data: item.data}})
	}
	return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: parts}}}}
}

// 生图选到 gemini 时必须能发出请求。这两个接口没实现过，GenerateImage 在断言那一步
// 就返回「image generation is not supported for provider」，配置里明明列着 gemini 的
// 图片模型却连请求都发不出去。
func TestGeminiClientImplementsImageInterfaces(t *testing.T) {
	client, err := NewClient(ProviderConfig{Provider: ProviderGemini, APIKey: "k", Model: "gemini-3.1-flash-image"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.(ImageGenerator); !ok {
		t.Fatal("gemini 客户端没实现 ImageGenerator，生图会被判为不支持")
	}
	if _, ok := client.(ImageEditor); !ok {
		t.Fatal("gemini 客户端没实现 ImageEditor，改图会被判为不支持")
	}
}

// 像素尺寸换成宽高比：上层统一传 "1024x1024" 这种写法，Gemini 只收比例。
func TestGeminiAspectRatioFromPixelSize(t *testing.T) {
	for _, tc := range []struct{ size, want string }{
		{"1024x1024", "1:1"},
		{"1024x1536", "2:3"},
		{"1536x1024", "3:2"},
		{"1080x1920", "9:16"},
		{"1920x1080", "16:9"},
		{"2560x1080", "21:9"},
		{"768x1024", "3:4"},
		{"1024x768", "4:3"},
		// 认不出来就不带这个字段，让模型用默认值，而不是猜一个塞进去。
		{"", ""},
		{"large", ""},
		{"1024x0", ""},
		{"axb", ""},
	} {
		if got := geminiAspectRatio(tc.size); got != tc.want {
			t.Fatalf("geminiAspectRatio(%q) = %q, want %q", tc.size, got, tc.want)
		}
	}
}

// 响应里的图片要编码成和 OpenAI 那条链路一致的 data URI，上层拿到的东西才与
// 选中了谁无关；非图片的部分不能混进去。
func TestGeminiInlineImagesEncodesDataURI(t *testing.T) {
	resp := geminiResponseWithParts(t, []inlinePart{
		{text: "这是你要的图"},
		{mime: "image/png", data: []byte{1, 2, 3}},
		{mime: "text/plain", data: []byte("not an image")},
	})
	images := geminiInlineImages(resp)
	if len(images) != 1 {
		t.Fatalf("images=%d，只应取出那一张图：%v", len(images), images)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte{1, 2, 3})
	if images[0] != want {
		t.Fatalf("images[0]=%q, want %q", images[0], want)
	}
}

// 空提示词不该发请求出去。
func TestGeminiImageRejectsEmptyPrompt(t *testing.T) {
	client, err := newGeminiClient(ProviderConfig{Provider: ProviderGemini, APIKey: "k", Model: "gemini-3.1-flash-image"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GenerateImage(context.Background(), ImageGenerateRequest{Prompt: "  "}); err == nil {
		t.Fatal("空提示词应当被拒绝")
	}
	if _, err := client.EditImage(context.Background(), ImageEditRequest{Prompt: "改一下"}); err == nil {
		t.Fatal("没有源图的改图请求应当被拒绝")
	}
}

// 改图的源图读取和 OpenAI 那条链路共用一套规则：同一张图在两个提供商之间换来换去，
// 能不能读、读成什么 MIME 不该取决于选中了谁。
func TestImageEditInputSharedAcrossProviders(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n0123456789")
	path := filepath.Join(t.TempDir(), "source.png")
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		path,
		"file://" + path,
		"data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	} {
		got, err := imageEditInputFrom(context.Background(), nil, value, 0)
		if err != nil {
			t.Fatalf("读取 %q 失败：%v", value, err)
		}
		if string(got.data) != string(png) || !strings.HasPrefix(got.mediaType, "image/") {
			t.Fatalf("读取 %q 得到 mediaType=%q bytes=%d", value, got.mediaType, len(got.data))
		}
	}
	if _, err := imageEditInputFrom(context.Background(), nil, "ftp://example.invalid/a.png", 0); err == nil {
		t.Fatal("不认识的来源应当报错")
	}
}

// 真实生图：配了 DIANA_IMAGE_PROBE_KEY 就对着真实提供商发一次请求，把返回的图片
// 落到磁盘上。默认跳过，因为它要花钱、要联网。
//
//	DIANA_IMAGE_PROBE_KEY=<key> DIANA_IMAGE_PROBE_BASE_URL=https://... \
//	DIANA_IMAGE_PROBE_MODEL=gemini-3.1-flash-image \
//	go test ./model/llm -run TestGeminiImageGenerationLive -v
func TestGeminiImageGenerationLive(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("DIANA_IMAGE_PROBE_KEY"))
	if key == "" {
		t.Skip("未配置 DIANA_IMAGE_PROBE_KEY，跳过真实生图")
	}
	model := strings.TrimSpace(os.Getenv("DIANA_IMAGE_PROBE_MODEL"))
	if model == "" {
		model = "gemini-3.1-flash-image"
	}
	cfg := ProviderConfig{
		Provider:   ProviderGemini,
		APIKey:     key,
		BaseURL:    strings.TrimSpace(os.Getenv("DIANA_IMAGE_PROBE_BASE_URL")),
		ImageModel: model,
		Timeout:    3 * time.Minute,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	resp, err := GenerateImage(ctx, cfg, ImageGenerateRequest{Prompt: "画一只坐在窗台上的橘猫，卡通风格，干净背景", Size: "1024x1024", N: 1})
	if err != nil {
		t.Fatalf("真实生图失败：%v", err)
	}
	if resp.Provider != ProviderGemini || len(resp.Images) == 0 {
		t.Fatalf("响应不对：%+v", resp)
	}
	input, ok := dataImageInput(resp.Images[0])
	if !ok {
		t.Fatalf("返回的不是 data URI：%.80s", resp.Images[0])
	}
	if len(input.Data) < 1024 {
		t.Fatalf("图片只有 %d 字节，太小了", len(input.Data))
	}
	out := filepath.Join(os.TempDir(), "diana-gemini-image-live.png")
	if dir := strings.TrimSpace(os.Getenv("DIANA_IMAGE_PROBE_OUT")); dir != "" {
		out = dir
	}
	if err := os.WriteFile(out, input.Data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("已生成图片：%s（%s，%d 字节）", out, input.MediaType, len(input.Data))
}

// 端到端跑一遍自己这条链路，不依赖上游配额：起一个本地服务端按 Gemini 的
// generateContent 形状回一张图，验证请求怎么发、响应怎么解、图片编码成什么。
func TestGeminiImageRoundTripAgainstFakeServer(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("p", 64))
	var got struct {
		path string
		body map[string]any
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{"parts": []any{
					map[string]any{"text": "给你画好了"},
					map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(png)}},
				}},
				"finishReason": "STOP",
			}},
		})
	}))
	defer server.Close()

	cfg := ProviderConfig{Provider: ProviderGemini, APIKey: "k", BaseURL: server.URL, ImageModel: "gemini-3.1-flash-image"}
	resp, err := GenerateImage(context.Background(), cfg, ImageGenerateRequest{Prompt: "一只橘猫", Size: "1920x1080", N: 1})
	if err != nil {
		t.Fatalf("生图失败：%v", err)
	}
	if resp.Provider != ProviderGemini || len(resp.Images) != 1 {
		t.Fatalf("响应不对：%+v", resp)
	}
	input, ok := dataImageInput(resp.Images[0])
	if !ok || string(input.Data) != string(png) || input.MediaType != "image/png" {
		t.Fatalf("图片没有原样带回来：ok=%v mediaType=%q bytes=%d", ok, input.MediaType, len(input.Data))
	}
	if !strings.Contains(got.path, "gemini-3.1-flash-image") {
		t.Fatalf("请求打到了 %q，模型名没带上", got.path)
	}
	// 尺寸要换成宽高比发出去，模态要显式声明成文字加图片，否则模型只回文字。
	config, _ := got.body["generationConfig"].(map[string]any)
	if config == nil {
		config, _ = got.body["config"].(map[string]any)
	}
	raw, err := json.Marshal(got.body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"16:9"`, `"IMAGE"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("请求体里没有 %s：%s", want, raw)
		}
	}

	// 改图：源图要作为 inlineData 一起发出去。
	source := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if _, err := EditImage(context.Background(), cfg, ImageEditRequest{Prompt: "换成夜景", Images: []string{source}, Size: "1024x1024", N: 1}); err != nil {
		t.Fatalf("改图失败：%v", err)
	}
	raw, err = json.Marshal(got.body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), base64.StdEncoding.EncodeToString(png)) {
		t.Fatalf("改图请求里没有带上源图：%s", raw)
	}
}
