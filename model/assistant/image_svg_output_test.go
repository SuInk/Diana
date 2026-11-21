// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func testSquarePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if x >= 16 && x < 48 && y >= 16 && y < 48 {
				c = color.RGBA{200, 40, 40, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// format=svg：图片接口照常出 PNG，图先发出去，再把描好的 SVG 当文件发。
func TestDianaImageToolSVGSendsImageThenVectorFile(t *testing.T) {
	pngBytes := testSquarePNG(t)
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media" {
			writeTestPNG(w)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(pngBytes)}}})
	}))
	defer server.Close()

	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "secret", BaseURL: server.URL + "/v1",
		Model: "gpt-test", ImageModel: "gpt-image-2",
	})}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{Platform: PlatformOneBotV11}, channel, NewPluginManager(), store, nil, nil, nil)
	runtime.SetMediaStore(mediaStore(t))
	runtime.SetLocalMediaSharer(&recordingLocalMediaSharer{url: server.URL + "/media"})
	event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "svg-image"}
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", event.UserID)
	tool := newDianaImageTool(context.Background(), runtime, event, policy)

	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "generate", "prompt": "红色方块，扁平矢量插画风格", "format": "svg",
	}); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 2*time.Second, func() bool { return runtime.activeSubagentTaskCount() == 0 })

	if payload["output_format"] != "png" {
		t.Fatalf("svg 要先向图片接口要 PNG: %#v", payload)
	}
	var uploads []recordingAPICall
	for _, call := range channel.callsSnapshot() {
		if call.action == "upload_group_file" {
			uploads = append(uploads, call)
		}
	}
	if len(uploads) != 1 || !strings.HasSuffix(uploads[0].params["name"].(string), ".svg") {
		t.Fatalf("uploads = %#v", uploads)
	}
	imageSent := false
	for _, message := range channel.sentSnapshot() {
		imageSent = imageSent || len(message.ImageURLs) > 0
	}
	if !imageSent {
		t.Fatalf("成图也要发出去: %#v", channel.sentSnapshot())
	}
}

func TestVectorizeGeneratedImages(t *testing.T) {
	inline := "data:image/png;base64," + base64.StdEncoding.EncodeToString(testSquarePNG(t))
	files, note := (&Runtime{}).vectorizeGeneratedImages(context.Background(), "image-x", []string{inline, "data:image/png;base64,bm90LWEtcG5n"})
	if len(files) != 1 || files[0].Name != "image-x-1.svg" {
		t.Fatalf("files = %#v", files)
	}
	svg := string(files[0].Data)
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, `color="#c82828"`) {
		t.Fatalf("svg = %s", svg)
	}
	// 一张转不成不拖累另一张，原因要带回给用户。
	if !strings.Contains(note, "SVG 没转成") {
		t.Fatalf("note = %q", note)
	}
}

func TestDianaImageToolSVGFormatValidation(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42", Platform: PlatformOneBotV11}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "secret", Model: "gpt-test", ImageModel: "gpt-image-2",
	})}, nil, nil, nil)
	event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m1"}
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", event.UserID)
	tool := &dianaImageTool{runtime: runtime, event: event, relationship: policy}

	request, err := tool.prepareRequest(map[string]any{"operation": "generate", "prompt": "猫", "format": "SVG"})
	if err != nil || request.Format != dianaImageFormatSVG {
		t.Fatalf("request = %#v, err = %v", request, err)
	}
	if upstreamImageFormat(request.Format) != llm.ImageFormatPNG || upstreamImageFormat(llm.ImageFormatWebP) != llm.ImageFormatWebP {
		t.Fatal("只有 svg 要换成 png 交给图片接口")
	}
	if _, err := tool.prepareRequest(map[string]any{"operation": "generate", "prompt": "猫", "format": "svg", "sticker": true}); err == nil || !strings.Contains(err.Error(), "表情包") {
		t.Fatalf("svg+sticker err = %v", err)
	}
	enum, _ := tool.InputSchema()["properties"].(map[string]any)["format"].(map[string]any)["enum"].([]string)
	if !strings.Contains(strings.Join(enum, ","), dianaImageFormatSVG) {
		t.Fatalf("format enum = %v", enum)
	}
}
