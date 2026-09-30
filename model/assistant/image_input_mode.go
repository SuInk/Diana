// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// ImageInputMode 决定对话模型怎么看图。带图的消息始终由对话模型回答，不再整轮
// 切到视觉理解；视觉理解只做 Hermes 里 auxiliary.vision 那件事：替对话模型看图。
//
//   - auto（默认）：消息里的图只给视觉理解写的描述；要看细节时对话模型调
//     history_media 带上问题，视觉理解看图作答、只回文字（Hermes 的 vision_analyze）。
//   - text：只给描述，history_media 也只返回描述，不再追问。
//   - off：不经过视觉理解。原图直接交给对话模型，不写描述，后台描述队列也停掉；
//     对话模型本身要能看图。
//
// 描述本来就会写（后台描述队列、当前图的描述锚点），默认只给描述、不再附原图，
// 省掉和描述重复的那份 token；agent 每走一步原图还要重发一遍。
type ImageInputMode string

const (
	ImageInputModeAuto ImageInputMode = "auto"
	ImageInputModeText ImageInputMode = "text"
	ImageInputModeOff  ImageInputMode = "off"
)

func normalizeImageInputMode(mode ImageInputMode) ImageInputMode {
	switch ImageInputMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case ImageInputModeText:
		return ImageInputModeText
	case ImageInputModeOff:
		return ImageInputModeOff
	default:
		return ImageInputModeAuto
	}
}

// imageDescriptionsInPrompt 表示这一轮消息里的图换成描述。没有模型配置库（单一
// 提供商的嵌入用法）时写描述和回答是同一个模型，描述不会更便宜，照原样给图。
func (r *Runtime) imageDescriptionsInPrompt(cfg BotConfig) bool {
	if normalizeImageInputMode(cfg.ImageInputMode) == ImageInputModeOff {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.llmStore != nil
}

// backgroundImageDescriptionEnabled 是后台描述队列的开关：关了自动描述，或者视觉
// 理解整个关掉（图片交付方式为关闭），都不再写描述。
func (cfg BotConfig) backgroundImageDescriptionEnabled() bool {
	return boolValue(cfg.AutoImageDescription, true) && normalizeImageInputMode(cfg.ImageInputMode) != ImageInputModeOff
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

const imageTextModeHeading = "【图片内容】下面这些图没有附原图，内容由视觉模型描述，可能有误或漏掉细节。描述只用来理解图片，不要原样复述给用户；描述够用就直接答，要确认图里某处细节（小字、数量、位置、是谁）再调用 history_media。"

var promptImageTextModeSpec = registerPrompt(PromptSpec{
	Key:     "media.image_text_mode",
	Group:   PromptGroupMedia,
	Title:   "图片描述段头",
	Usage:   "图片交付方式为自动或仅文字描述时，消息里的每张图都换成视觉理解写的描述，这句放在描述前面，告诉对话模型它没看到原图、要看细节该怎么办。",
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

const videoFramesQuestion = "这些是同一段视频按时间顺序抽出的关键帧。按先后说清这段视频在拍什么、发生了什么动作和变化，能认出的人物、角色、物体直接点名，完整抄录清晰可辨的字幕和画面文字。"

// framesDescription 把一段视频的关键帧一次交给视觉理解，按时间顺序描述整段内容。
// 同一轮里 agent 每一步都会重拼消息，结果记在本轮，不重复识别。拿不到描述返回空，
// 调用方照原样把关键帧附给对话模型。
func (m *imageTextMode) framesDescription(ctx context.Context, frames []string) string {
	key := "frames:" + sha256Hex(strings.Join(frames, "\n"))
	m.mu.Lock()
	description, ok := m.sources[key]
	m.mu.Unlock()
	if !ok {
		parts := make([]llm.ContentPart, 0, len(frames))
		for _, ready := range llmReadyImageURLs(ctx, frames) {
			parts = append(parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: ready, Detail: "auto"})
		}
		if len(parts) > 0 {
			callCtx, cancel := context.WithTimeout(ctx, replyImageGroundingTimeout)
			answer, err := m.runtime.askImages(callCtx, m.event, parts, videoFramesQuestion)
			cancel()
			if err != nil {
				log.Printf("diana image text mode: describe video frames failed: message_id=%s err=%v", m.event.MessageID, err)
			}
			description = strings.TrimSpace(answer)
		}
		m.mu.Lock()
		m.sources[key] = description
		m.mu.Unlock()
	}
	if description == "" {
		return ""
	}
	return "【视频关键帧内容，由视觉模型按时间顺序描述，可能有误】" + description
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

// message 是 llmMessageFromEventWithImageDetail 在只给描述时的版本：每张图换成一行
// 描述。消息自己的图（含引用）按原图逐张描述，不按长图切块、GIF 分镜之后的碎片描述。
// 描述拿不到的图退回附原图——没看图就答是最不能接受的；原图也读不到时照原来的
// 规则报读取失败，由调用方拦下这一轮。
func (m *imageTextMode) message(ctx context.Context, event MessageEvent, text string, extraImageURLs []string, detail string) (llm.Message, []error) {
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
		return llm.Message{Role: llm.RoleUser, Content: text}, nil
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
	var fallback []string
	for index, description := range descriptions {
		switch {
		case description == "":
			description = "（视觉模型没能描述这张图，原图附在消息后面，直接看原图）"
			if index < len(images) {
				fallback = append(fallback, availableImageURLs([]MessageSegment{images[index]})...)
			} else {
				fallback = append(fallback, extras[index-len(images)])
			}
		case seen[description] > 0:
			description = fmt.Sprintf("（和图片%d是同一张）", seen[description])
		default:
			seen[description] = index + 1
		}
		lines = append(lines, fmt.Sprintf("图片%d：%s", index+1, description))
	}
	text = strings.TrimSpace(text + "\n\n" + strings.Join(lines, "\n"))
	if len(fallback) == 0 {
		return llm.Message{Role: llm.RoleUser, Content: text}, nil
	}
	groups, failures := loadLLMImageURLGroupsDetailed(ctx, fallback)
	parts := []llm.ContentPart{{Type: llm.ContentPartText, Text: text}}
	for _, imageURL := range flattenLLMImageGroups(dedupeLLMImageGroups(groups)) {
		parts = append(parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: imageURL, Detail: detail})
	}
	return llm.Message{Role: llm.RoleUser, Content: text, Parts: parts}, failures
}

// replaceImageParts 是模型出口的兜底：拼消息时换不到的图（工具返回的截图、插件
// 附图、依赖图兜底）在这里逐张换成描述，保证一个像素都不进对话模型。
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
			// 描述拿不到就留着原图，让对话模型自己看，不能让它对着一张没看过的图作答。
			continue
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
	Usage:   "图片交付方式为自动时，对话模型调 history_media 带着问题来问，视觉理解按这段要求看图作答，回答以文字交回。",
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
