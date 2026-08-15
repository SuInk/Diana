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
	dianaHistoryImagesToolName      = "diana.history_images"
	maximumHistoryImagesPerToolCall = 8
)

type dianaHistoryImagesTool struct {
	runtime *Runtime
	event   MessageEvent

	mu          sync.Mutex
	resultParts []llm.ContentPart
}

type historyImageSelector struct {
	MessageID    string
	ImageIndexes []int
}

type dianaHistoryImagesResult struct {
	OK        bool                      `json:"ok"`
	Requested int                       `json:"requested"`
	Loaded    int                       `json:"loaded"`
	Failed    int                       `json:"failed"`
	Limited   bool                      `json:"limited,omitempty"`
	Images    []dianaHistoryImageStatus `json:"images"`
	Message   string                    `json:"message"`
}

type dianaHistoryImageStatus struct {
	MessageID  string `json:"message_id"`
	ImageIndex int    `json:"image_index,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

func newDianaHistoryImagesTool(runtime *Runtime, event MessageEvent) *dianaHistoryImagesTool {
	return &dianaHistoryImagesTool{runtime: runtime, event: event}
}

func (t *dianaHistoryImagesTool) Name() string {
	return dianaHistoryImagesToolName
}

func (t *dianaHistoryImagesTool) Description() string {
	return `按需读取当前会话历史消息中的原始图片，并把图片作为真实多模态附件交给下一轮模型。历史摘要足够回答时不要调用；需要辨认细小文字、比较多张图片或核对视觉细节时调用，并一次传入所有相关消息。只接受当前会话中真实存在的 message_id，不接受文件路径或 URL。单次最多读取 8 张以保证整批图片能完整进入模型；更大集合需分批读取。单张失效会跳过并报告，不会拖垮其他图片。input: {"message_ids":["消息1","消息2"],"detail":"auto|low|high"}；精确选图可用 {"items":[{"message_id":"消息1","image_indexes":[1,2]},{"message_id":"消息2"}],"detail":"high"}。省略 message_ids/items 时使用当前引用或语义来源。`
}

func (t *dianaHistoryImagesTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana history images: runtime is not configured")
	}
	t.setResultParts(nil)
	selectors, err := historyImageSelectors(input, t.event)
	if err != nil {
		return "", err
	}
	detail := normalizeHistoryImageDetail(configToolString(input, "detail"))
	result := dianaHistoryImagesResult{Images: make([]dianaHistoryImageStatus, 0)}
	parts := make([]llm.ContentPart, 0)
	attempted := 0

	for _, selector := range selectors {
		source, found, persistState := t.findSourceEvent(ctx, selector.MessageID)
		if !found {
			result.Requested++
			result.Failed++
			result.Images = append(result.Images, dianaHistoryImageStatus{
				MessageID: selector.MessageID,
				Status:    "failed",
				Error:     "当前会话中找不到这条消息",
			})
			continue
		}
		original := cloneHistoricalImageEvent(source)
		source = cloneHistoricalImageEvent(source)
		images := historicalStillImageRefs(source)
		if len(images) == 0 {
			result.Requested++
			result.Failed++
			result.Images = append(result.Images, dianaHistoryImageStatus{
				MessageID: selector.MessageID,
				Status:    "failed",
				Error:     "消息中没有原始图片",
			})
			continue
		}
		indexes, invalid := selectedHistoryImageIndexes(len(images), selector.ImageIndexes)
		for _, index := range invalid {
			result.Requested++
			result.Failed++
			result.Images = append(result.Images, dianaHistoryImageStatus{
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
				result.Images = append(result.Images, dianaHistoryImageStatus{
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
			if strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") {
				result.Failed++
				result.Images = append(result.Images, dianaHistoryImageStatus{
					MessageID:  selector.MessageID,
					ImageIndex: index,
					Status:     "failed",
					Error:      "原始图片已失效或无法恢复",
				})
				continue
			}
			segment = t.runtime.prepareHistoricalImageSegment(ctx, source, ref)
			setHistoricalStillImageSegment(&source, ref, segment)
			if strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") {
				result.Failed++
				result.Images = append(result.Images, dianaHistoryImageStatus{
					MessageID:  selector.MessageID,
					ImageIndex: index,
					Status:     "failed",
					Error:      "原始图片已失效或无法恢复",
				})
				continue
			}
			imageSources := availableImageURLs([]MessageSegment{segment})
			ready, complete := loadLLMImageURLs(ctx, imageSources)
			if !complete || len(ready) == 0 {
				result.Failed++
				result.Images = append(result.Images, dianaHistoryImageStatus{
					MessageID:  selector.MessageID,
					ImageIndex: index,
					Status:     "failed",
					Error:      "原始图片读取或编码失败",
				})
				continue
			}
			parts = append(parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: ready[0], Detail: detail})
			result.Loaded++
			result.Images = append(result.Images, dianaHistoryImageStatus{
				MessageID:  selector.MessageID,
				ImageIndex: index,
				Status:     "loaded",
			})
		}
		if persistState && historicalImageStateChanged(original, source) {
			t.runtime.updateHistoricalImageState(source)
		}
		t.runtime.enqueueHistoryImageDescriptions(source)
	}

	if result.Loaded == 0 {
		return "", fmt.Errorf("历史原图读取失败：请求的图片均不可用（%s）", historyImageFailureSummary(result.Images))
	}
	result.OK = true
	result.Message = fmt.Sprintf("已把 %d 张历史原图附加到本次工具观察，请逐张查看后再回答；不要把摘要当成原图细节。", result.Loaded)
	if result.Failed > 0 {
		result.Message += fmt.Sprintf(" 另有 %d 张读取失败，禁止推测其内容。", result.Failed)
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	t.setResultParts(parts)
	return string(body), nil
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
	return MessageEvent{}, false, false
}

func mergeFreshHistoricalImagePayload(stored, fresh MessageEvent) MessageEvent {
	merged := cloneHistoricalImageEvent(stored)
	storedRefs := historicalStillImageRefs(merged)
	for imageIndex, freshRef := range historicalStillImageRefs(fresh) {
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
		if !recallStillImageSegment(segment) || (firstImageSource(segment) == "" && strings.TrimSpace(segment.Data["file"]) == "") {
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
	for _, ref := range historicalStillImageRefs(event) {
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
			label += fmt.Sprintf(" image_index=%d", status.ImageIndex)
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

func historyImageSelectors(input map[string]any, event MessageEvent) ([]historyImageSelector, error) {
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
			indexes, err := positiveIntegerList(item["image_indexes"])
			if err != nil {
				return nil, fmt.Errorf("message_id=%s 的 image_indexes: %w", messageID, err)
			}
			selectors = append(selectors, historyImageSelector{MessageID: messageID, ImageIndexes: indexes})
		}
	}
	if len(selectors) == 0 {
		for _, messageID := range stringListFromAny(input["message_ids"]) {
			selectors = append(selectors, historyImageSelector{MessageID: messageID})
		}
		if messageID := strings.TrimSpace(configToolString(input, "message_id")); messageID != "" {
			indexes, err := positiveIntegerList(input["image_indexes"])
			if err != nil {
				return nil, fmt.Errorf("image_indexes: %w", err)
			}
			selectors = append(selectors, historyImageSelector{MessageID: messageID, ImageIndexes: indexes})
		}
	}
	if len(selectors) == 0 {
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
	var refs []historicalStillImageRef
	appendImages := func(items []MessageSegment, quoted bool) {
		for segmentIndex, segment := range items {
			if recallStillImageSegment(segment) {
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
