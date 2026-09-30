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

func TestImageInputPlanResolvesExplicitModes(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	ctx := context.Background()
	if describe, pixels := runtime.imageInputPlan(ctx, BotConfig{ImageInputMode: ImageInputModeText}); !describe || pixels {
		t.Fatalf("text mode: describe=%v pixels=%v", describe, pixels)
	}
	if describe, pixels := runtime.imageInputPlan(ctx, BotConfig{ImageInputMode: ImageInputModeNative}); describe || !pixels {
		t.Fatalf("native mode: describe=%v pixels=%v", describe, pixels)
	}
	// 没有模型配置库时写描述和回答是同一个模型，照原样给图。
	if describe, _ := runtime.imageInputPlan(ctx, BotConfig{ImageInputMode: ImageInputModeAuto}); describe {
		t.Fatal("auto without an llm store must attach images")
	}
}

// 自动档：消息里的图总是给描述；对话模型能看图时，模型要看原图就交给它自己看，
// 看不了（清单写明不收或查不到）就由视觉理解代看。
func TestImageInputAutoDescribesAndGivesPixelsOnDemand(t *testing.T) {
	planFor := func(model string, modalities []string) (bool, bool) {
		store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "chat", Group: llm.GroupChat, Config: llm.ProviderConfig{
			Provider: llm.ProviderOpenAICompatible, APIKey: "key", Model: model,
			Models: []llm.ModelInfo{{ID: model, InputModalities: modalities}},
		}}}}}
		runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
		return runtime.imageInputPlan(context.Background(), BotConfig{ImageInputMode: ImageInputModeAuto})
	}
	for _, c := range []struct {
		model      string
		modalities []string
		pixels     bool
	}{
		{"house-model", []string{"text", "image"}, true},
		{"house-model", []string{"text"}, false},
		{"gemini-3.8-flash-low", nil, true},
		{"house-model-unknown", nil, false},
	} {
		describe, pixels := planFor(c.model, c.modalities)
		if !describe || pixels != c.pixels {
			t.Fatalf("%s %v: describe=%v pixels=%v, want describe and pixels=%v", c.model, c.modalities, describe, pixels, c.pixels)
		}
	}
}

// 仅摘要模式：消息里的图换成描述，一张原图都不附。
func TestImageTextModeMessageReplacesImagesWithDescriptions(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := photoEvent("30001", "10001", 1_800_000_000)
	event.Segments[0].Data[recallImageDescriptionKey] = "一只橘猫张大嘴在笑"
	ctx := withImageTextMode(context.Background(), runtime, event, false)

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
	ctx := withImageTextMode(context.Background(), runtime, MessageEvent{MessageID: "30002"}, false)
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
	// 对话模型看得了图时，那是模型自己要看的原图，照原样放行。
	pixels := imageTextModeFromContext(withImageTextMode(context.Background(), runtime, MessageEvent{MessageID: "30002"}, true))
	if !messagesContainImages(pixels.replaceImageParts(ctx, messages)) {
		t.Fatal("on-demand pixels must reach a chat model that can see images")
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
	tool.withImageInput(true, true)
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

// 自动档下当前这条的图只有描述；模型回头要看原图时，不传消息 ID 也能读到当前这条，
// 哪怕它还没进历史。对话模型看得了图就拿到原图，看不了就由视觉理解代看。
func TestHistoryMediaReadsDescribedCurrentImage(t *testing.T) {
	provider := &capturingLLMProvider{reply: "右下角写着 99 元"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	current := MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "30005",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file": "price.png", "url": imageInputModeTestPNG}}},
	}

	pixels := newDianaHistoryImagesTool(runtime, current).withImageInput(true, false)
	output, err := pixels.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pixels.ToolResultParts(output)) == 0 {
		t.Fatalf("a chat model that can see images should get the current image: %s", output)
	}

	asking := newDianaHistoryImagesTool(runtime, current).withImageInput(true, true)
	output, err = asking.Run(context.Background(), map[string]any{"message_id": "30005", "question": "价格是多少"})
	if err != nil {
		t.Fatal(err)
	}
	if len(asking.ToolResultParts(output)) != 0 || !strings.Contains(output, "右下角写着 99 元") {
		t.Fatalf("a text-only chat model should get the vision model's answer, not pixels: %s", output)
	}

	// 原图档下当前这条本来就附着原图，不传 ID 时不重复读它。
	native := newDianaHistoryImagesTool(runtime, current)
	if _, err := native.Run(context.Background(), map[string]any{}); err == nil {
		t.Fatal("native mode should not default to re-reading the current message")
	}
}
