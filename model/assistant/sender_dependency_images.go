// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 同一个人稍早发的图（候选依赖图）。
//
// 「先发一张图、隔几秒再问这是什么」以前靠入站队列把图并进后一句话：先问一次模型
// 「这句话指的是不是那张图」，是就把图那条的任务注销、图段塞进这句话。线上 7 天
// 并了约 245 次、判了约 260 次（116 次判是、73 次判否、71 次模型不可用），判错收不
// 回来——2026-09-26 那次把一张表情并进「把 winter 头像变成机器人风格」，改图时表情
// 顶掉了模型点名的头像。
//
// 现在运行时不替模型选：这个人刚发、还没人接的图全部作为候选单独附上，每张标清是
// 谁、多久前、哪条消息，明说「可能是、也可能不是这句话在说的东西」，由回复模型结合
// 正文判断。它们不进当前消息的 Segments，改图也不会隐式拿它们当原图（见
// image_edit_source_plan.go），要用就按 message_id 点名。
//
// 怎么附取决于对话模型能不能看图：
//   - 能看（默认）：直接附原图，60 秒内下完就开始回复，不等识图描述；描述照常在
//     后台补，给历史和检索用。
//   - 看不了（识图插件设成「仅识别文字」）：加急识图并等描述，第一张 90 秒、之后
//     每张多 30 秒、最多 4 分钟；等不完照样回复，但写明哪几张还没读到，别让模型猜。
//   - 附了原图、模型却回「没收到图片」：这一轮按「看不了」的办法重来一次。

const (
	// senderDependencyImageMaxImages 是一轮最多附几张候选图，从新到旧取。
	senderDependencyImageMaxImages = 4
	// senderDependencyImageDownloadBudget 是下载原图的总时限。
	senderDependencyImageDownloadBudget = 60 * time.Second
	// 看不了图时等描述的时限：第一张、之后每张、封顶。
	senderDependencyDescriptionFirstBudget = 90 * time.Second
	senderDependencyDescriptionExtraBudget = 30 * time.Second
	senderDependencyDescriptionMaxBudget   = 4 * time.Minute
)

// senderDependencyImage 是一张候选依赖图。Source 是它所在的那条消息。
type senderDependencyImage struct {
	Source       MessageEvent
	SegmentIndex int
}

func (image senderDependencyImage) segment() MessageSegment {
	return image.Source.Segments[image.SegmentIndex]
}

// singleImageEvent 是只剩这张图的那条消息，识图队列按它入队。
func (image senderDependencyImage) singleImageEvent() MessageEvent {
	event := image.Source
	event.Quoted = nil
	event.Segments = []MessageSegment{image.segment()}
	return event
}

// senderDependencyImages 选出当前发言者刚发、还没人接的图。规则：
//   - 当前消息引用了带图的消息：引用就是明确的指向，不再附别的图。
//   - 只看当前这条之前的消息（历史里在它后面、或者时间比它晚的都不算）。
//   - 只看同一个人 recentSenderImageWindow 内最近 recentSenderImageMessages 条带图消息，
//     合计最多 senderDependencyImageMaxImages 张，从新到旧取。
//   - 中间机器人已经回过这个人、或者别人说了有内容的话，那张图就不再「悬着」，停。
//   - 表情不算：mface/face、带非零 sub_type 的图、以及窗口里有人发过两次以上的同一张图
//     （刷屏的表情包）。
//
// skip 里是本轮已经作为同轮补充附上原图的消息。返回按时间从旧到新排。
func senderDependencyImages(history []MessageEvent, event MessageEvent, skip map[string]bool, botID string) []senderDependencyImage {
	userID := strings.TrimSpace(event.UserID)
	if userID == "" {
		return nil
	}
	if event.Quoted != nil && hasImageSegment(event.Quoted.Segments) {
		return nil
	}
	windowStart := int64(0)
	if event.Time > 0 {
		windowStart = event.Time - int64(recentSenderImageWindow/time.Second)
	}
	repeated := repeatedImageKeys(history, windowStart)
	var picked []senderDependencyImage
	messages := 0
	// 只取当前这条之前的图。几条消息并发处理时历史末尾可能是更晚到的图，那不是
	// 「这句话之前刚发的」；拿它当依赖图还会让早的文字接走晚的图（见 sender_burst.go）。
	start := len(history) - 1
	if currentID := strings.TrimSpace(event.MessageID); currentID != "" {
		for index := len(history) - 1; index >= 0; index-- {
			if strings.TrimSpace(history[index].MessageID) == currentID {
				start = index - 1
				break
			}
		}
	}
	for index := start; index >= 0; index-- {
		item := history[index]
		messageID := strings.TrimSpace(item.MessageID)
		if messageID != "" && (messageID == strings.TrimSpace(event.MessageID) || skip[messageID]) {
			continue
		}
		if event.Time > 0 && item.Time > event.Time {
			continue
		}
		if item.crossGroupContext || isPokeHistoryEvent(item) {
			continue
		}
		if windowStart > 0 && item.Time > 0 && item.Time < windowStart {
			break
		}
		if strings.TrimSpace(item.botReply) != "" || assistantHistoryEvent(item, botID) {
			if botMessageAnswersUser(history, index, item, userID, botID) {
				break
			}
			continue
		}
		if strings.TrimSpace(item.UserID) != userID {
			if messageHasSubstance(item, repeated) {
				break
			}
			continue
		}
		var own []senderDependencyImage
		for segmentIndex, segment := range item.Segments {
			if dependencyImageCandidate(segment, repeated) {
				own = append(own, senderDependencyImage{Source: item, SegmentIndex: segmentIndex})
			}
		}
		if len(own) == 0 {
			continue
		}
		// 同一条消息里的图按原顺序，消息之间从新到旧取，最后整体翻成从旧到新。
		for position := len(own) - 1; position >= 0 && len(picked) < senderDependencyImageMaxImages; position-- {
			picked = append(picked, own[position])
		}
		messages++
		if messages >= recentSenderImageMessages || len(picked) >= senderDependencyImageMaxImages {
			break
		}
	}
	for left, right := 0, len(picked)-1; left < right; left, right = left+1, right-1 {
		picked[left], picked[right] = picked[right], picked[left]
	}
	return picked
}

