// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func imageFormatTestServer(t *testing.T, payload *map[string]any, b64 string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": b64}}})
	}))
}

// 指定 gif 时 output_format 和帧参数都要发给上游，返回的 data URI 按文件头标成 image/gif。
func TestOpenAICompatibleImageSendsGIFParameters(t *testing.T) {
	var payload map[string]any
	gif := base64.StdEncoding.EncodeToString([]byte("GIF89a\x01\x00\x01\x00\x80\x00\x00"))
	server := imageFormatTestServer(t, &payload, gif)
	defer server.Close()

	resp, err := GenerateImage(context.Background(), ProviderConfig{
		Provider: ProviderOpenAICompatible, APIKey: "secret", BaseURL: server.URL + "/v1",
		Model: "gpt-test", ImageModel: "gpt-image-2.6-sunburst",
	}, ImageGenerateRequest{Prompt: "像素猫挥爪", OutputFormat: ImageFormatGIF, FrameCount: 12, FrameDurationMS: 150}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if payload["output_format"] != "gif" || payload["frame_count"] != float64(12) || payload["frame_duration_ms"] != float64(150) {
		t.Fatalf("payload = %#v", payload)
	}
	if len(resp.Images) != 1 || !strings.HasPrefix(resp.Images[0], "data:image/gif;base64,") {
		t.Fatalf("images = %#v", resp.Images)
	}
}

// 不指定格式时请求体和以前一样，不带 output_format；帧参数只跟 gif 走。
func TestOpenAICompatibleImageOmitsFormatByDefault(t *testing.T) {
	var payload map[string]any
	server := imageFormatTestServer(t, &payload, "YWJjZA==")
	defer server.Close()

	resp, err := GenerateImage(context.Background(), ProviderConfig{
		Provider: ProviderOpenAICompatible, APIKey: "secret", BaseURL: server.URL + "/v1",
		Model: "gpt-test", ImageModel: "gpt-image-2",
	}, ImageGenerateRequest{Prompt: "猫", FrameCount: 12}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"output_format", "frame_count", "frame_duration_ms"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("payload 不该带 %s: %#v", key, payload)
		}
	}
	if resp.Images[0] != "data:image/png;base64,YWJjZA==" {
		t.Fatalf("认不出格式时应按 PNG: %q", resp.Images[0])
	}
}

func TestOpenAIBase64ImageMediaType(t *testing.T) {
	cases := map[string]string{
		"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR": "image/png",
		"\xff\xd8\xff\xe0\x00\x10JFIF\x00":    "image/jpeg",
		"RIFF\x24\x00\x00\x00WEBPVP8 ":        "image/webp",
		"GIF87a\x01\x00\x01\x00":              "image/gif",
		"not an image":                        "image/png",
	}
	for raw, want := range cases {
		if got := openAIBase64ImageMediaType(base64.StdEncoding.EncodeToString([]byte(raw))); got != want {
			t.Errorf("%q => %q, want %q", raw, got, want)
		}
	}
	if got := openAIBase64ImageMediaType("!!!"); got != "image/png" {
		t.Errorf("非法 base64 => %q", got)
	}
}

// Gemini 只出静态图，要 GIF 时直接报错让故障转移换提供商，不悄悄交一张 PNG。
func TestGeminiImageRejectsGIF(t *testing.T) {
	client := &geminiClient{cfg: ProviderConfig{Provider: ProviderGemini, ImageModel: "gemini-image"}}
	if _, err := client.GenerateImage(context.Background(), ImageGenerateRequest{Prompt: "猫", OutputFormat: "GIF"}); err == nil || !strings.Contains(err.Error(), "GIF") {
		t.Fatalf("err = %v", err)
	}
}

func TestImageModelSupportsAnimation(t *testing.T) {
	override := func(model string, output ...string) []ModelInfo {
		return []ModelInfo{{ID: model, CapabilitiesOverride: &ModelCapabilities{InputModalities: []string{"text"}, OutputModalities: output}}}
	}
	for _, tc := range []struct {
		name string
		cfg  ProviderConfig
		want bool
	}{
		{"已知模型", ProviderConfig{Provider: ProviderOpenAICompatible, ImageModel: "gpt-image-2.6-sunburst"}, true},
		{"带命名空间", ProviderConfig{Provider: ProviderOpenAICompatible, ImageModel: "openai/gpt-image-2.6-flare"}, true},
		{"老模型", ProviderConfig{Provider: ProviderOpenAICompatible, ImageModel: "gpt-image-2.5-sunburst"}, false},
		{"手动勾上", ProviderConfig{Provider: ProviderOpenAICompatible, ImageModel: "relay-gif", Models: override("relay-gif", "image", ModalityAnimation)}, true},
		{"手动勾掉", ProviderConfig{Provider: ProviderOpenAICompatible, ImageModel: "gpt-image-2.6-sunburst", Models: override("gpt-image-2.6-sunburst", "image")}, false},
		{"清单标注", ProviderConfig{Provider: ProviderOpenAICompatible, ImageModel: "relay-gif", Models: []ModelInfo{{ID: "relay-gif", OutputModalities: []string{"image", "animation"}}}}, true},
	} {
		if got := tc.cfg.ImageModelSupportsAnimation(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
