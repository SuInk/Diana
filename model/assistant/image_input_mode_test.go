// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 1x1 透明 PNG，够加载链路当成一张真图。
const imageInputModeTestPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

func TestImageInputModeNormalizeAndPayload(t *testing.T) {
	for input, want := range map[ImageInputMode]ImageInputMode{"": ImageInputModeAuto, "TEXT": ImageInputModeText, "native": ImageInputModeNative, "bogus": ImageInputModeAuto} {
		if got := normalizeImageInputMode(input); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", input, got, want)
		}
	}
	cfg := DefaultBotConfig()
	cfg.ImageInputMode = ImageInputModeText
	payload := PayloadFromConfig(cfg)
	if payload.ImageInputMode != ImageInputModeText {
		t.Fatalf("payload lost image_input_mode: %q", payload.ImageInputMode)
	}
	// 只改别的字段的保存不能把已选的档位冲掉。
	payload.ImageInputMode = ""
	if got := ConfigFromPayload(payload, cfg).WithDefaults().ImageInputMode; got != ImageInputModeText {
		t.Fatalf("partial payload reset image_input_mode to %q", got)
	}
}

func TestImageInputTextOnlyResolvesExplicitModes(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	ctx := context.Background()
	if !runtime.imageInputTextOnly(ctx, BotConfig{ImageInputMode: ImageInputModeText}) {
		t.Fatal("text mode should be text-only")
	}
	if runtime.imageInputTextOnly(ctx, BotConfig{ImageInputMode: ImageInputModeNative}) {
		t.Fatal("native mode must attach images")
	}
	// 查不到模型清单时自动档按原图走，老部署行为不变。
	if runtime.imageInputTextOnly(ctx, BotConfig{ImageInputMode: ImageInputModeAuto}) {
		t.Fatal("auto without model metadata must keep native behavior")
	}
}

// 仅摘要模式：消息里的图换成描述，一张原图都不附。
func TestImageTextModeMessageReplacesImagesWithDescriptions(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := photoEvent("30001", "10001", 1_800_000_000)
	event.Segments[0].Data[recallImageDescriptionKey] = "一只橘猫张大嘴在笑"
	ctx := withImageTextMode(context.Background(), runtime, event)

	message, failures := llmMessageFromEventWithImageDetail(ctx, event, "这是啥", nil, "high")
	if len(failures) != 0 || llmMessageHasImagePart(message) || len(message.Parts) != 0 {
		t.Fatalf("text mode must not attach pixels: %+v failures=%v", message, failures)
	}
	for _, want := range []string{"这是啥", "图片1：一只橘猫张大嘴在笑", "history_media"} {
		if !strings.Contains(message.Content, want) {
			t.Fatalf("message should contain %q:\n%s", want, message.Content)
		}
	}
}

// 模型出口兜底：工具返回的截图也换成描述，整轮不再切到视觉路由。
func TestImageTextModeReplacesLeftoverImageParts(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	ctx := withImageTextMode(context.Background(), runtime, MessageEvent{MessageID: "30002"})
	mode := imageTextModeFromContext(ctx)
	mode.sources[sha256Hex(imageInputModeTestPNG)] = "网页截图：价格 99 元"

	messages := []llm.Message{{Role: llm.RoleUser, Content: "工具观察", Parts: []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: imageInputModeTestPNG}}}}
	out := mode.replaceImageParts(ctx, messages)
	if messagesContainImages(out) {
		t.Fatal("leftover image parts must be replaced")
	}
	if text := out[0].Parts[0].Text; !strings.Contains(text, "工具观察") || !strings.Contains(text, "价格 99 元") {
		t.Fatalf("replacement should keep the content and carry the description: %q", text)
	}
	if !messagesContainImages(messages) {
		t.Fatal("the caller's messages must not be mutated")
	}
}

// history_media 在仅摘要模式下带着问题让视觉模型看图，只回文字。
func TestHistoryMediaTextModeAsksVisionModel(t *testing.T) {
	provider := &capturingLLMProvider{reply: "是猫，橘色，张着嘴"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	tool := newDianaHistoryImagesTool(runtime, MessageEvent{MessageID: "30003"})
	if _, ok := tool.InputSchema()["properties"].(map[string]any)["question"]; ok {
		t.Fatal("native mode schema should not grow a question field")
	}
	tool.withTextMode(true)
	if _, ok := tool.InputSchema()["properties"].(map[string]any)["question"]; !ok {
		t.Fatal("text mode schema needs a question field")
	}

	answer, err := runtime.askImages(context.Background(), MessageEvent{MessageID: "30003"}, []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: imageInputModeTestPNG}}, "这是猫还是狗")
	if err != nil || answer != "是猫，橘色，张着嘴" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	request := provider.requestSnapshot()
	if !messagesContainImages(request.Messages) || !strings.Contains(requestTextForPrivacyTest(request), "这是猫还是狗") {
		t.Fatalf("vision model should receive the image and the question: %+v", request)
	}
}

// 端到端：仅摘要模式下主回复的请求里一张原图都没有，描述在当前消息里；原图模式照旧附图。
func TestReplyInImageTextModeSendsNoPixels(t *testing.T) {
	requestsFor := func(t *testing.T, mode ImageInputMode) []llm.GenerateRequest {
		t.Helper()
		provider := &agentSequenceLLMProvider{responses: []string{`{"action":"final","content":"是只猫"}`}}
		runtime := NewRuntime(BotConfig{OwnerID: "owner", AgentEnabled: true, ImageInputMode: mode, ReplySafetyMasterEnabled: boolPointer(false)}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
			return provider, nil
		})
		event := MessageEvent{
			Kind: EventKindPrivate, UserID: "owner", MessageID: "30004", ProfileID: "qq", RawMessage: "这是啥",
			Segments: []MessageSegment{
				{Type: "text", Data: map[string]string{"text": "这是啥"}},
				{Type: "image", Data: map[string]string{"file": "cat.png", "url": imageInputModeTestPNG, recallImageDescriptionKey: "一只橘猫张大嘴在笑"}},
			},
		}
		if _, err := runtime.replyTo(context.Background(), event, "这是啥"); err != nil {
			t.Fatal(err)
		}
		if len(provider.requests) == 0 {
			t.Fatal("provider was not called")
		}
		return provider.requests
	}

	found := false
	for _, request := range requestsFor(t, ImageInputModeText) {
		if messagesContainImages(request.Messages) {
			t.Fatalf("text mode leaked pixels into a request: %s", requestTextForPrivacyTest(request))
		}
		found = found || strings.Contains(requestTextForPrivacyTest(request), "一只橘猫张大嘴在笑")
	}
	if !found {
		t.Fatal("the reply request should carry the image description")
	}
	attached := false
	for _, request := range requestsFor(t, ImageInputModeNative) {
		attached = attached || messagesContainImages(request.Messages)
	}
	if !attached {
		t.Fatal("native mode should still attach the original image")
	}
}
