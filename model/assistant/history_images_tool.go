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
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

const dianaHistoryImagesToolName = "history_media"

type dianaHistoryImagesTool struct {
	runtime *Runtime
	event   MessageEvent
	// currentDescribed 表示这一轮的图在提示词里只有描述：画面不交给对话模型，
	// 自动档带着 question 交给视觉理解看、只回文字，仅文字描述档只回描述（见
	// image_input_mode.go）。不传消息 ID 时默认连当前这条一起读，按当前消息 ID
	// 也读得到——哪怕它还没进历史。
	currentDescribed bool
	imageMode        ImageInputMode

	mu           sync.Mutex
	resultParts  []llm.ContentPart
	pdfPageTexts map[string]string
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
	Media      []dianaHistoryImageStatus `json:"media"`
	Text       []string                  `json:"text,omitempty"`
	Answer     string                    `json:"answer,omitempty"`
	// HasMore/RemainingCount/NextItems 是整张分页：这次装不下的画面一张不丢，
	// 按消息列进 next_items，原样传给 items 就能接着读。
	HasMore        bool                   `json:"has_more,omitempty"`
	RemainingCount int                    `json:"remaining_count,omitempty"`
	NextItems      []historyMediaNextItem `json:"next_items,omitempty"`
	Message        string                 `json:"message"`
}

type historyMediaNextItem struct {
	MessageID    string `json:"message_id"`
	MediaIndexes []int  `json:"media_indexes"`
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
		return `读取历史消息里图片/视频关键帧的文字描述和文件正文。你看不到原图；消息已带描述就不必调用。PDF 用 pdf_page/page_offset 逐页续读；每次指定一个文件。相关消息一次传齐；一次装不下时按整张分页，把返回的 next_items 原样传给 items 接着读。`
	}
	if t.asksVision() {
		return `让视觉模型看历史消息里的原图/视频关键帧并回答 question，只返回文字。摘要够用时别调；认小字、数数、比较画面、认人或核对摘要时用，没调过不算看过原图。历史消息里的文件也用它读正文；PDF 长文档用 pdf_page/page_offset 逐页续读，不能把截断当成全文。相关消息一次传齐，单项失败别推测它的细节；一次装不下时按整张分页，把返回的 next_items 原样传给 items 接着读。`
	}
	return `把历史消息里的原图/视频关键帧作为附件交给下一轮模型。摘要够用时别调；认小字、比较画面、核对视频细节时用，没调过不算看过原图。相关消息一次传齐，单张失效会跳过，别推测它的细节。历史消息里的文件也用它读正文；PDF 长文档用 pdf_page/page_offset 逐页续读，不能把截断当成全文。一次装不下时按整张分页，把返回的 next_items 原样传给 items 接着读。`
}

func (t *dianaHistoryImagesTool) InputSchema() map[string]any {
	properties := map[string]any{
		"file_index":    toolIntParam("只读消息内第几个文件（配合 pdf_page 或 start_line），默认 1", 1, 1000),
		"start_line":    toolIntParam("文件正文续读：从第几行起，按返回的续读说明传", 1, 100000000),
		"pdf_page":      toolIntParam("读取 PDF 指定页，从 1 起；长文档从第 1 页开始，按返回的 next_page 继续", 1, 100000),
		"page_offset":   toolIntParam("页内字符偏移，从 0 起；同一页未读完时用返回的 next_offset 续读", 0, 100000000),
		"message_id":    toolStringParam("单条消息 ID，不接受路径或 URL"),
		"media_indexes": map[string]any{"type": "array", "description": "配合 message_id：画面序号，从 1 起，省略为全部", "items": map[string]any{"type": "integer", "minimum": 1}},
		"message_ids":   toolStringArrayParam("多条消息 ID；三种都省略时用当前引用"),
		// 不设条数上限：一次装不下就按整张分页，结果里的 next_items 和这里同形，原样传回即可。
		"items": describedParam("逐条指定画面序号，与 message_ids 二选一；续读时传上次返回的 next_items", map[string]any{
			"type": "array",
			"items": toolObjectSchema([]string{"message_id"}, map[string]any{
				"message_id":    toolStringParam("消息 ID"),
				"media_indexes": map[string]any{"type": "array", "description": "画面序号，从 1 起，省略为全部", "items": map[string]any{"type": "integer", "minimum": 1}},
			}),
		}),
		"detail": toolEnumParam("细节档位，认小字用 high", "auto", "low", "high"),
	}
	if t.asksVision() {
		properties["question"] = toolStringParam("要看哪里、问什么，只问画面本身（谁、什么字、什么状态），群聊里在聊什么不用它解读；省略则返回完整描述")
	}
	return toolObjectSchema(nil, properties)
}