// botMessageAnswersUser 判断历史里这条机器人发言是不是在回 userID：点名、引用了他，
// 或者它前面紧挨着的那条入站消息就是他发的。
func botMessageAnswersUser(history []MessageEvent, index int, message MessageEvent, userID, botID string) bool {
	if proactiveReplyBotMessageAddressesUser(message, history, userID) {
		return true
	}
	for previous := index - 1; previous >= 0; previous-- {
		item := history[previous]
		if item.crossGroupContext || isPokeHistoryEvent(item) {
			continue
		}
		if strings.TrimSpace(item.botReply) != "" || assistantHistoryEvent(item, botID) {
			continue
		}
		return strings.TrimSpace(item.UserID) == userID
	}
	return false
}

// messageHasSubstance 判断别人这条消息算不算「接了话」：有文字，或者发了不是表情的
// 图和其他媒体。只甩一张表情不算，图还悬着。
func messageHasSubstance(item MessageEvent, repeated map[string]int) bool {
	if strings.TrimSpace(historyPlainText(item)) != "" {
		return true
	}
	for _, segment := range item.Segments {
		switch segment.Type {
		case "image":
			if dependencyImageCandidate(segment, repeated) {
				return true
			}
		case "video", "record", "file", "forward", "json", "xml":
			return true
		}
	}
	return false
}

// dependencyImageCandidate 判断一个段能不能当候选依赖图：可用的静态图，不是表情，
// 也不是窗口里被刷屏的那张。
func dependencyImageCandidate(segment MessageSegment, repeated map[string]int) bool {
	if !recallStillImageSegment(segment) || strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") {
		return false
	}
	if imageSegmentLooksLikeSticker(segment) {
		return false
	}
	if key := dependencyImageKey(segment); key != "" && repeated[key] >= 2 {
		return false
	}
	return true
}

// imageSegmentLooksLikeSticker 认表情：OneBot 各实现口径不一，收藏和小表情多是 image
// 带非零 sub_type，摘要常写着「[动画表情]」。认不出就当普通图片。
func imageSegmentLooksLikeSticker(segment MessageSegment) bool {
	switch segment.Type {
	case "mface", "face":
		return true
	case "image":
		if subType := strings.TrimSpace(segment.Data["sub_type"]); subType != "" && subType != "0" {
			return true
		}
		return strings.Contains(segment.Data["summary"], "表情")
	}
	return false
}

// dependencyImageKey 是认「同一张图」用的键：有内容哈希用哈希，否则退回文件名
// （QQ 的 file 字段本身就是按内容算的）或地址。
func dependencyImageKey(segment MessageSegment) string {
	if hash, ok := imageSegmentContentSHA256(segment); ok {
		return "sha256:" + hash
	}
	if file := strings.TrimSpace(segment.Data["file"]); file != "" {
		return "file:" + file
	}
	if url := strings.TrimSpace(segment.Data["url"]); url != "" {
		return "url:" + url
	}
	return ""
}

