// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 1x1 透明 PNG，够加载链路当成一张真图。
const imageInputModeTestPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// finalReplyProvider 每次都直接给最终回复，并记下每次请求。这里要看的是请求里有没有
// 原图、有没有描述，agent 多走一轮也不该让测试崩。
type finalReplyProvider struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
}

func (p *finalReplyProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, cloneGenerateRequestForTest(req))
	p.mu.Unlock()
	return &llm.GenerateResponse{Text: `{"action":"final","content":"是只猫"}`}, nil
}

func (p *finalReplyProvider) snapshot() []llm.GenerateRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.GenerateRequest(nil), p.requests...)
}

func imageInputModeTestStore() *stubLLMProfileStore {
	return &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "chat", Group: llm.GroupChat, Config: llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "key", Model: "chat-model",
	}}}}}
}

func TestImageInputModeNormalizeAndPayload(t *testing.T) {
	for input, want := range map[ImageInputMode]ImageInputMode{"": ImageInputModeAuto, "TEXT": ImageInputModeText, "off": ImageInputModeOff, "native": ImageInputModeAuto} {
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

// 自动和仅文字描述都把消息里的图换成描述；关闭时原图直接交给对话模型，后台也不再写描述。
func TestImageInputModeDecidesDescriptions(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), imageInputModeTestStore(), nil, nil, nil)
	for mode, want := range map[ImageInputMode]bool{ImageInputModeAuto: true, ImageInputModeText: true, ImageInputModeOff: false} {
		if got := runtime.imageDescriptionsInPrompt(BotConfig{ImageInputMode: mode}); got != want {
			t.Fatalf("%s: descriptions in prompt = %v, want %v", mode, got, want)
		}
	}
	if (BotConfig{ImageInputMode: ImageInputModeOff}).backgroundImageDescriptionEnabled() {
		t.Fatal("turning vision off must stop the background description queue")
	}
	if !(BotConfig{}).backgroundImageDescriptionEnabled() {
		t.Fatal("the default keeps background descriptions")
	}
	// 没有模型配置库时写描述和回答是同一个模型，照原样给图。
	embedded := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if embedded.imageDescriptionsInPrompt(BotConfig{}) {
		t.Fatal("without an llm store the image should be attached as is")
	}
}