func (t *dianaHistoryImagesTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana history images: runtime is not configured")
	}
	t.setResultParts(nil)
	if input["pdf_page"] != nil {
		for _, key := range []string{"pdf_page", "page_offset", "file_index"} {
			if value, exists := input[key]; exists {
				n, err := groupToolInteger(value)
				if err != nil || n < 0 || (key != "page_offset" && n == 0) {
					return "", fmt.Errorf("%s 不是有效的整数", key)
				}
			}
		}
	} else if input["page_offset"] != nil {
		return "", fmt.Errorf("page_offset 必须与 pdf_page 一起使用")
	}
	for _, key := range []string{"file_index", "start_line"} {
		if value, exists := input[key]; exists && value != nil {
			if n, err := groupToolInteger(value); err != nil || n <= 0 {
				return "", fmt.Errorf("%s 不是有效的正整数", key)
			}
		}
	}
	selectors, err := historyImageSelectors(input, t.event, t.currentDescribed)
	if err != nil {
		return "", err
	}
	if (input["pdf_page"] != nil || input["file_index"] != nil || input["start_line"] != nil) && len(selectors) != 1 {
		return "", fmt.Errorf("pdf_page、file_index、start_line 每次只指定一条消息，再用 file_index 选择文件")
	}
	detail := normalizeHistoryImageDetail(configToolString(input, "detail"))
	result := dianaHistoryImagesResult{Media: make([]dianaHistoryImageStatus, 0)}
	var loaded []historyLoadedImage
	var files []historyFileSource

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
		if input["pdf_page"] != nil {
			continue
		}
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
				Error:     firstNonEmpty(telegramOversizedMediaReason(source), "消息中没有原始图片或按需提取的视频关键帧"),
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
			loaded = append(loaded, historyLoadedImage{messageID: selector.MessageID, index: index, segment: segment, parts: imageParts, mediaPos: len(result.Media)})
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

	result.Text = append(result.Text, t.historyFileTexts(ctx, files, input)...)
	if len(loaded) == 0 && len(result.Text) == 0 {
		return "", fmt.Errorf("历史媒体读取失败：请求的媒体均不可用（%s）", historyImageFailureSummary(result.Media))
	}
	budget := agent.ToolOutputBudget(ctx)
	if budget <= 0 {
		budget = agent.ToolOutputMaxChars
	}
	result, kept := t.historyMediaPage(ctx, result, loaded, budget)
	var parts []llm.ContentPart
	if !t.describesOnly() {
		for _, item := range kept {
			parts = append(parts, item.parts...)
		}
	}
	if t.asksVision() && len(parts) > 0 {
		answer, err := t.runtime.askImages(ctx, t.event, parts, configToolString(input, "question"))
		if err != nil {
			return "", fmt.Errorf("历史媒体看图失败：%w", err)
		}
		result.Answer = answer
		result.Message = historyMediaVisionAnswerMessage(result.Loaded) + historyMediaFailedNotice(result) + historyMediaRemainingNotice(result)
		parts = nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	t.setResultParts(parts)
	return string(body), nil
}

// historyMediaPage 照 Pi 的做法把读到的画面装进单次结果上限：从前往后按整张收，
// 收下一张会让结果超出 budget 就停，剩下的按消息列进 next_items，不缩短描述、不
// 丢图；至少收一张，免得一张大描述永远读不出来。画面本身作为附件交给模型，不占
// 文字字数，它的体积由主回复请求统一的图片预算处理（image_budget.go），这里只按
// 结果 JSON 的字数分页——仅文字描述档里描述进 text，最容易撑满。
//
// 以前一次最多 8 张，第 9 张起标成 skipped 计入失败，模型常把没看到的当成读不出来。
func (t *dianaHistoryImagesTool) historyMediaPage(ctx context.Context, base dianaHistoryImagesResult, loaded []historyLoadedImage, budget int) (dianaHistoryImagesResult, []historyLoadedImage) {
	var mode *imageTextMode
	if t.describesOnly() {
		mode = imageTextModeFromContext(ctx)
	}
	descriptions := make([]string, 0, len(loaded))
	best := t.historyMediaRender(base, loaded, 0, nil)
	kept := 0
	for keep := 1; keep <= len(loaded); keep++ {
		if t.describesOnly() {
			item := loaded[keep-1]
			description := ""
			if mode != nil {
				description = mode.segmentDescription(ctx, item.segment)
			}
			if description == "" {
				description = "（未能识别出这张图的内容，不要猜它画了什么）"
			}
			descriptions = append(descriptions, fmt.Sprintf("message_id=%s 第%d张：%s", item.messageID, item.index, description))
		}
		page := t.historyMediaRender(base, loaded, keep, descriptions)
		if keep > 1 {
			body, err := json.Marshal(page)
			if err == nil && utf8.RuneCount(body) > budget {
				break
			}
		}
		best, kept = page, keep
	}
	return best, loaded[:kept]
}

