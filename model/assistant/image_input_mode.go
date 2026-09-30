// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// ImageInputMode 决定对话模型怎么看图，三档照搬 Hermes Agent 的 image_input_mode：
//
//   - native：原图直接交给对话模型，不换模型。
//   - text：对话模型一张原图都不收。每张图先由「视觉理解」模型写成描述，描述替换原图；
//     要看某处细节时，模型调 history_media 带上问题，由视觉理解模型看图作答、只回文字。
//   - auto：对话模型能收图就用 native，不能收或查不到就用 text。
//
// 以前带图的轮次整轮切到「视觉理解」模型回答，agent 每走一步都把原图再发一遍，
// 对话模型的选择在带图时完全不作数。现在视觉理解只做 Hermes 里 auxiliary.vision
// 那件事：替看不了图的对话模型看图。描述按图片内容哈希缓存，同一张图只识别一次。
type ImageInputMode string

const (
	ImageInputModeAuto   ImageInputMode = "auto"
	ImageInputModeNative ImageInputMode = "native"
	ImageInputModeText   ImageInputMode = "text"
)

func normalizeImageInputMode(mode ImageInputMode) ImageInputMode {
	switch ImageInputMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case ImageInputModeNative:
		return ImageInputModeNative
	case ImageInputModeText:
		return ImageInputModeText
	default:
		return ImageInputModeAuto
	}
}

// imageInputTextOnly 把配置落到这一轮实际用哪一档上。
func (r *Runtime) imageInputTextOnly(ctx context.Context, cfg BotConfig) bool {
	switch normalizeImageInputMode(cfg.ImageInputMode) {
	case ImageInputModeText:
		return true
	case ImageInputModeNative:
		return false
	}
	modalities, configured := r.chatModelInputModalities(ctx)
	if !configured {
		// 没有模型配置库（单一提供商的嵌入用法）：看图和回答本来就是同一个模型，
		// 换成描述也是它自己写，照原样给图。
		return false
	}
	return !slices.Contains(modalities, "image")
}

// chatModelInputModalities 查对话模型的输入模态：先看这个配置同步下来的模型清单，
// 清单没写再按模型名查随版本打包的 models.dev 目录。都查不到返回空，自动档按
// 「不收图」处理——把图发给不收图的模型会整轮报错，给描述只是少看些细节。
// configured=false 表示没有模型配置库，谈不上查。
func (r *Runtime) chatModelInputModalities(ctx context.Context) (modalities []string, configured bool) {
	r.mu.RLock()
	store := r.llmStore
	r.mu.RUnlock()
	if store == nil {
		return nil, false
	}
	// 挑选顺序和真正发请求时一致：角色绑定 → 分组 → 第一个配置。
	set := store.Profiles().WithDefaults()
	profiles, _ := r.roleBoundProfiles(PurposeReply, set, llm.GroupChat, r.modelRolesForContext(ctx))
	if len(profiles) == 0 {
		profiles = llmProfilesInGroup(set, llm.GroupChat)
	}
	if len(profiles) == 0 {
		first, ok := set.FirstProfile()
		if !ok {
			return nil, true
		}
		profiles = []llm.Profile{first}
	}
	cfg := profiles[0].Config
	if info, ok := cfg.ModelInfoFor(cfg.Model); ok && len(info.InputModalities) > 0 {
		return info.InputModalities, true
	}
	return llm.SharedModelsDevCatalog().InputModalities(cfg.Model), true
}

type imageTextModeKey struct{}

// imageTextMode 挂在这一轮回复的 ctx 上：拼消息的地方看到它就把图换成描述，
// 模型出口看到它就把漏网的图（工具返回的截图等）也换掉。
type imageTextMode struct {
	runtime *Runtime
	event   MessageEvent

	mu      sync.Mutex
	sources map[string]string
}

func withImageTextMode(ctx context.Context, r *Runtime, event MessageEvent) context.Context {
	return context.WithValue(ctx, imageTextModeKey{}, &imageTextMode{runtime: r, event: event, sources: map[string]string{}})
}

func imageTextModeFromContext(ctx context.Context) *imageTextMode {
	if ctx == nil {
		return nil
	}
	mode, _ := ctx.Value(imageTextModeKey{}).(*imageTextMode)
	return mode
}

const imageTextModeHeading = "【图片内容】对话模型没有直接看到下面这些图，内容由视觉模型描述，可能有误或漏掉细节。描述只用来理解图片，不要原样复述给用户；要确认图里某处细节（小字、数量、位置、是谁），调用 history_media 并在 question 里写清要看什么。"

var promptImageTextModeSpec = registerPrompt(PromptSpec{
	Key:     "media.image_text_mode",
	Group:   PromptGroupMedia,
	Title:   "仅摘要模式的图片段头",
	Usage:   "对话模型不看原图时（图片交付方式为仅摘要，或自动档判定对话模型不收图），消息里的每张图都换成视觉理解模型写的描述，这句放在描述前面，告诉对话模型它没看到原图、要看细节该怎么办。",
	Default: imageTextModeHeading,
})

