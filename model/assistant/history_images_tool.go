// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/llm"
)

const (
	dianaHistoryImagesToolName      = "history_media"
	maximumHistoryImagesPerToolCall = 8
)

type dianaHistoryImagesTool struct {
	runtime *Runtime
	event   MessageEvent
	// currentDescribed 表示这一轮的图在提示词里只有描述：画面不交给对话模型，
	// 自动档带着 question 交给视觉理解看、只回文字，仅文字描述档只回描述（见
	// image_input_mode.go）。不传消息 ID 时默认连当前这条一起读，按当前消息 ID
	// 也读得到——哪怕它还没进历史。
	currentDescribed bool
	imageMode        ImageInputMode

	mu          sync.Mutex
	resultParts []llm.ContentPart
}

type historyImageSelector struct {
	MessageID    string
	ImageIndexes []int
}

type dianaHistoryImagesResult struct {
	OK         bool                      `json:"ok"`
	Requested  int                       `json:"requested"`
	Loaded     int                       `json:"loaded"`
	FocusCrops int                       `json:"focus_crops,omitempty"`
	Failed     int                       `json:"failed"`
	Limited    bool                      `json:"limited,omitempty"`
	Media      []dianaHistoryImageStatus `json:"media"`
	Text       []string                  `json:"text,omitempty"`
	Answer     string                    `json:"answer,omitempty"`
	Message    string                    `json:"message"`
}