// repeatedImageKeys 数窗口里每张图被多少条消息发过（谁发的都算）。
func repeatedImageKeys(history []MessageEvent, windowStart int64) map[string]int {
	counts := map[string]int{}
	for _, item := range history {
		if windowStart > 0 && item.Time > 0 && item.Time < windowStart {
			continue
		}
		seen := map[string]bool{}
		for _, segment := range item.Segments {
			if segment.Type != "image" {
				continue
			}
			if key := dependencyImageKey(segment); key != "" && !seen[key] {
				seen[key] = true
				counts[key]++
			}
		}
	}
	return counts
}

// senderDependencyDescriptionBudget 是看不了图时等描述的总时限。
func senderDependencyDescriptionBudget(images int) time.Duration {
	if images <= 0 {
		return 0
	}
	budget := senderDependencyDescriptionFirstBudget + time.Duration(images-1)*senderDependencyDescriptionExtraBudget
	return min(budget, senderDependencyDescriptionMaxBudget)
}

// chatModelReceivesImages 报告这一轮的图会不会原样交给对话模型。唯一的「看不了图」
// 配置是识图插件的「仅识别文字」：那时图会从消息里摘掉，换成识别文本。
func (r *Runtime) chatModelReceivesImages(event MessageEvent) bool {
	_, cfg, ok := r.imageOCRActiveConfig(event)
	return !(ok && cfg.textOnly())
}

// senderDependencyContext 是一轮里候选依赖图的全部状态，失败重来时要换成文字版。
type senderDependencyContext struct {
	images   []senderDependencyImage
	toolHint bool
	// pixels 表示这一轮附的是原图。
	pixels bool
}

// senderDependencyMessage 按 pixels 生成那段上下文。原图模式下载失败的图单独说明；文字模式
// 等描述，等不完的写明还没读到。
func (r *Runtime) senderDependencyMessage(ctx context.Context, current MessageEvent, dependency *senderDependencyContext) llm.Message {
	if dependency == nil || len(dependency.images) == 0 {
		return llm.Message{}
	}
	images := r.prepareSenderDependencyImages(ctx, dependency.images)
	dependency.images = images
	header := senderDependencyHeader(current, dependency.toolHint)
	if dependency.pixels {
		return r.senderDependencyPixelMessage(ctx, current, images, header)
	}
	return r.senderDependencyDescriptionMessage(ctx, current, images, header)
}

// prepareSenderDependencyImages 在下载时限内把原图取到本地，识图和附原图都要用。
func (r *Runtime) prepareSenderDependencyImages(ctx context.Context, images []senderDependencyImage) []senderDependencyImage {
	downloadCtx, cancel := context.WithTimeout(ctx, senderDependencyImageDownloadBudget)
	defer cancel()
	prepared := map[string]MessageEvent{}
	out := make([]senderDependencyImage, 0, len(images))
	for _, image := range images {
		key := strings.TrimSpace(image.Source.MessageID)
		source, ok := prepared[key]
		if !ok || key == "" {
			source = r.prepareHistoricalEventImages(downloadCtx, image.Source)
			if historicalImageStateChanged(image.Source, source) {
				r.updateHistoricalImageState(source)
			}
			prepared[key] = source
		}
		if image.SegmentIndex < len(source.Segments) {
			image.Source = source
		}
		out = append(out, image)
	}
	return out
}

func senderDependencyHeader(current MessageEvent, toolHint bool) string {
	header := "【同一发言者稍早发的图（候选，不是本条消息自带的）】\n" +
		promptSenderIdentity(current) + " 在这条消息之前单独发了下面这些图。本条消息可能在说其中某张，也可能跟它们无关——结合正文和上下文判断；" +
		"拿不准指的是哪张，就问一句，或在回复里说明你按哪张理解。"
	if toolHint {
		header += "它们不会自动当作改图原图：要改、要当素材，把对应的 message_id 填进 image 工具的 source_message_ids；用户说的是某个人的头像时用 identity_sources，别拿这些图顶替。"
	}
	return header
}