// 消息里的图换成描述，一张原图都不附。
func TestImageTextModeMessageReplacesImagesWithDescriptions(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := photoEvent("30001", "10001", 1_800_000_000)
	event.Segments[0].Data[recallImageDescriptionKey] = "一只橘猫张大嘴在笑"
	ctx := withImageTextMode(context.Background(), runtime, event)

	message, failures := llmMessageFromEventWithImageDetail(ctx, event, "这是啥", nil, "high")
	if len(failures) != 0 || llmMessageHasImagePart(message) || len(message.Parts) != 0 {
		t.Fatalf("descriptions mode must not attach pixels: %+v failures=%v", message, failures)
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

// 自动档的 history_media 带着问题让视觉理解看图，只回文字；只有自动档才有 question。
func TestHistoryMediaAutoAsksVisionModel(t *testing.T) {
	provider := &capturingLLMProvider{reply: "是猫，橘色，张着嘴"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	hasQuestion := func(tool *dianaHistoryImagesTool) bool {
		_, ok := tool.InputSchema()["properties"].(map[string]any)["question"]
		return ok
	}
	event := MessageEvent{MessageID: "30003"}
	if hasQuestion(newDianaHistoryImagesTool(runtime, event)) || hasQuestion(newDianaHistoryImagesTool(runtime, event).withImageInput(true, ImageInputModeText)) {
		t.Fatal("only auto mode asks the vision model a question")
	}
	if !hasQuestion(newDianaHistoryImagesTool(runtime, event).withImageInput(true, ImageInputModeAuto)) {
		t.Fatal("auto mode schema needs a question field")
	}

	answer, err := runtime.askImages(context.Background(), event, []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: imageInputModeTestPNG}}, "这是猫还是狗")
	if err != nil || answer != "是猫，橘色，张着嘴" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	request := provider.requestSnapshot()
	if !messagesContainImages(request.Messages) || !strings.Contains(requestTextForPrivacyTest(request), "这是猫还是狗") {
		t.Fatalf("vision model should receive the image and the question: %+v", request)
	}
}

// 当前这条的图在提示词里只有描述：不传消息 ID 也能读到当前这条，哪怕它还没进历史。
// 自动档交给视觉理解作答，仅文字描述档只回描述，关闭时当前这条本来就附着原图。
func TestHistoryMediaReadsDescribedCurrentImage(t *testing.T) {
	provider := &capturingLLMProvider{reply: "右下角写着 99 元"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	current := MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "30005",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file": "price.png", "url": imageInputModeTestPNG, recallImageDescriptionKey: "一张价签"}}},
	}
	ctx := withImageTextMode(context.Background(), runtime, current)

	asking := newDianaHistoryImagesTool(runtime, current).withImageInput(true, ImageInputModeAuto)
	output, err := asking.Run(ctx, map[string]any{"question": "价格是多少"})
	if err != nil {
		t.Fatal(err)
	}
	if len(asking.ToolResultParts(output)) != 0 || !strings.Contains(output, "右下角写着 99 元") {
		t.Fatalf("auto mode should return the vision model's answer, not pixels: %s", output)
	}

	describing := newDianaHistoryImagesTool(runtime, current).withImageInput(true, ImageInputModeText)
	output, err = describing.Run(ctx, map[string]any{"message_id": "30005"})
	if err != nil {
		t.Fatal(err)
	}
	if len(describing.ToolResultParts(output)) != 0 || !strings.Contains(output, "一张价签") {
		t.Fatalf("descriptions-only mode should return the description: %s", output)
	}

	native := newDianaHistoryImagesTool(runtime, current).withImageInput(false, ImageInputModeOff)
	if _, err := native.Run(context.Background(), map[string]any{}); err == nil {
		t.Fatal("off mode should not default to re-reading the current message")
	}
}

// 端到端：默认档主回复的请求里一张原图都没有，描述在当前消息里；关闭时原图直接给对话模型。
func TestReplyImageInputModeEndToEnd(t *testing.T) {
	requestsFor := func(t *testing.T, mode ImageInputMode) []llm.GenerateRequest {
		t.Helper()
		provider := &finalReplyProvider{}
		runtime := NewRuntime(BotConfig{OwnerID: "owner", AgentEnabled: true, ImageInputMode: mode, ReplySafetyMasterEnabled: boolPointer(false)}, nilChannel{}, NewDefaultPluginManager(), imageInputModeTestStore(), nil, nil, nil)
		runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return provider, nil })
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
		requests := provider.snapshot()
		if len(requests) == 0 {
			t.Fatal("provider was not called")
		}
		return requests
	}

	found := false
	for _, request := range requestsFor(t, ImageInputModeAuto) {
		if messagesContainImages(request.Messages) {
			t.Fatalf("auto mode leaked pixels into a request: %s", requestTextForPrivacyTest(request))
		}
		found = found || strings.Contains(requestTextForPrivacyTest(request), "一只橘猫张大嘴在笑")
	}
	if !found {
		t.Fatal("the reply request should carry the image description")
	}
	attached := false
	for _, request := range requestsFor(t, ImageInputModeOff) {
		attached = attached || messagesContainImages(request.Messages)
	}
	if !attached {
		t.Fatal("off mode should hand the original image to the chat model")
	}
}

// 视频关键帧合成一次交给视觉理解，不逐帧各识一次；同一轮重拼消息不再重复识别。
func TestImageTextModeDescribesVideoFramesInOneCall(t *testing.T) {
	provider := &capturingLLMProvider{reply: "一只猫从沙发跳到桌上"}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	mode := imageTextModeFromContext(withImageTextMode(context.Background(), runtime, MessageEvent{MessageID: "30006"}))
	frames := []string{imageInputModeTestPNG, imageInputModeTestPNG + "#2", imageInputModeTestPNG + "#3"}
	for range 2 {
		if text := mode.framesDescription(context.Background(), frames); !strings.Contains(text, "一只猫从沙发跳到桌上") {
			t.Fatalf("frames description = %q", text)
		}
	}
	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 1 {
		t.Fatalf("frames should be described in one vision call per turn, got %d", calls)
	}
}

// 描述拿不到的图退回附原图：对话模型要看过图才答，不能对着一句「未能识别」硬答。
func TestImageTextModeFallsBackToPixelsWhenDescriptionFails(t *testing.T) {
	provider := &capturingLLMProvider{reply: ""}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "30007",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file": "cat.png", "url": imageInputModeTestPNG}}},
	}
	ctx := withImageTextMode(context.Background(), runtime, event)
	message, failures := llmMessageFromEventWithImageDetail(ctx, event, "这是啥", nil, "high")
	if len(failures) != 0 || !llmMessageHasImagePart(message) {
		t.Fatalf("an undescribed image must be attached as is: %+v failures=%v", message, failures)
	}
	if !strings.Contains(message.Content, "直接看原图") {
		t.Fatalf("the model should be told the original is attached: %s", message.Content)
	}

	mode := imageTextModeFromContext(ctx)
	leftover := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: imageInputModeTestPNG}}}}
	if !messagesContainImages(mode.replaceImageParts(ctx, leftover)) {
		t.Fatal("a tool image without a description must stay attached")
	}
	if mode.framesDescription(ctx, []string{imageInputModeTestPNG}) != "" {
		t.Fatal("failed frame descriptions should return empty so the frames stay attached")
	}
}