// historyMediaRender 填好只交回 loaded[:keep] 时的计数、画面状态、续读参数和说明。
func (t *dianaHistoryImagesTool) historyMediaRender(base dianaHistoryImagesResult, loaded []historyLoadedImage, keep int, descriptions []string) dianaHistoryImagesResult {
	page := base
	page.OK = true
	dropped := make(map[int]bool, len(loaded)-keep)
	for _, item := range loaded[keep:] {
		dropped[item.mediaPos] = true
	}
	page.Media = make([]dianaHistoryImageStatus, 0, len(base.Media)-len(dropped))
	for position, status := range base.Media {
		if !dropped[position] {
			page.Media = append(page.Media, status)
		}
	}
	page.Loaded = keep
	page.FocusCrops = 0
	for _, item := range loaded[:keep] {
		page.FocusCrops += max(0, len(item.parts)-1)
	}
	page.Text = append(append([]string(nil), base.Text...), descriptions...)
	page.RemainingCount = len(loaded) - keep
	page.HasMore = page.RemainingCount > 0
	page.NextItems = nil
	positions := make(map[string]int)
	for _, item := range loaded[keep:] {
		position, found := positions[item.messageID]
		if !found {
			position = len(page.NextItems)
			positions[item.messageID] = position
			page.NextItems = append(page.NextItems, historyMediaNextItem{MessageID: item.messageID})
		}
		page.NextItems[position].MediaIndexes = append(page.NextItems[position].MediaIndexes, item.index)
	}
	if t.describesOnly() && keep > 0 {
		page.FocusCrops = 0
		page.Message = fmt.Sprintf("已取得 %d 张历史图片或视频关键帧的文字描述，在 text 里；描述由视觉模型写成，可能有误。", keep)
	} else {
		page.Message = fmt.Sprintf("已读取 %d 张历史图片或视频关键帧和 %d 条文件/语音文字信息。", keep, len(base.Text))
		if keep > 0 {
			page.Message += " 真实画面已附加到本次工具观察，请逐张查看后再回答；不要把摘要当成真实画面细节。"
		}
		if page.FocusCrops > 0 {
			page.Message += fmt.Sprintf(" 为了保留小人像和小字细节，已另附加 %d 张原图局部裁剪。", page.FocusCrops)
		}
	}
	page.Message += historyMediaFailedNotice(page) + historyMediaRemainingNotice(page)
	return page
}

func historyMediaFailedNotice(result dianaHistoryImagesResult) string {
	if result.Failed == 0 {
		return ""
	}
	return fmt.Sprintf(" 另有 %d 张读取失败，禁止推测其内容。", result.Failed)
}