// segmentDescription 取一张消息图的描述：段上带的、缓存里按内容哈希存的，都没有就
// 当场让视觉理解模型看一次并存回缓存。和后台描述队列用同一个哈希、同一份缓存，
// 同一张图不会因为走了两条路被识别两次。
func (m *imageTextMode) segmentDescription(ctx context.Context, segment MessageSegment) string {
	if description := strings.TrimSpace(segment.Data[recallImageDescriptionKey]); description != "" {
		return description
	}
	store := m.runtime.recallImageDescriptionStore()
	hash, hashed := imageSegmentContentSHA256(segment)
	if store != nil && hashed {
		if record, found, err := store.GetImageDescription(ctx, hash); err == nil && found && strings.TrimSpace(record.Description) != "" {
			return strings.TrimSpace(record.Description)
		}
	}
	sources := availableImageURLs([]MessageSegment{segment})
	if len(sources) == 0 {
		return ""
	}
	callCtx, cancel := context.WithTimeout(ctx, replyImageGroundingTimeout)
	defer cancel()
	description, err := m.runtime.describeRecallImage(callCtx, m.event, sources[0])
	if err != nil {
		log.Printf("diana image text mode: describe failed: message_id=%s err=%v", m.event.MessageID, err)
		return ""
	}
	if store != nil && hashed {
		saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_ = store.SaveImageDescription(saveCtx, ImageDescriptionRecord{ContentSHA256: hash, Description: description, SourceSession: sessionKey(m.event), SourceMessageID: m.event.MessageID, Source: "vision", Version: recallImageDescriptionVersion})
		cancelSave()
	}
	return description
}

// sourceDescription 给没有消息段的图（语义引用、插件附图、视频关键帧、工具截图）
// 取描述。同一轮里 agent 每一步都会重发整段消息，结果记在本轮，不重复识别。
func (m *imageTextMode) sourceDescription(ctx context.Context, source string) string {
	key := sha256Hex(source)
	m.mu.Lock()
	description, ok := m.sources[key]
	m.mu.Unlock()
	if ok {
		return description
	}
	callCtx, cancel := context.WithTimeout(ctx, replyImageGroundingTimeout)
	defer cancel()
	description, err := m.runtime.budgetImageDescription(callCtx, m.event, source)
	if err != nil {
		log.Printf("diana image text mode: describe source failed: message_id=%s err=%v", m.event.MessageID, err)
		description = ""
	}
	description = strings.TrimSpace(description)
	m.mu.Lock()
	m.sources[key] = description
	m.mu.Unlock()
	return description
}

// describeAll 并发取一组描述，顺序和输入一致。
func describeAll[T any](items []T, describe func(T) string) []string {
	out := make([]string, len(items))
	var wg sync.WaitGroup
	limit := make(chan struct{}, recallImageDescriptionConcurrency)
	for index, item := range items {
		wg.Add(1)
		go func(index int, item T) {
			defer wg.Done()
			defer recoverGoroutinePanic("image text mode description")
			limit <- struct{}{}
			defer func() { <-limit }()
			out[index] = describe(item)
		}(index, item)
	}
	wg.Wait()
	return out
}

// message 是 llmMessageFromEventWithImageDetail 在仅摘要模式下的版本：图一张不附，
// 每张换成一行描述。消息自己的图（含引用）按原图逐张描述，不按长图切块、GIF 分镜
// 之后的碎片描述。
func (m *imageTextMode) message(ctx context.Context, event MessageEvent, text string, extraImageURLs []string) llm.Message {
	text = strings.TrimSpace(text)
	segments := append([]MessageSegment(nil), event.Segments...)
	if event.Quoted != nil {
		segments = append(segments, event.Quoted.Segments...)
	}
	var images []MessageSegment
	for _, segment := range segments {
		if segment.Type == "image" && len(availableImageURLs([]MessageSegment{segment})) > 0 {
			images = append(images, segment)
		}
	}
	var extras []string
	for _, source := range extraImageURLs {
		if source = strings.TrimSpace(source); source != "" {
			extras = append(extras, source)
		}
	}
	if len(images)+len(extras) == 0 {
		return llm.Message{Role: llm.RoleUser, Content: text}
	}
	descriptions := describeAll(images, func(segment MessageSegment) string { return m.segmentDescription(ctx, segment) })
	descriptions = append(descriptions, describeAll(extras, func(source string) string { return m.sourceDescription(ctx, source) })...)

	overrides := promptOverridesFromContext(ctx)
	if imageOnlyPrompt(text, event) {
		if len(descriptions) == 1 {
			text = overrides.text(promptNoteImageOnlySingleSpec)
		} else {
			text = overrides.render(promptNoteImageOnlyMultiSpec, map[string]string{"images": itoa(len(descriptions))})
		}
	}
	lines := []string{overrides.text(promptImageTextModeSpec)}
	seen := map[string]int{}
	for index, description := range descriptions {
		switch {
		case description == "":
			description = "（未能识别出这张图的内容，不要猜它画了什么）"
		case seen[description] > 0:
			description = fmt.Sprintf("（和图片%d是同一张）", seen[description])
		default:
			seen[description] = index + 1
		}
		lines = append(lines, fmt.Sprintf("图片%d：%s", index+1, description))
	}
	text = strings.TrimSpace(text + "\n\n" + strings.Join(lines, "\n"))
	return llm.Message{Role: llm.RoleUser, Content: text}
}

