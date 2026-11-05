// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

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

	"github.com/SuInk/diana/model/llm"
)

// 群友要 gpt-image-2.6 的 GIF：工具得把 format/帧参数一路带到图片接口，成品按 GIF 存、发。
func TestDianaImageToolGeneratesGIF(t *testing.T) {
	gifBytes := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00animated")
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media" {
			writeTestPNG(w)
			return
		}
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(gifBytes)}}})
	}))
	defer server.Close()

	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "secret", BaseURL: server.URL + "/v1",
		Model: "gpt-test", ImageModel: "gpt-image-2.6-sunburst",
	})}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), store, nil, nil, nil)
	runtime.SetMediaStore(mediaStore(t))
	sharer := &recordingLocalMediaSharer{url: server.URL + "/media"}
	runtime.SetLocalMediaSharer(sharer)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "gif-image"}
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", event.UserID)
	tool := newDianaImageTool(runtime, event, policy)

	schema := tool.(*dianaImageTool).InputSchema()["properties"].(map[string]any)
	for _, key := range []string{"format", "frame_count", "frame_duration_ms"} {
		if _, ok := schema[key]; !ok {
			t.Fatalf("schema 缺少 %s", key)
		}
	}

	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "generate", "prompt": "像素猫挥爪、眨眼再回到初始姿势", "format": "gif",
		"frame_count": 12, "frame_duration_ms": 5000,
	}); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 2*time.Second, func() bool { return runtime.activeSubagentTaskCount() == 0 })

	// 越界的帧时长夹到上限，而不是让上游拒掉整次请求。
	if payload["output_format"] != "gif" || payload["frame_count"] != float64(12) || payload["frame_duration_ms"] != float64(llm.ImageGIFMaxFrameDurationMS) {
		t.Fatalf("payload = %#v", payload)
	}
	paths := sharer.pathsSnapshot()
	if len(paths) != 1 || filepath.Ext(paths[0]) != ".gif" {
		t.Fatalf("shared paths = %#v", paths)
	}
	if data, err := os.ReadFile(paths[0]); err != nil || string(data) != string(gifBytes) {
		t.Fatalf("shared gif = %q, err = %v", data, err)
	}
	sent := channel.sentSnapshot()
	if len(sent) == 0 || !strings.Contains(sent[0].Text, "开始生成动图") {
		t.Fatalf("sent = %#v", sent)
	}
}

func TestDianaImageToolFormatValidation(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "secret", Model: "gpt-test", ImageModel: "gpt-image-2",
	})}, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m1"}
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", event.UserID)
	tool := &dianaImageTool{runtime: runtime, event: event, relationship: policy}

	// 上游 edits 不出 GIF，要明确告诉模型改用 generate，而不是悄悄交一张静态图。
	if _, err := tool.prepareRequest(map[string]any{"operation": "edit", "prompt": "让它动起来", "format": "gif"}); err == nil || !strings.Contains(err.Error(), "generate") {
		t.Fatalf("edit+gif err = %v", err)
	}
	if _, err := tool.prepareRequest(map[string]any{"operation": "generate", "prompt": "猫", "format": "bmp"}); err == nil {
		t.Fatal("不支持的格式应报错")
	}
	request, err := tool.prepareRequest(map[string]any{"operation": "generate", "prompt": "猫", "format": "JPG", "frame_count": 8})
	if err != nil {
		t.Fatal(err)
	}
	if request.Format != llm.ImageFormatJPEG || request.FrameCount != 0 {
		t.Fatalf("jpg 应归一成 jpeg，帧参数只跟 gif 走: %#v", request)
	}
	request, err = tool.prepareRequest(map[string]any{"operation": "generate", "prompt": "猫", "format": "gif", "frame_count": 1})
	if err != nil {
		t.Fatal(err)
	}
	if request.FrameCount != llm.ImageGIFMinFrames || request.FrameDurationMS != 0 {
		t.Fatalf("帧数应夹到下限、没填的帧时长用上游默认: %#v", request)
	}
}