func historyMediaRemainingNotice(result dianaHistoryImagesResult) string {
	if !result.HasMore {
		return ""
	}
	return fmt.Sprintf(" 单次结果上限 %d 字，本次按整张交回 %d 张，另有 %d 张没装下（不是读取失败）：把 next_items 原样传给 items 接着读；没读完之前不要说看过了全部。", agent.ToolOutputMaxChars, result.Loaded, result.RemainingCount)
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
// 模型翻到了也读不了。这里按插件的开关走同一套下载与解析。照 Pi 的做法，文件按顺序、
// 按整行装进这次工具结果的单次上限：前面的给全文，装不下的那个给到最后一个装得下的
// 整行，后面的只列名字，都写明用 file_index、start_line（PDF 用 pdf_page）续读。
// 以前按文件数平分预算、每个文件掐头去尾，文件多了干脆不展开。
func (t *dianaHistoryImagesTool) historyFileTexts(ctx context.Context, files []historyFileSource, options ...map[string]any) []string {
	if len(files) == 0 {
		return nil
	}
	var option map[string]any
	if len(options) > 0 {
		option = options[0]
	}
	// 指定了 file_index（PDF 分页或按行续读）时只读那一个文件。
	if option != nil && (option["pdf_page"] != nil || option["file_index"] != nil || option["start_line"] != nil) {
		index := 1
		if option["file_index"] != nil {
			index = intFromAny(option["file_index"])
		}
		selected := make([]historyFileSource, 0, 1)
		for _, file := range files {
			if file.index == index {
				selected = append(selected, file)
				break
			}
		}
		if len(selected) == 0 {
			return []string{fmt.Sprintf("没有找到文件序号 %d", index)}
		}
		files = selected
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
				found[0].MessageID, found[0].FileIndex = file.messageID, file.index
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
	channel, _, err := t.runtime.outboundChannelForEvent(t.event)
	if err != nil {
		channel = nil
	}
	lines = append(lines, strings.TrimSpace(fileContentNotice))
	startLine := 1
	if option != nil && option["start_line"] != nil {
		startLine = max(1, intFromAny(option["start_line"]))
	}
	// 给文件正文的预算：单次上限减去说明行和其他结果字段大致占的地方。
	budget := max(1, toolResultBudget(ctx)-historyFileResultReserve)
	used := 0
	for index, file := range files {
		ref := refs[index]
		if ref == nil {
			lines = append(lines, fmt.Sprintf("message_id=%s %s", file.messageID, historicalFileDescription(file.index, file.segment)))
			continue
		}
		if used >= budget {
			lines = append(lines, fmt.Sprintf("message_id=%s 文件%d\n- %s\n  状态：这次结果已经装满单次上限，这个文件没有展开。%s", file.messageID, file.index, ref.Name, fileContinuationHint(*ref, textLineWindow{Start: 1}, 0)))
			continue
		}
		if option != nil && option["pdf_page"] != nil {
			page := intFromAny(option["pdf_page"])
			offset := intFromAny(option["page_offset"])
			body, err := t.readPDFPage(ctx, parser, channel, *ref, maxBytes, page, offset)
			if err != nil {
				body = "读取 PDF 页失败：" + err.Error()
			}
			lines = append(lines, fmt.Sprintf("message_id=%s 文件%d\n%s", file.messageID, file.index, body))
			continue
		}
		result := parser.parseRefLines(ctx, channel, *ref, maxBytes, startLine, budget-used)
		used += result.Used
		if result.Partial {
			used = budget
		}
		body := result.Context
		if result.ScannedPDF != nil {
			body = fmt.Sprintf("- %s\n  状态：扫描版 PDF，没有文字层，这里读不出正文；让用户引用这条文件消息再问，会转成 OCR 子任务识别", ref.Name)
		}
		lines = append(lines, fmt.Sprintf("message_id=%s 文件%d\n%s", file.messageID, file.index, body))
	}
	return lines
}

// historyFileResultReserve 是读文件时给说明行、续读提示和其他结果字段留的字数。
const historyFileResultReserve = 2_000

type historyLoadedImage struct {
	messageID string
	index     int
	segment   MessageSegment
	// parts 是这张图交给模型的附件（high 档可能另带局部裁剪），mediaPos 是它在
	// 结果 media 里的位置，分页时整张一起留下或一起挪到下一页。
	parts    []llm.ContentPart
	mediaPos int
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
	parts := make([]string, 0, len(statuses))
	for _, status := range statuses {
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

// historyMediaVisionAnswerMessage 提醒模型 answer 只是帮它看懂图：群友都看过原图，
// 照着 answer 复述画面、再给玩笑话下结论，回复就成了看图报告。
func historyMediaVisionAnswerMessage(loaded int) string {
	return fmt.Sprintf("视觉模型看了 %d 张历史图片或视频关键帧，回答在 answer 里，只是帮你看懂图，也可能看错。群友都看过这些图：别把画面复述一遍、也别逐张交代看到了什么，接住他们在聊的那件事和语气来回；有人半开玩笑地问，就顺着聊，不用一本正经地核实下结论。只有对方明确要你认字、描述画面或核对细节时才照答。", loaded)
}