// replaceImageParts 是模型出口的兜底：拼消息时换不到的图（工具返回的截图、插件
// 附图、依赖图兜底）在这里逐张换成描述，保证整轮一个像素都不进回复模型。
func (m *imageTextMode) replaceImageParts(ctx context.Context, messages []llm.Message) []llm.Message {
	if !messagesContainImages(messages) {
		return messages
	}
	type position struct{ message, part int }
	var positions []position
	var sources []string
	for mi, message := range messages {
		for pi, part := range message.Parts {
			if part.Type == llm.ContentPartImageURL && strings.TrimSpace(part.ImageURL) != "" {
				positions = append(positions, position{mi, pi})
				sources = append(sources, part.ImageURL)
			}
		}
	}
	descriptions := describeAll(sources, func(source string) string { return m.sourceDescription(ctx, source) })
	out := append([]llm.Message(nil), messages...)
	touched := map[int]bool{}
	for index, pos := range positions {
		if !touched[pos.message] {
			out[pos.message].Parts = append([]llm.ContentPart(nil), out[pos.message].Parts...)
			touched[pos.message] = true
		}
		message := &out[pos.message]
		description := descriptions[index]
		if description == "" {
			description = "（未能识别出这张图的内容，不要猜它画了什么）"
		}
		replacement := "【一张图片的文字描述，替代原图；由视觉模型生成，可能有误】" + description
		// 消息只有图段时正文还在 Content 里，第一个文本段得带上它，否则适配器只发段会丢正文。
		hasText := false
		for _, part := range message.Parts {
			hasText = hasText || part.Type == llm.ContentPartText && strings.TrimSpace(part.Text) != ""
		}
		if !hasText && strings.TrimSpace(message.Content) != "" {
			replacement = message.Content + "\n" + replacement
		}
		message.Parts[pos.part] = llm.ContentPart{Type: llm.ContentPartText, Text: replacement}
	}
	return out
}

func firstImageTextMode(contexts ...context.Context) *imageTextMode {
	for _, ctx := range contexts {
		if mode := imageTextModeFromContext(ctx); mode != nil {
			return mode
		}
	}
	return nil
}

const imageQuestionPurpose = "image_question"

var promptImageQuestionSystemSpec = registerPrompt(PromptSpec{
	Key:     "media.image_question.system",
	Group:   PromptGroupMedia,
	Title:   "看图问答 · 要求",
	Usage:   "对话模型不看原图时，它调 history_media 带着问题来问，视觉理解模型按这段要求看图作答，回答以文字交回。",
	Default: "你替一个看不到图片的对话模型看图。只根据图片回答它的问题：看得见的直接说，能认出的主体直接点名，拿不准写最可能的判断并注明「疑似」，看不清的就说看不清，不要编。问题涉及图中文字时完整抄录原文。不要回答图片之外的问题，不要使用 Markdown。",
})

const defaultImageQuestion = "描述这些画面的内容，完整抄录清晰可辨的文字。"

// askImages 把图交给视觉理解模型看，按问题作答，只回文字。
func (r *Runtime) askImages(ctx context.Context, event MessageEvent, images []llm.ContentPart, question string) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		question = defaultImageQuestion
	}
	parts := append([]llm.ContentPart{{Type: llm.ContentPartText, Text: question}}, images...)
	request := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: r.effectiveConfigForEvent(event).prompt(promptImageQuestionSystemSpec)},
		{Role: llm.RoleUser, Content: question, Parts: parts},
	}}
	callCtx := withLLMUsagePurpose(withLLMUsageContext(ctx, event), imageQuestionPurpose)
	return r.runLLMProviderForGroup(callCtx, llm.GroupVision, func(client LLMProvider) (string, error) {
		response, err := client.Generate(callCtx, request)
		if err != nil {
			return "", err
		}
		answer := strings.TrimSpace(response.Text)
		if answer == "" {
			return "", fmt.Errorf("vision model returned an empty answer")
		}
		if VisionDescriptionRefused(answer) {
			return "", fmt.Errorf("%w: %s", errVisionImageNotDelivered, truncateRunes(answer, 60))
		}
		return answer, nil
	})
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