func senderDependencyLabel(current MessageEvent, index int, image senderDependencyImage, pixels bool) string {
	age := ""
	if current.Time > 0 && image.Source.Time > 0 && current.Time >= image.Source.Time {
		age = fmt.Sprintf(" %d 秒前", current.Time-image.Source.Time)
	}
	kind := "原图"
	if !pixels {
		kind = "机器识别的描述"
	}
	messageID := strings.TrimSpace(image.Source.MessageID)
	if messageID == "" {
		messageID = "不可用"
	}
	return fmt.Sprintf("图%d：%s%s发的图（%s，不是本条消息自带的；message_id=%s）", index+1, promptSenderIdentity(image.Source), age, kind, messageID)
}

func (r *Runtime) senderDependencyPixelMessage(ctx context.Context, current MessageEvent, images []senderDependencyImage, header string) llm.Message {
	downloadCtx, cancel := context.WithTimeout(ctx, senderDependencyImageDownloadBudget)
	defer cancel()
	loaded := make([][]string, len(images))
	var wait sync.WaitGroup
	for index, image := range images {
		urls := availableImageURLs([]MessageSegment{image.segment()})
		if len(urls) == 0 {
			continue
		}
		wait.Add(1)
		go func(index int, urls []string) {
			defer recoverGoroutinePanic("senderDependencyPixelMessage")
			defer wait.Done()
			loaded[index], _ = loadLLMImageURLsDetailed(downloadCtx, urls)
		}(index, urls)
	}
	wait.Wait()
	parts := []llm.ContentPart{{Type: llm.ContentPartText, Text: header}}
	attached := 0
	for index, image := range images {
		label := senderDependencyLabel(current, index, image, true)
		if len(loaded[index]) == 0 {
			parts = append(parts, llm.ContentPart{Type: llm.ContentPartText, Text: label + "\n（这张原图没能读取，不要猜它的内容）"})
			continue
		}
		parts = append(parts, llm.ContentPart{Type: llm.ContentPartText, Text: label})
		for _, url := range loaded[index] {
			parts = append(parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: url})
		}
		attached++
		// 描述照常在后台补：历史和检索要用，这一轮不等。
		r.enqueueHistoryImageDescriptionsNow(image.singleImageEvent())
	}
	if attached == 0 {
		// 一张都没下下来：退回文字版，至少把能说的说清楚。
		return r.senderDependencyDescriptionMessage(ctx, current, images, header)
	}
	return llm.Message{Role: llm.RoleUser, Parts: parts, Priority: llm.MessagePriorityCurrent}
}

func (r *Runtime) senderDependencyDescriptionMessage(ctx context.Context, current MessageEvent, images []senderDependencyImage, header string) llm.Message {
	events := make([]MessageEvent, 0, len(images))
	for _, image := range images {
		events = append(events, image.singleImageEvent())
	}
	waitCtx, cancel := context.WithTimeout(ctx, senderDependencyDescriptionBudget(len(images)))
	r.awaitHistoryImageDescriptions(waitCtx, events...)
	cancel()
	lines := []string{header}
	var pending []string
	for index, image := range images {
		label := senderDependencyLabel(current, index, image, false)
		if description := r.cachedImageSegmentDescription(ctx, image.segment()); description != "" {
			lines = append(lines, label+"\n画面描述："+description)
			continue
		}
		pending = append(pending, fmt.Sprintf("图%d", index+1))
		lines = append(lines, label+"\n（还没读到：识图还没完成，不要猜这张图的内容）")
	}
	if len(pending) > 0 {
		lines = append(lines, "【还没读到的图】"+strings.Join(pending, "、")+" 的内容你现在不知道。如果这条消息问的正是它们，如实说还没看清、稍后再看，不要编内容。")
	}
	return llm.Message{Role: llm.RoleUser, Content: strings.Join(lines, "\n"), Priority: llm.MessagePriorityCurrent, AtomicText: true}
}

// cachedImageSegmentDescription 取一张图已有的识图描述，没有就返回空。
func (r *Runtime) cachedImageSegmentDescription(ctx context.Context, segment MessageSegment) string {
	if description := strings.TrimSpace(segment.Data[recallImageDescriptionKey]); description != "" {
		return description
	}
	store := r.recallImageDescriptionStore()
	if store == nil {
		return ""
	}
	hash, ok := imageSegmentContentSHA256(segment)
	if !ok {
		return ""
	}
	record, found, err := store.GetImageDescription(ctx, hash)
	if err != nil {
		log.Printf("diana dependency image description load failed: %v", err)
		return ""
	}
	if !found {
		return ""
	}
	return strings.TrimSpace(record.Description)
}
