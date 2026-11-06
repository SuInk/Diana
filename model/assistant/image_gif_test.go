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
	tool := newDianaImageTool(context.Background(), runtime, event, policy)

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
	// 单帧 1024 时 9 帧的精灵图超出上游像素预算，GIF 一律交给上游挑尺寸。
	if payload["size"] != "auto" {
		t.Fatalf("gif size = %#v", payload["size"])
	}
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
	tool := &dianaImageTool{runtime: runtime, event: event, relationship: policy, animation: true}

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

// 画面描述带句号时，开场白不能念成「纯色背景。，完成后」。
func TestImageAnnouncementSubjectTrimsTrailingPunctuation(t *testing.T) {
	if got := imageAnnouncementSubject("像素风小狗摇尾巴，纯色背景。"); got != "像素风小狗摇尾巴，纯色背景" {
		t.Fatalf("subject = %q", got)
	}
}

// 动图是图片模型的单独能力：同组里并列着能出 GIF 和不能出 GIF 的模型时，GIF 请求
// 只能落到前者；兼容网关不认 output_format 回了静图，也不能当动图交差。
func TestGIFGenerationOnlyUsesAnimationModels(t *testing.T) {
	gifBytes := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00animated")
	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		model, _ := payload["model"].(string)
		models = append(models, model)
		body := gifBytes
		if model == "still-gateway" {
			body = pngBytes
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(body)}}})
	}))
	defer server.Close()

	base := llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "secret", BaseURL: server.URL + "/v1", Model: "gpt-test"}
	gateway := base
	// 手动勾了动图、上游却照旧回 PNG 的网关。
	gateway.Models = []llm.ModelInfo{{ID: "still-gateway", CapabilitiesOverride: &llm.ModelCapabilities{InputModalities: []string{"text"}, OutputModalities: []string{"image", llm.ModalityAnimation}}}}
	set := llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "old", Group: "pool", Config: base},
		{ID: "gateway", Group: "pool", Config: gateway},
		{ID: "new", Group: "pool", Config: base},
	}}
	role := ModelRole{ProfileID: "old", Model: "gpt-image-2.5-sunburst", Fallbacks: []ModelRole{
		{ProfileID: "gateway", Model: "still-gateway"}, {ProfileID: "new", Model: "gpt-image-2.6-sunburst"},
	}}
	runtime := NewRuntime(BotConfig{ID: "bot", ModelRoles: map[string]ModelRole{"image": role}}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: set}, nil, nil, nil)

	if !runtime.animationImageAvailable(context.Background()) {
		t.Fatal("有 gpt-image-2.6 时应能出动图")
	}
	resp, cfg, err := runtime.generateImageWithFailover(context.Background(), llm.ImageGenerateRequest{Prompt: "猫", OutputFormat: llm.ImageFormatGIF})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "still-gateway,gpt-image-2.6-sunburst" || cfg.ImageModelWithDefault() != "gpt-image-2.6-sunburst" {
		t.Fatalf("models = %v, picked %s", models, cfg.ImageModelWithDefault())
	}
	if !strings.HasPrefix(resp.Images[0], "data:image/gif") {
		t.Fatalf("image = %.40s", resp.Images[0])
	}

	// 静态图照旧从第一个候选开始。
	models = nil
	if _, _, err := runtime.generateImageWithFailover(context.Background(), llm.ImageGenerateRequest{Prompt: "猫"}); err != nil || len(models) != 1 || models[0] != "gpt-image-2.5-sunburst" {
		t.Fatalf("still models = %v, err = %v", models, err)
	}
}

// 没有能出动图的模型时，gif 不摆给模型，硬填也当场报错，不先回一句「开始生成动图」。
func TestDianaImageToolHidesGIFWithoutAnimationModel(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "secret", Model: "gpt-test", ImageModel: "gpt-image-2.5-sunburst",
	})}, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m1"}
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", event.UserID)
	tool := newDianaImageTool(context.Background(), runtime, event, policy).(*dianaImageTool)

	properties := tool.InputSchema()["properties"].(map[string]any)
	if _, ok := properties["frame_count"]; ok {
		t.Fatal("没有动图模型时不该有帧参数")
	}
	if enum, _ := properties["format"].(map[string]any)["enum"].([]string); strings.Contains(strings.Join(enum, ","), "gif") {
		t.Fatalf("format enum = %v", enum)
	}
	if _, err := tool.prepareRequest(map[string]any{"operation": "generate", "prompt": "猫", "format": "gif"}); err == nil || !strings.Contains(err.Error(), "动图") {
		t.Fatalf("err = %v", err)
	}
}

// 审核拒绝按内容判，换模型再试等于绕审核，还要再花一次钱；上游 5xx 才换下一个。
func TestImageFailoverStopsOnContentPolicy(t *testing.T) {
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		model, _ := payload["model"].(string)
		models = append(models, model)
		w.Header().Set("Content-Type", "application/json")
		switch model {
		case "blocked":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Your request was rejected by the safety system.","type":"image_generation_user_error","code":"moderation_blocked"}}`))
		case "flaky":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"bad gateway"}}`))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))}}})
		}
	}))
	defer server.Close()

	base := llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "secret", BaseURL: server.URL + "/v1", Model: "gpt-test"}
	set := llm.ProfileSet{Profiles: []llm.Profile{{ID: "a", Group: "pool", Config: base}, {ID: "b", Group: "pool", Config: base}}}
	run := func(first string) error {
		models = nil
		role := ModelRole{ProfileID: "a", Model: first, Fallbacks: []ModelRole{{ProfileID: "b", Model: "spare"}}}
		runtime := NewRuntime(BotConfig{ID: "bot", ModelRoles: map[string]ModelRole{"image": role}}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: set}, nil, nil, nil)
		_, _, err := runtime.generateImageWithFailover(context.Background(), llm.ImageGenerateRequest{Prompt: "猫"})
		return err
	}
	if err := run("blocked"); err == nil || strings.Join(models, ",") != "blocked" {
		t.Fatalf("审核拒绝后不该换模型: models=%v err=%v", models, err)
	}
	if err := run("flaky"); err != nil || strings.Join(models, ",") != "flaky,spare" {
		t.Fatalf("5xx 应换下一个: models=%v err=%v", models, err)
	}
}