type dianaHistoryImageStatus struct {
	MessageID  string `json:"message_id"`
	ImageIndex int    `json:"media_index,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

func newDianaHistoryImagesTool(runtime *Runtime, event MessageEvent) *dianaHistoryImagesTool {
	return &dianaHistoryImagesTool{runtime: runtime, event: event}
}

// withImageInput 按这一轮的图片交付方式切换工具的形态。
func (t *dianaHistoryImagesTool) withImageInput(currentDescribed bool, mode ImageInputMode) *dianaHistoryImagesTool {
	t.currentDescribed = currentDescribed
	t.imageMode = normalizeImageInputMode(mode)
	return t
}

// asksVision 是自动档：带着问题让视觉理解看图作答。
func (t *dianaHistoryImagesTool) asksVision() bool {
	return t.currentDescribed && t.imageMode == ImageInputModeAuto
}

// describesOnly 是仅文字描述档：只回描述，不再看图。
func (t *dianaHistoryImagesTool) describesOnly() bool {
	return t.currentDescribed && t.imageMode == ImageInputModeText
}

func (t *dianaHistoryImagesTool) Name() string {
	return dianaHistoryImagesToolName
}

func (t *dianaHistoryImagesTool) Description() string {
	if t.describesOnly() {
		return `读取当前会话历史消息里图片或视频关键帧的文字描述，以及文件的正文。你看不到原图，只能拿到视觉模型写的描述；消息里已经带着的描述就不必再调用。一次传入所有相关消息。`
	}
	if t.asksVision() {
		return `看当前会话历史消息里的原始图片或视频关键帧并回答问题。你看不到原图，只有视觉模型写的摘要；摘要够用时不要调用。需要辨认小字、数数量、比较画面、认出是谁或核对摘要是否说对时调用：在 question 里写清要看什么，视觉模型会看图作答，只返回文字。历史消息里的文件也用它读正文。一次传入所有相关消息。`
	}
	return `读取当前会话历史消息里的原始图片或按需提取的视频关键帧，作为真实多模态附件交给下一轮模型。历史摘要够用时不要调用；需要辨认小字、比较画面或核对视频细节时才调用，并一次传入所有相关消息。单张失效会跳过并报告，不影响其他画面。历史消息里的文件也用它读正文。`
}

func (t *dianaHistoryImagesTool) InputSchema() map[string]any {
	properties := map[string]any{
		"message_id":    toolStringParam("只读一条消息时用它：该消息的 ID，只接受当前会话中真实存在的 message_id，不接受文件路径或 URL。"),
		"media_indexes": map[string]any{"type": "array", "description": "配合 message_id 使用：要读取的图片或视频关键帧序号，从 1 开始；省略表示全部画面。", "items": map[string]any{"type": "integer", "minimum": 1}},
		"message_ids":   toolStringArrayParam("一次读多条消息时用它：消息 ID 数组。省略 message_id、message_ids 和 items 时使用当前引用或语义来源。"),
		"items": toolItemsParam("需要精确指定某条消息里的第几个画面时改用它，与 message_ids 二选一。",
			maximumHistoryImagesPerToolCall,
			[]string{"message_id"},
			map[string]any{
				"message_id":    toolStringParam("消息 ID。"),
				"media_indexes": map[string]any{"type": "array", "description": "要读取的图片或视频关键帧序号，从 1 开始；省略表示该消息里的全部画面。文件与音频无需指定序号。", "items": map[string]any{"type": "integer", "minimum": 1}},
			}),
		"detail": toolEnumParam("图片细节档位。auto 由运行时按预算决定；辨认细小文字时用 high。", "auto", "low", "high"),
	}
	if t.asksVision() {
		properties["question"] = toolStringParam("要视觉模型看图回答的问题，写清要看哪里、要什么，例如「第二张图右下角的价格是多少」「这是猫还是狗」。省略时返回画面的完整描述。")
	}
	return toolObjectSchema(nil, properties)
}

func (t *dianaHistoryImagesTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana history images: runtime is not configured")
	}
	t.setResultParts(nil)
	selectors, err := historyImageSelectors(input, t.event, t.currentDescribed)
	if err != nil {
		return "", err
	}
	detail := normalizeHistoryImageDetail(configToolString(input, "detail"))
	result := dianaHistoryImagesResult{Media: make([]dianaHistoryImageStatus, 0)}
	parts := make([]llm.ContentPart, 0)
	var loaded []historyLoadedImage
	var files []historyFileSource
	attempted := 0

	followed := make(map[string]bool, len(selectors))
	for _, selector := range selectors {
		followed[selector.MessageID] = true
	}
	for position := 0; position < len(selectors); position++ {
		selector := selectors[position]
		source, found, persistState := t.findSourceEvent(ctx, selector.MessageID)
		if !found {
			result.Requested++
			result.Failed++
			result.Media = append(result.Media, dianaHistoryImageStatus{
				MessageID: selector.MessageID,
				Status:    "failed",
				Error:     "当前会话中找不到这条消息",
			})
			continue
		}
		original := cloneHistoricalImageEvent(source)
		source = cloneHistoricalImageEvent(source)
		source = t.runtime.prepareRequestedVideoFrames(ctx, source)
		textSegments := append([]MessageSegment(nil), source.Segments...)
		if source.Quoted != nil {
			textSegments = append(textSegments, source.Quoted.Segments...)
		}
		result.Text = append(result.Text, historicalAudioDescriptions(textSegments)...)
		files = append(files, historyFileSources(selector.MessageID, source)...)
		images := historicalToolImageRefs(source)
		if len(images) == 0 && len(historicalNonImageMediaDescriptions(textSegments)) == 0 {
			// 「发张图，接着问这是什么」那句文字本身不带图，图在它问的那条媒体消息里。
			// 模型拿这句话的 ID 来取图时，顺着找过去，而不是报没有图。
			if sources := unfollowedSemanticSources(source, followed); len(sources) > 0 {
				for _, sourceID := range sources {
					selectors = append(selectors, historyImageSelector{MessageID: sourceID})
				}
				continue
			}
			result.Requested++
			result.Failed++
			result.Media = append(result.Media, dianaHistoryImageStatus{
				MessageID: selector.MessageID,
				Status:    "failed",
				Error:     "消息中没有原始图片或按需提取的视频关键帧",
			})
			continue
		}
		indexes, invalid := selectedHistoryImageIndexes(len(images), selector.ImageIndexes)
		for _, index := range invalid {
			result.Requested++
			result.Failed++
			result.Media = append(result.Media, dianaHistoryImageStatus{
				MessageID:  selector.MessageID,
				ImageIndex: index,
				Status:     "failed",
				Error:      "图片序号不存在",
			})
		}
		for _, index := range indexes {
			result.Requested++
			if attempted >= maximumHistoryImagesPerToolCall {
				result.Limited = true
				result.Failed++
				result.Media = append(result.Media, dianaHistoryImageStatus{
					MessageID:  selector.MessageID,
					ImageIndex: index,
					Status:     "skipped",
					Error:      "超过单次 8 张图片上限，请分批读取",
				})
				continue
			}
			attempted++
			ref := images[index-1]
			segment := ref.segment
			mediaType := historicalToolImageMediaType(segment)
			if strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") {
				result.Failed++
				result.Media = append(result.Media, dianaHistoryImageStatus{
					MessageID:  selector.MessageID,
					ImageIndex: index,
					MediaType:  mediaType,
					Status:     "failed",
					Error:      "原始图片已失效或无法恢复",
				})
				continue
			}
			segment = t.runtime.prepareHistoricalImageSegment(ctx, source, ref)
			setHistoricalStillImageSegment(&source, ref, segment)
			if strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") {
				result.Failed++
				result.Media = append(result.Media, dianaHistoryImageStatus{
					MessageID:  selector.MessageID,
					ImageIndex: index,
					MediaType:  mediaType,
					Status:     "failed",
					Error:      "原始图片已失效或无法恢复",
				})
				continue
			}
			imageSources := availableImageURLs([]MessageSegment{segment})
			ready, complete := loadLLMImageURLs(ctx, imageSources)
			if !complete || len(ready) == 0 {
				result.Failed++
				result.Media = append(result.Media, dianaHistoryImageStatus{
					MessageID:  selector.MessageID,
					ImageIndex: index,
					MediaType:  mediaType,
					Status:     "failed",
					Error:      "原始图片读取或编码失败",
				})
				continue
			}
			imageParts := make([]llm.ContentPart, 0, len(ready))
			if len(ready) == 1 {
				imageParts = highDetailImageParts(ready[0], detail)
			} else {
				for _, imageURL := range ready {
					imageParts = append(imageParts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: imageURL, Detail: detail})
				}
			}
			parts = append(parts, imageParts...)
			loaded = append(loaded, historyLoadedImage{messageID: selector.MessageID, index: index, segment: segment})
			result.FocusCrops += len(imageParts) - 1
			result.Loaded++
			result.Media = append(result.Media, dianaHistoryImageStatus{
				MessageID:  selector.MessageID,
				ImageIndex: index,
				MediaType:  mediaType,
				Status:     "loaded",
			})
		}
		if persistState && historicalImageStateChanged(original, source) {
			t.runtime.updateHistoricalImageState(source)
		}
		t.runtime.enqueueHistoryImageDescriptionsNow(source)
	}

	result.Text = append(result.Text, t.historyFileTexts(ctx, files)...)
	if result.Loaded == 0 && len(result.Text) == 0 {
		return "", fmt.Errorf("历史媒体读取失败：请求的媒体均不可用（%s）", historyImageFailureSummary(result.Media))
	}
	result.OK = true
	result.Message = fmt.Sprintf("已读取 %d 张历史图片或视频关键帧和 %d 条文件/语音文字信息。", result.Loaded, len(result.Text))
	if result.Loaded > 0 {
		result.Message += " 真实画面已附加到本次工具观察，请逐张查看后再回答；不要把摘要当成真实画面细节。"
	}
	if result.FocusCrops > 0 {
		result.Message += fmt.Sprintf(" 为了保留小人像和小字细节，已另附加 %d 张原图局部裁剪。", result.FocusCrops)
	}
	if result.Failed > 0 {
		result.Message += fmt.Sprintf(" 另有 %d 张读取失败，禁止推测其内容。", result.Failed)
	}
	if t.describesOnly() && len(loaded) > 0 {
		lines := make([]string, 0, len(loaded))
		mode := imageTextModeFromContext(ctx)
		for _, item := range loaded {
			description := ""
			if mode != nil {
				description = mode.segmentDescription(ctx, item.segment)
			}
			if description == "" {
				description = "（未能识别出这张图的内容，不要猜它画了什么）"
			}
			lines = append(lines, fmt.Sprintf("message_id=%s 第%d张：%s", item.messageID, item.index, description))
		}
		result.Text = append(result.Text, lines...)
		result.Message = fmt.Sprintf("已取得 %d 张历史图片或视频关键帧的文字描述，在 text 里；描述由视觉模型写成，可能有误。", len(lines))
		if result.Failed > 0 {
			result.Message += fmt.Sprintf(" 另有 %d 张读取失败，禁止推测其内容。", result.Failed)
		}
		parts = nil
	}
	if t.asksVision() && len(parts) > 0 {
		answer, err := t.runtime.askImages(ctx, t.event, parts, configToolString(input, "question"))
		if err != nil {
			return "", fmt.Errorf("历史媒体看图失败：%w", err)
		}
		result.Answer = answer
		result.Message = fmt.Sprintf("视觉模型看了 %d 张历史图片或视频关键帧，回答在 answer 里；它也可能看错，不确定的地方照实说。", result.Loaded)
		if result.Failed > 0 {
			result.Message += fmt.Sprintf(" 另有 %d 张读取失败，禁止推测其内容。", result.Failed)
		}
		parts = nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	t.setResultParts(parts)
	return string(body), nil
}

type historyFileSource struct {
	messageID string
	groupID   string
	index     int
	segment   MessageSegment
}

func historyFileSources(messageID string, event MessageEvent) []historyFileSource {
	var files []historyFileSource
	add := func(groupID string, segments []MessageSegment) {
		for _, segment := range segments {
			if segment.Type == "file" {
				files = append(files, historyFileSource{messageID: messageID, groupID: groupID, index: len(files) + 1, segment: segment})
			}
		}
	}
	add(event.GroupID, event.Segments)
	if event.Quoted != nil {
		add(firstNonEmpty(event.Quoted.GroupID, event.GroupID), event.Quoted.Segments)
	}
	return files
}

// historyFileTexts 把历史消息里的文件交给文件解析插件读出正文。
//
// 解析插件只看当前消息和引用消息，历史里的文件以前只回文件名加「正文尚未解析」，
// 模型翻到了也读不了。这里按插件的开关和设置走同一套下载与解析，一次调用里的
// 文件共用一轮的字数预算，不会因为读了几条历史就挤掉对话上下文。
func (t *dianaHistoryImagesTool) historyFileTexts(ctx context.Context, files []historyFileSource) []string {
	if len(files) == 0 {
		return nil
	}
	var parser *FileParserPlugin
	var settings SettingValues
	if plugin, values, enabled := t.runtime.pluginWithSettingsForEvent(fileParserPluginID, t.event); enabled {
		parser, _ = plugin.(*FileParserPlugin)
		settings = values
	}
	refs := make([]*fileRef, len(files))
	parsable := 0
	if parser != nil {
		for index, file := range files {
			found := collectFileRefs(PluginRequest{Event: MessageEvent{ProfileID: t.event.ProfileID, Platform: t.event.Platform, GroupID: file.groupID, Segments: []MessageSegment{file.segment}}})
			if len(found) == 1 {
				refs[index] = &found[0]
				parsable++
			}
		}
	}
	lines := make([]string, 0, len(files)+1)
	if parsable == 0 {
		for _, file := range files {
			lines = append(lines, fmt.Sprintf("message_id=%s %s", file.messageID, historicalFileDescription(file.index, file.segment)))
		}
		return lines
	}
	maxBytes := settings.Bytes(fileParserSettingMaxFileBytes, parser.maxBytes)
	maxChars, expand := fileParserTurnBudget(settings.Int(fileParserSettingMaxChars, parser.maxChars), parsable)
	channel, _, err := t.runtime.outboundChannelForEvent(t.event)
	if err != nil {
		channel = nil
	}
	lines = append(lines, strings.TrimSpace(fileContentNotice))
	parsed := 0
	for index, file := range files {
		ref := refs[index]
		if ref == nil {
			lines = append(lines, fmt.Sprintf("message_id=%s %s", file.messageID, historicalFileDescription(file.index, file.segment)))
			continue
		}
		if parsed >= expand {
			lines = append(lines, fmt.Sprintf("message_id=%s 文件%d\n- %s\n  状态：这次读的文件太多，为了不挤掉对话上下文没有展开；需要时单独读这条消息", file.messageID, file.index, ref.Name))
			continue
		}
		parsed++
		result := parser.parseRef(ctx, channel, *ref, maxBytes, maxChars)
		body := result.Context
		if result.ScannedPDF != nil {
			body = fmt.Sprintf("- %s\n  状态：扫描版 PDF，没有文字层，这里读不出正文；让用户引用这条文件消息再问，会转成 OCR 子任务识别", ref.Name)
		}
		lines = append(lines, fmt.Sprintf("message_id=%s 文件%d\n%s", file.messageID, file.index, body))
	}
	return lines
}

type historyLoadedImage struct {
	messageID string
	index     int
	segment   MessageSegment
}

// unfollowedSemanticSources 取出这条消息指向、还没读过的来源消息，并记成已读，
// 免得两条消息互相指着绕圈。
func unfollowedSemanticSources(event MessageEvent, followed map[string]bool) []string {
	var sources []string
	for _, sourceID := range eventSemanticSourceMessageIDs(event) {
		sourceID = strings.TrimSpace(sourceID)
		if sourceID == "" || followed[sourceID] {
			continue
		}
		followed[sourceID] = true
		sources = append(sources, sourceID)
	}
	return sources
}

func (t *dianaHistoryImagesTool) findSourceEvent(ctx context.Context, messageID string) (MessageEvent, bool, bool) {
	messageID = strings.TrimSpace(messageID)
	stored, storedFound := t.runtime.findSemanticReferenceEvent(ctx, t.event, messageID)
	if t.event.Quoted != nil && strings.TrimSpace(t.event.Quoted.MessageID) == messageID {
		quoted := t.event.Quoted
		source := MessageEvent{
			Platform:         t.event.Platform,
			ProfileID:        t.event.ProfileID,
			ContextNamespace: t.event.ContextNamespace,
			Kind:             t.event.Kind,
			GroupID:          firstNonEmpty(quoted.GroupID, t.event.GroupID),
			UserID:           firstNonEmpty(quoted.UserID, t.event.UserID),
			MessageID:        quoted.MessageID,
			RawMessage:       quoted.RawMessage,
			Segments:         quoted.Segments,
			SenderName:       quoted.SenderName,
		}
		// The quote carried by this turn is the freshest OneBot payload. Merge it
		// with the persisted copy so a stable local cache remains the first fallback.
		if historicalEventHasCurrentImagePayload(source) {
			fresh := eventWithFreshHistoricalImagePayload(source)
			if storedFound {
				return mergeFreshHistoricalImagePayload(stored, fresh), true, true
			}
			return fresh, true, false
		}
	}
	if storedFound {
		return stored, true, true
	}
	// 当前这条的图在提示词里只有描述，模型回头要看原图时它可能还没进历史。
	if t.currentDescribed && strings.TrimSpace(t.event.MessageID) == messageID {
		return cloneHistoricalImageEvent(t.event), true, false
	}
	return MessageEvent{}, false, false
}

func mergeFreshHistoricalImagePayload(stored, fresh MessageEvent) MessageEvent {
	merged := cloneHistoricalImageEvent(stored)
	storedRefs := historicalToolImageRefs(merged)
	for imageIndex, freshRef := range historicalToolImageRefs(fresh) {
		freshSegment := freshRef.segment
		if imageIndex >= len(storedRefs) {
			merged.Segments = append(merged.Segments, freshSegment)
			continue
		}
		storedRef := storedRefs[imageIndex]
		data := cloneSegmentData(storedRef.segment.Data)
		stableCachedFile := normalizedLocalImagePath(data["cached_file"])
		stableHash := strings.ToLower(strings.TrimSpace(data[imageContentSHA256Key]))
		for index := 1; index <= 8; index++ {
			delete(data, fmt.Sprintf("%s%d", imageResolvedSourceKey, index))
		}
		for key, value := range freshSegment.Data {
			data[key] = value
		}
		if stableCachedFile != "" {
			data["cached_file"] = stableCachedFile
		}
		if validSHA256(stableHash) {
			data[imageContentSHA256Key] = stableHash
		}
		delete(data, imageUnavailableKey)
		delete(data, imageSourceFailedKey)
		storedRef.segment.Data = data
		setHistoricalStillImageSegment(&merged, storedRef, storedRef.segment)
	}
	return merged
}

func eventWithFreshHistoricalImagePayload(event MessageEvent) MessageEvent {
	event = cloneHistoricalImageEvent(event)
	for index, segment := range event.Segments {
		if !historyDescribableImageSegment(segment) || (firstImageSource(segment) == "" && strings.TrimSpace(segment.Data["file"]) == "") {
			continue
		}
		data := cloneSegmentData(segment.Data)
		delete(data, imageUnavailableKey)
		delete(data, imageSourceFailedKey)
		event.Segments[index].Data = data
	}
	return event
}

func historicalEventHasCurrentImagePayload(event MessageEvent) bool {
	for _, ref := range historicalToolImageRefs(event) {
		if firstImageSource(ref.segment) != "" || strings.TrimSpace(ref.segment.Data["file"]) != "" {
			return true
		}
	}
	return false
}

func historyImageFailureSummary(statuses []dianaHistoryImageStatus) string {
	parts := make([]string, 0, min(len(statuses), 8))
	for _, status := range statuses {
		if len(parts) >= 8 {
			break
		}
		label := "message_id=" + status.MessageID
		if status.ImageIndex > 0 {
			label += fmt.Sprintf(" media_index=%d", status.ImageIndex)
		}
		parts = append(parts, label+": "+firstNonEmpty(status.Error, "读取失败"))
	}
	if len(parts) == 0 {
		return "没有可读取的图片"
	}
	return strings.Join(parts, "；")
}

func (t *dianaHistoryImagesTool) ToolResultParts(string) []llm.ContentPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.ContentPart(nil), t.resultParts...)
}

func (t *dianaHistoryImagesTool) setResultParts(parts []llm.ContentPart) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resultParts = append([]llm.ContentPart(nil), parts...)
}

func historyImageSelectors(input map[string]any, event MessageEvent, includeCurrent bool) ([]historyImageSelector, error) {
	var selectors []historyImageSelector
	if rawItems, ok := input["items"]; ok {
		items, ok := rawItems.([]any)
		if !ok {
			return nil, fmt.Errorf("items 必须是数组")
		}
		for _, rawItem := range items {
			item, ok := rawItem.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("items 中每一项都必须是对象")
			}
			messageID := strings.TrimSpace(configToolString(item, "message_id"))
			if messageID == "" {
				return nil, fmt.Errorf("items 中的 message_id 不能为空")
			}
			indexes, err := positiveIntegerList(item["media_indexes"])
			if err != nil {
				return nil, fmt.Errorf("message_id=%s 的 media_indexes: %w", messageID, err)
			}
			selectors = append(selectors, historyImageSelector{MessageID: messageID, ImageIndexes: indexes})
		}
	}
	if len(selectors) == 0 {
		for _, messageID := range stringListFromAny(input["message_ids"]) {
			selectors = append(selectors, historyImageSelector{MessageID: messageID})
		}
		if messageID := strings.TrimSpace(configToolString(input, "message_id")); messageID != "" {
			indexes, err := positiveIntegerList(input["media_indexes"])
			if err != nil {
				return nil, fmt.Errorf("media_indexes: %w", err)
			}
			selectors = append(selectors, historyImageSelector{MessageID: messageID, ImageIndexes: indexes})
		}
	}
	if len(selectors) == 0 {
		if includeCurrent && hasImageSegment(event.Segments) {
			selectors = append(selectors, historyImageSelector{MessageID: event.MessageID})
		}
		for _, messageID := range eventSemanticSourceMessageIDs(event) {
			selectors = append(selectors, historyImageSelector{MessageID: messageID})
		}
		if event.Quoted != nil && strings.TrimSpace(event.Quoted.MessageID) != "" {
			selectors = append(selectors, historyImageSelector{MessageID: event.Quoted.MessageID})
		}
	}
	selectors = mergeHistoryImageSelectors(selectors)
	if len(selectors) == 0 {
		return nil, fmt.Errorf("需要 message_ids 或 items；当前消息也没有可用引用")
	}
	return selectors, nil
}

func mergeHistoryImageSelectors(selectors []historyImageSelector) []historyImageSelector {
	positions := make(map[string]int)
	out := make([]historyImageSelector, 0, len(selectors))
	for _, selector := range selectors {
		selector.MessageID = strings.TrimSpace(selector.MessageID)
		if selector.MessageID == "" {
			continue
		}
		position, found := positions[selector.MessageID]
		if !found {
			positions[selector.MessageID] = len(out)
			out = append(out, selector)
			continue
		}
		if len(out[position].ImageIndexes) == 0 || len(selector.ImageIndexes) == 0 {
			out[position].ImageIndexes = nil
			continue
		}
		out[position].ImageIndexes = uniqueSortedPositiveIntegers(append(out[position].ImageIndexes, selector.ImageIndexes...))
	}
	return out
}

func stringListFromAny(value any) []string {
	var out []string
	switch items := value.(type) {
	case []any:
		for _, item := range items {
			if text := strings.TrimSpace(stringFromAny(item)); text != "" {
				out = append(out, text)
			}
		}
	case []string:
		for _, item := range items {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
	case string:
		if items = strings.TrimSpace(items); items != "" {
			out = append(out, items)
		}
	}
	return uniqueNonEmptyStrings(out...)
}

func positiveIntegerList(value any) ([]int, error) {
	if value == nil {
		return nil, nil
	}
	var raw []any
	switch items := value.(type) {
	case []any:
		raw = items
	case []int:
		for _, item := range items {
			raw = append(raw, item)
		}
	default:
		raw = []any{value}
	}
	values := make([]int, 0, len(raw))
	for _, item := range raw {
		value := intFromAny(item)
		if value <= 0 {
			return nil, fmt.Errorf("必须使用从 1 开始的正整数序号")
		}
		values = append(values, value)
	}
	return uniqueSortedPositiveIntegers(values), nil
}

func uniqueSortedPositiveIntegers(values []int) []int {
	seen := make(map[int]bool, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func selectedHistoryImageIndexes(count int, requested []int) (selected, invalid []int) {
	if len(requested) == 0 {
		selected = make([]int, count)
		for index := range selected {
			selected[index] = index + 1
		}
		return selected, nil
	}
	for _, index := range requested {
		if index > count {
			invalid = append(invalid, index)
			continue
		}
		selected = append(selected, index)
	}
	return selected, invalid
}

func historicalStillImageSegments(event MessageEvent) []MessageSegment {
	refs := historicalStillImageRefs(event)
	segments := make([]MessageSegment, 0, len(refs))
	for _, ref := range refs {
		segments = append(segments, ref.segment)
	}
	return segments
}

func cloneHistoricalImageEvent(event MessageEvent) MessageEvent {
	event.Segments = cloneMessageSegments(event.Segments)
	if event.Quoted != nil {
		quoted := *event.Quoted
		quoted.Segments = cloneMessageSegments(quoted.Segments)
		event.Quoted = &quoted
	}
	return event
}

func cloneMessageSegments(segments []MessageSegment) []MessageSegment {
	out := make([]MessageSegment, len(segments))
	for index, segment := range segments {
		out[index] = MessageSegment{Type: segment.Type, Data: cloneSegmentData(segment.Data)}
	}
	return out
}

type historicalStillImageRef struct {
	segment      MessageSegment
	segmentIndex int
	quoted       bool
}

func historicalStillImageRefs(event MessageEvent) []historicalStillImageRef {
	return historicalImageRefsMatching(event, recallStillImageSegment)
}

func historicalToolImageRefs(event MessageEvent) []historicalStillImageRef {
	return historicalImageRefsMatching(event, historyDescribableImageSegment)
}

func historicalImageRefsMatching(event MessageEvent, matches func(MessageSegment) bool) []historicalStillImageRef {
	var refs []historicalStillImageRef
	appendImages := func(items []MessageSegment, quoted bool) {
		for segmentIndex, segment := range items {
			if matches(segment) {
				refs = append(refs, historicalStillImageRef{segment: segment, segmentIndex: segmentIndex, quoted: quoted})
			}
		}
	}
	appendImages(event.Segments, false)
	if event.Quoted != nil {
		appendImages(event.Quoted.Segments, true)
	}
	return refs
}

func historicalToolImageMediaType(segment MessageSegment) string {
	if strings.EqualFold(strings.TrimSpace(segment.Data["source_type"]), "video_frame") {
		return "video_frame"
	}
	return "image"
}

func (r *Runtime) prepareHistoricalImageSegment(ctx context.Context, event MessageEvent, ref historicalStillImageRef) MessageSegment {
	item := event
	if ref.quoted && event.Quoted != nil {
		item.GroupID = firstNonEmpty(event.Quoted.GroupID, event.GroupID)
		item.UserID = firstNonEmpty(event.Quoted.UserID, event.UserID)
		item.MessageID = event.Quoted.MessageID
	}
	item.Segments = []MessageSegment{ref.segment}
	item.Quoted = nil
	prepared := r.prepareHistoricalImageSegments(ctx, item, item.Segments)
	if len(prepared) != 1 {
		return ref.segment
	}
	return prepared[0]
}

func setHistoricalStillImageSegment(event *MessageEvent, ref historicalStillImageRef, segment MessageSegment) {
	if event == nil {
		return
	}
	if ref.quoted {
		if event.Quoted != nil && ref.segmentIndex >= 0 && ref.segmentIndex < len(event.Quoted.Segments) {
			event.Quoted.Segments[ref.segmentIndex] = segment
		}
		return
	}
	if ref.segmentIndex >= 0 && ref.segmentIndex < len(event.Segments) {
		event.Segments[ref.segmentIndex] = segment
	}
}

func normalizeHistoryImageDetail(detail string) string {
	switch strings.ToLower(strings.TrimSpace(detail)) {
	case "low", "high":
		return strings.ToLower(strings.TrimSpace(detail))
	default:
		return "auto"
	}
}
