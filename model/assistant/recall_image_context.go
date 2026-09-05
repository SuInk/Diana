// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

const (
	recallImageDescriptionKey         = "cached_description"
	recallImageDescriptionSourceKey   = "cached_description_source"
	recallImageAttachmentIndexKey     = "recall_attachment_index"
	recallImageDescriptionVersion     = "recall-image-v1"
	recallImageDescriptionMaxRunes    = 1600
	recallImageDescriptionConcurrency = 3
	// 历史行里的描述比撤回记录短得多：每轮都要重复发送，超长会把上下文预算吃光。
	historyImageDescriptionMaxRunes     = 400
	historyImageDescriptionQueueLimit   = 32
	historyImageDescriptionReadyLimit   = 2048
	historyImageDescriptionTimeout      = 90 * time.Second
	historyImageDescriptionRetryBackoff = 10 * time.Minute
	historyImageDescriptionIdlePoll     = 250 * time.Millisecond
	// historyImageDescriptionMaxAge 限定「自动补描述」的时间窗。
	//
	// 库里有近两万条带图消息，识图是单并发、每张最多等 90 秒。真要顺着回填
	// 补完，按每张 5 秒算是 26 小时，按超时算是 19 天——而且补的是没人再提起
	// 的老图。窗口之外的图不自动补，等真被引用时再补。
	historyImageDescriptionMaxAge = 24 * time.Hour
)

// withinHistoryImageDescriptionWindow 判断这条消息是否新到值得自动补描述。
// 没有时间戳的合成事件当作当前消息处理，不因为缺字段被挡掉。
func withinHistoryImageDescriptionWindow(event MessageEvent, now time.Time) bool {
	if event.Time <= 0 {
		return true
	}
	return now.Sub(time.Unix(event.Time, 0)) <= historyImageDescriptionMaxAge
}

type recallImagePosition struct {
	eventIndex   int
	segmentIndex int
}

type recallImageTarget struct {
	key               string
	contentSHA256     string
	imageSource       string
	sourceMessageIDs  []string
	positions         []recallImagePosition
	description       string
	descriptionSource string
}

func prepareRecallImageAttachments(recalls []MessageEvent) ([]MessageEvent, []string) {
	out := cloneRecallEvents(recalls)
	attachmentByImage := make(map[string]int)
	var imageURLs []string
	for eventIndex := range out {
		for segmentIndex := range out[eventIndex].Segments {
			segment := &out[eventIndex].Segments[segmentIndex]
			if !recallStillImageSegment(*segment) {
				continue
			}
			delete(segment.Data, recallImageAttachmentIndexKey)
			if strings.TrimSpace(segment.Data[recallImageDescriptionKey]) != "" {
				continue
			}
			source := firstImageSource(*segment)
			if source == "" {
				continue
			}
			key := source
			if hash, ok := imageSegmentContentSHA256(*segment); ok {
				key = hash
				segment.Data[imageContentSHA256Key] = hash
			}
			attachmentIndex, ok := attachmentByImage[key]
			if !ok {
				imageURLs = append(imageURLs, source)
				attachmentIndex = len(imageURLs)
				attachmentByImage[key] = attachmentIndex
			}
			segment.Data[recallImageAttachmentIndexKey] = strconv.Itoa(attachmentIndex)
		}
	}
	return out, imageURLs
}

func recallImageFactLines(segments []MessageSegment) []string {
	var lines []string
	imageIndex := 0
	for _, segment := range segments {
		if !recallStillImageSegment(segment) {
			continue
		}
		imageIndex++
		description := compactRecallImageDescription(segment.Data[recallImageDescriptionKey])
		switch {
		case description != "":
			lines = append(lines, fmt.Sprintf("图片%d内容描述=%s", imageIndex, description))
		case strings.TrimSpace(segment.Data[recallImageAttachmentIndexKey]) != "":
			lines = append(lines, fmt.Sprintf("图片%d内容描述=此前没有可复用描述，请查看多模态附件%s后客观描述", imageIndex, segment.Data[recallImageAttachmentIndexKey]))
		default:
			lines = append(lines, fmt.Sprintf("图片%d内容描述=没有缓存描述，原图片文件当前也不可读取", imageIndex))
		}
	}
	return lines
}

func recallStillImageSegment(segment MessageSegment) bool {
	return segment.Type == "image" && strings.TrimSpace(segment.Data["source_type"]) != "video_frame"
}

func historyDescribableImageSegment(segment MessageSegment) bool {
	return segment.Type == "image"
}

func cloneRecallEvents(events []MessageEvent) []MessageEvent {
	out := make([]MessageEvent, len(events))
	for eventIndex, event := range events {
		out[eventIndex] = event
		out[eventIndex].Segments = make([]MessageSegment, len(event.Segments))
		for segmentIndex, segment := range event.Segments {
			out[eventIndex].Segments[segmentIndex] = segment
			out[eventIndex].Segments[segmentIndex].Data = cloneSegmentData(segment.Data)
		}
	}
	return out
}

func compactRecallImageDescription(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) > recallImageDescriptionMaxRunes {
		value = string(runes[:recallImageDescriptionMaxRunes]) + "..."
	}
	return value
}

func (r *Runtime) enrichRecallImageDescriptions(ctx context.Context, event MessageEvent, recalls []MessageEvent) []MessageEvent {
	out := cloneRecallEvents(recalls)
	targets := collectRecallImageTargets(out)
	if len(targets) == 0 {
		return out
	}

	store := r.recallImageDescriptionStore()
	for _, target := range targets {
		if target.contentSHA256 == "" || store == nil {
			continue
		}
		record, ok, err := store.GetImageDescription(ctx, target.contentSHA256)
		if err != nil {
			log.Printf("diana recall image description cache load failed: %v", err)
			continue
		}
		if ok && strings.TrimSpace(record.Description) != "" {
			target.description = compactRecallImageDescription(record.Description)
			target.descriptionSource = firstNonEmpty(record.Source, "cache")
		}
	}

	historical := r.historicalRecallImageDescriptions(ctx, event, targets)
	for _, target := range targets {
		if target.description != "" {
			continue
		}
		if description := compactRecallImageDescription(historical[target.key]); description != "" {
			target.description = description
			target.descriptionSource = "history"
			r.saveRecallImageDescription(target, event)
		}
	}

	r.describeMissingRecallImages(ctx, event, targets)
	for _, target := range targets {
		if target.description == "" {
			continue
		}
		for _, position := range target.positions {
			segment := &out[position.eventIndex].Segments[position.segmentIndex]
			segment.Data[recallImageDescriptionKey] = target.description
			segment.Data[recallImageDescriptionSourceKey] = target.descriptionSource
			if target.contentSHA256 != "" {
				segment.Data[imageContentSHA256Key] = target.contentSHA256
			}
		}
	}
	return out
}

func collectRecallImageTargets(recalls []MessageEvent) []*recallImageTarget {
	targetByKey := make(map[string]*recallImageTarget)
	var targets []*recallImageTarget
	for eventIndex := range recalls {
		for segmentIndex := range recalls[eventIndex].Segments {
			segment := &recalls[eventIndex].Segments[segmentIndex]
			if !recallStillImageSegment(*segment) {
				continue
			}
			hash, _ := imageSegmentContentSHA256(*segment)
			if hash != "" {
				segment.Data[imageContentSHA256Key] = hash
			}
			source := firstImageSource(*segment)
			key := hash
			if key == "" {
				key = source
			}
			if key == "" {
				key = fmt.Sprintf("message:%s:image:%d", recalls[eventIndex].MessageID, segmentIndex)
			}
			target := targetByKey[key]
			if target == nil {
				target = &recallImageTarget{
					key:               key,
					contentSHA256:     hash,
					imageSource:       source,
					description:       compactRecallImageDescription(segment.Data[recallImageDescriptionKey]),
					descriptionSource: strings.TrimSpace(segment.Data[recallImageDescriptionSourceKey]),
				}
				targetByKey[key] = target
				targets = append(targets, target)
			}
			target.sourceMessageIDs = appendUniqueStrings(target.sourceMessageIDs, recalls[eventIndex].MessageID)
			target.positions = append(target.positions, recallImagePosition{eventIndex: eventIndex, segmentIndex: segmentIndex})
		}
	}
	return targets
}

func (r *Runtime) recallImageDescriptionStore() ImageDescriptionStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	store, _ := r.messageStore.(ImageDescriptionStore)
	return store
}

// enqueueHistoryImageDescriptions fills the durable summary layer away from
// the visible reply path. Content hashes deduplicate identical images across
// messages and the bounded pending set prevents image bursts from creating an
// unbounded background workload.
func (r *Runtime) enqueueHistoryImageDescriptions(event MessageEvent) {
	// 自动路径只补近期图片。重连回填会把很久以前的消息重放一遍，每条都排一次
	// 识图，等于拿单并发去补一整个库——按当前速度是几十小时起步，而这些老图
	// 绝大多数没人再提起。真被引用时会走 enqueueHistoryImageDescriptionsNow。
	if !withinHistoryImageDescriptionWindow(event, time.Now()) {
		return
	}
	r.enqueueHistoryImageDescriptionsNow(event)
}

// enqueueHistoryImageDescriptionsNow 不看时间，用于用户/模型真的在读这张图的路径。
func (r *Runtime) enqueueHistoryImageDescriptionsNow(event MessageEvent) {
	if r == nil || r.recallImageDescriptionStore() == nil {
		return
	}
	for _, sourceEvent := range historyImageDescriptionEvents(event) {
		for _, segment := range sourceEvent.Segments {
			if !historyDescribableImageSegment(segment) || strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") {
				continue
			}
			if strings.TrimSpace(segment.Data[recallImageDescriptionKey]) != "" {
				continue
			}
			hash, ok := imageSegmentContentSHA256(segment)
			if !ok {
				continue
			}
			// 排队的任务不能攥着图片本体：单并发下 31 个任务纯粹在等，却各自
			// 钉住一整条消息。削成「哈希 + 本地路径」再入队（见 history_image_queue.go）。
			source, retained := queuedImageSourceRetained(segment)
			if !retained || !r.reserveHistoryImageDescription(hash) {
				continue
			}
			jobEvent := historyImageDescriptionQueueEvent(sourceEvent)
			jobEvent.Segments = []MessageSegment{stripImageSegmentForQueue(segment)}
			go r.runHistoryImageDescription(jobEvent, historyImageDescriptionQueueEvent(sourceEvent), hash, source)
		}
	}
}

func historyImageDescriptionEvents(event MessageEvent) []MessageEvent {
	main := event
	main.Quoted = nil
	events := []MessageEvent{main}
	if event.Quoted == nil {
		return events
	}
	quoted := event.Quoted
	quotedEvent := event
	quotedEvent.GroupID = firstNonEmpty(quoted.GroupID, event.GroupID)
	quotedEvent.UserID = firstNonEmpty(quoted.UserID, event.UserID)
	quotedEvent.MessageID = quoted.MessageID
	quotedEvent.RawMessage = quoted.RawMessage
	quotedEvent.Segments = quoted.Segments
	quotedEvent.SenderName = quoted.SenderName
	quotedEvent.Quoted = nil
	return append(events, quotedEvent)
}

func (r *Runtime) reserveHistoryImageDescription(hash string) bool {
	now := time.Now()
	r.historyImageDescMu.Lock()
	defer r.historyImageDescMu.Unlock()
	if r.historyImageDescRun == nil {
		r.historyImageDescRun = map[string]struct{}{}
	}
	if r.historyImageDescReady == nil {
		r.historyImageDescReady = map[string]struct{}{}
	}
	if r.historyImageDescRetry == nil {
		r.historyImageDescRetry = map[string]time.Time{}
	}
	if r.historyImageDescSem == nil {
		r.historyImageDescSem = make(chan struct{}, 1)
	}
	for key, retryAt := range r.historyImageDescRetry {
		if !retryAt.After(now) {
			delete(r.historyImageDescRetry, key)
		}
	}
	if _, running := r.historyImageDescRun[hash]; running {
		return false
	}
	if _, ready := r.historyImageDescReady[hash]; ready {
		return false
	}
	if retryAt := r.historyImageDescRetry[hash]; retryAt.After(now) {
		return false
	}
	if len(r.historyImageDescRun) >= historyImageDescriptionQueueLimit {
		return false
	}
	r.historyImageDescRun[hash] = struct{}{}
	return true
}

func (r *Runtime) runHistoryImageDescription(event, indexEvent MessageEvent, hash, source string) {
	ctx := context.Background()
	r.mu.RLock()
	runtimeCtx := r.runCtx
	if runtimeCtx != nil {
		ctx = runtimeCtx
	}
	r.mu.RUnlock()
	ctx, cancel := context.WithTimeout(ctx, historyImageDescriptionTimeout)
	defer cancel()

	err := r.waitForHistoryImageDescriptionSlot(ctx, cancel)
	if err == nil {
		defer r.releaseHistoryImageDescriptionSlot()
		store := r.recallImageDescriptionStore()
		if store == nil {
			err = fmt.Errorf("image description store is not configured")
		} else if record, found, loadErr := store.GetImageDescription(ctx, hash); loadErr != nil {
			err = loadErr
		} else if found && strings.TrimSpace(record.Description) != "" {
			r.markHistoryImageDescriptionReady(hash)
			// 描述早就生成过（同一张表情包、或升级前留下的记录），但这条消息的
			// 检索文本可能还是空的：顺手补上，老历史才搜得到。
			r.refreshMessageImageSearchText(ctx, indexEvent)
		} else {
			var description string
			description, err = r.describeRecallImage(ctx, event, source)
			if err == nil {
				err = store.SaveImageDescription(ctx, ImageDescriptionRecord{
					ContentSHA256:   hash,
					Description:     compactRecallImageDescription(description),
					SourceSession:   sessionKey(event),
					SourceMessageID: event.MessageID,
					Source:          "vision",
					Version:         recallImageDescriptionVersion,
				})
				if err == nil {
					r.markHistoryImageDescriptionReady(hash)
					r.refreshMessageImageSearchText(ctx, indexEvent)
				}
			}
		}
	}

	r.historyImageDescMu.Lock()
	delete(r.historyImageDescRun, hash)
	if err != nil && !errors.Is(err, context.Canceled) {
		r.historyImageDescRetry[hash] = time.Now().Add(historyImageDescriptionRetryBackoff)
	}
	r.historyImageDescMu.Unlock()
	if errors.Is(err, context.Canceled) && (runtimeCtx == nil || runtimeCtx.Err() == nil) {
		time.AfterFunc(historyImageDescriptionIdlePoll, func() { r.enqueueHistoryImageDescriptionsNow(event) })
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("diana history image description failed: message_id=%s err=%v", event.MessageID, err)
	}
}

// refreshMessageImageSearchText 在图片描述生成之后，把描述补进这条消息的可检索
// 文本，并重新排一次语义索引。
//
// 不这么做的话，纯图片消息在两条检索路径上都是隐形的：词面索引只收正文，语义
// 索引因为正文为空连门槛都过不去。用户后来问「上次那张猫哈气的图」，检索一条
// 都召不回——图早就在库里，只是没有任何一个字能被搜到。
func (r *Runtime) refreshMessageImageSearchText(ctx context.Context, event MessageEvent) {
	if r == nil || strings.TrimSpace(event.MessageID) == "" {
		return
	}
	descriptions := r.messageImageDescriptionText(ctx, event)
	if descriptions == "" {
		return
	}
	r.mu.RLock()
	store, _ := r.messageStore.(MessageSearchExtraStore)
	r.mu.RUnlock()
	if store != nil {
		if err := store.SaveMessageSearchExtra(ctx, sessionKey(event), event.MessageID, descriptions); err != nil {
			log.Printf("diana history image search text update failed: message_id=%s err=%v", event.MessageID, err)
		}
	}
	r.enqueueSemanticIndex(event)
}

// messageImageDescriptionText 汇总一条消息里所有图片的描述，供检索使用。
// 只读已经落库的描述，不触发新的视觉调用。
func (r *Runtime) messageImageDescriptionText(ctx context.Context, event MessageEvent) string {
	store := r.recallImageDescriptionStore()
	segments := append([]MessageSegment(nil), event.Segments...)
	if event.Quoted != nil {
		segments = append(segments, event.Quoted.Segments...)
	}
	var parts []string
	seen := map[string]bool{}
	for _, segment := range segments {
		if !recallStillImageSegment(segment) {
			continue
		}
		description := strings.TrimSpace(segment.Data[recallImageDescriptionKey])
		if description == "" && store != nil {
			hash, ok := imageSegmentContentSHA256(segment)
			if !ok {
				continue
			}
			record, found, err := store.GetImageDescription(ctx, hash)
			if err != nil || !found {
				continue
			}
			description = strings.TrimSpace(record.Description)
		}
		if description == "" || seen[description] {
			continue
		}
		seen[description] = true
		parts = append(parts, compactRecallImageDescription(description))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func (r *Runtime) beginHistoryImageDescriptionForeground() {
	if r == nil {
		return
	}
	r.historyImageDescMu.Lock()
	r.historyImageDescFront++
	cancel := r.historyImageDescStop
	r.historyImageDescStop = nil
	r.historyImageDescMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runtime) endHistoryImageDescriptionForeground() {
	if r == nil {
		return
	}
	r.historyImageDescMu.Lock()
	if r.historyImageDescFront > 0 {
		r.historyImageDescFront--
	}
	r.historyImageDescMu.Unlock()
}

func (r *Runtime) markHistoryImageDescriptionReady(hash string) {
	r.historyImageDescMu.Lock()
	if r.historyImageDescReady == nil {
		r.historyImageDescReady = map[string]struct{}{}
	}
	if len(r.historyImageDescReady) >= historyImageDescriptionReadyLimit {
		for existing := range r.historyImageDescReady {
			delete(r.historyImageDescReady, existing)
			break
		}
	}
	r.historyImageDescReady[hash] = struct{}{}
	r.historyImageDescMu.Unlock()
}

func (r *Runtime) waitForHistoryImageDescriptionSlot(ctx context.Context, cancel context.CancelFunc) error {
	ticker := time.NewTicker(historyImageDescriptionIdlePoll)
	defer ticker.Stop()
	for {
		r.historyImageDescMu.Lock()
		foreground := r.historyImageDescFront
		r.historyImageDescMu.Unlock()
		if foreground == 0 && r.activeCount() == 0 {
			select {
			case r.historyImageDescSem <- struct{}{}:
				r.historyImageDescMu.Lock()
				if r.historyImageDescFront == 0 && r.activeCount() == 0 {
					r.historyImageDescStop = cancel
					r.historyImageDescMu.Unlock()
					return nil
				}
				r.historyImageDescMu.Unlock()
				<-r.historyImageDescSem
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Runtime) releaseHistoryImageDescriptionSlot() {
	r.historyImageDescMu.Lock()
	r.historyImageDescStop = nil
	r.historyImageDescMu.Unlock()
	<-r.historyImageDescSem
}

func (r *Runtime) historicalRecallImageDescriptions(ctx context.Context, event MessageEvent, targets []*recallImageTarget) map[string]string {
	result := make(map[string]string)
	if len(targets) == 0 {
		return result
	}
	targetByHash := make(map[string]*recallImageTarget)
	targetByMessageID := make(map[string]*recallImageTarget)
	for _, target := range targets {
		if target.contentSHA256 != "" {
			targetByHash[target.contentSHA256] = target
		}
		for _, messageID := range target.sourceMessageIDs {
			targetByMessageID[messageID] = target
		}
	}

	timeline := r.recallDescriptionTimeline(ctx, event)
	for _, item := range timeline {
		for _, segment := range item.Segments {
			if !recallStillImageSegment(segment) {
				continue
			}
			hash, ok := imageSegmentContentSHA256(segment)
			if !ok {
				continue
			}
			if target := targetByHash[hash]; target != nil && strings.TrimSpace(item.MessageID) != "" {
				targetByMessageID[item.MessageID] = target
			}
		}
	}

	cfg := r.effectiveConfigForEvent(event)
	bestLength := make(map[string]int)
	for _, item := range timeline {
		if !recallDescriptionBotMessage(item, cfg) {
			continue
		}
		description := recallDescriptionMessageText(item)
		if description == "" {
			continue
		}
		sourceIDs := append(eventSemanticSourceMessageIDs(item), replyReferenceIDs(item.Segments)...)
		for _, sourceID := range dedupeStrings(sourceIDs) {
			target := targetByMessageID[sourceID]
			if target == nil {
				continue
			}
			length := len([]rune(description))
			if length > bestLength[target.key] {
				result[target.key] = description
				bestLength[target.key] = length
			}
		}
	}
	return result
}

func (r *Runtime) recallDescriptionTimeline(ctx context.Context, event MessageEvent) []MessageEvent {
	throughTime := event.Time
	if throughTime <= 0 {
		throughTime = time.Now().Unix()
	}
	fromTime := throughTime - int64((recallDefaultWindow+time.Hour)/time.Second)
	r.mu.RLock()
	store := r.messageStore
	inMemory := append([]MessageEvent(nil), r.history[sessionKey(event)]...)
	r.mu.RUnlock()
	timelineStore, ok := store.(MessageTimelineStore)
	if !ok {
		return inMemory
	}
	loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	loaded, err := timelineStore.ListMessageEventsBetween(loadCtx, sessionKey(event), fromTime, throughTime)
	if err != nil {
		log.Printf("diana recall image description timeline load failed: %v", err)
		return inMemory
	}
	return mergeMessageTimelines(loaded, inMemory)
}

func mergeMessageTimelines(primary, secondary []MessageEvent) []MessageEvent {
	byID := make(map[string]MessageEvent, len(primary)+len(secondary))
	var withoutID []MessageEvent
	for _, events := range [][]MessageEvent{primary, secondary} {
		for _, event := range events {
			if strings.TrimSpace(event.MessageID) == "" {
				withoutID = append(withoutID, event)
				continue
			}
			byID[event.MessageID] = event
		}
	}
	out := make([]MessageEvent, 0, len(byID)+len(withoutID))
	for _, event := range byID {
		out = append(out, event)
	}
	out = append(out, withoutID...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}

func recallDescriptionBotMessage(event MessageEvent, cfg BotConfig) bool {
	botAccount := strings.TrimSpace(cfg.BotAccount)
	if botAccount != "" && strings.TrimSpace(event.UserID) == botAccount {
		return true
	}
	return strings.TrimSpace(cfg.Name) != "" && strings.TrimSpace(event.SenderName) == strings.TrimSpace(cfg.Name)
}

func recallDescriptionMessageText(event MessageEvent) string {
	var builder strings.Builder
	for _, segment := range event.Segments {
		if segment.Type == "text" {
			builder.WriteString(segment.Data["text"])
		}
	}
	text := strings.TrimSpace(builder.String())
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	text = compactRecallImageDescription(text)
	if len([]rune(text)) < 4 || text == "[图片]" || text == "改好了。[图片]" {
		return ""
	}
	return text
}

func (r *Runtime) describeMissingRecallImages(ctx context.Context, event MessageEvent, targets []*recallImageTarget) {
	var pending []*recallImageTarget
	for _, target := range targets {
		if target.description == "" && target.imageSource != "" {
			pending = append(pending, target)
		}
	}
	if len(pending) == 0 {
		return
	}

	type descriptionResult struct {
		target      *recallImageTarget
		description string
		err         error
	}
	jobs := make(chan *recallImageTarget)
	results := make(chan descriptionResult, len(pending))
	workerCount := recallImageDescriptionConcurrency
	if len(pending) < workerCount {
		workerCount = len(pending)
	}
	var workers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for target := range jobs {
				description, err := r.describeRecallImage(ctx, event, target.imageSource)
				results <- descriptionResult{target: target, description: description, err: err}
			}
		}()
	}
	go func() {
		for _, target := range pending {
			jobs <- target
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()

	for result := range results {
		if result.err != nil {
			log.Printf("diana recall image description failed: message_id=%s err=%v", firstNonEmpty(result.target.sourceMessageIDs...), result.err)
			continue
		}
		result.target.description = compactRecallImageDescription(result.description)
		result.target.descriptionSource = "vision"
		r.saveRecallImageDescription(result.target, event)
	}
}

func (r *Runtime) describeRecallImage(ctx context.Context, event MessageEvent, source string) (string, error) {
	const instruction = "请为这张图片生成可复用的客观中文描述。说明主要人物、物体、场景、界面结构，并完整记录清晰可辨的文字、数字和关键细节。不要回答任何聊天问题，不要推测看不清的内容，不要使用 Markdown，控制在1200字以内。"
	return r.describeCachedImage(ctx, event, source, "你是 Diana 的图片内容缓存子代理。输出将作为后续聊天和撤回记录的可靠视觉事实。", instruction, "image_description_cache", 1200)
}

func (r *Runtime) describeStickerImage(ctx context.Context, event MessageEvent, source string) (string, error) {
	const instruction = "请为这张聊天表情包生成简短中文简介。重点说明发送者借这张图表达的潜台词、复合情绪、说话视角、典型触发场景和清晰可辨的原始文字，而不是只描述构图或画风。不要回答当前聊天问题，不要使用 Markdown，控制在180字以内。"
	return r.describeCachedImage(ctx, event, source, "你是 Diana 的表情包语义标注器。简介用于按聊天语境检索合适表情，不能编造看不清的文字、角色或梗来源。", instruction, "sticker_description", 400)
}

func (r *Runtime) describeCachedImage(ctx context.Context, event MessageEvent, source, system, instruction, purpose string, maxOutputTokens int64) (string, error) {
	readyImages := llmReadyImageURLs(ctx, []string{source})
	if len(readyImages) == 0 || !strings.HasPrefix(readyImages[0], "data:image/") {
		return "", fmt.Errorf("cached image is unavailable")
	}
	request := llm.GenerateRequest{
		MaxOutputTokens: maxOutputTokens,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: system},
			{
				Role:    llm.RoleUser,
				Content: instruction,
				Parts: []llm.ContentPart{
					{Type: llm.ContentPartText, Text: instruction},
					{Type: llm.ContentPartImageURL, ImageURL: readyImages[0], Detail: "auto"},
				},
			},
		},
	}
	callCtx := withLLMUsagePurpose(withLLMUsageContext(r.withIdentityPrivacyContext(ctx, event, nil), event), purpose)
	return r.runLLMProviderForGroup(callCtx, llm.GroupVision, func(client LLMProvider) (string, error) {
		response, err := client.Generate(callCtx, request)
		if err != nil {
			return "", err
		}
		description := compactRecallImageDescription(response.Text)
		if description == "" {
			return "", fmt.Errorf("vision model returned an empty description")
		}
		return description, nil
	})
}

func (r *Runtime) saveRecallImageDescription(target *recallImageTarget, event MessageEvent) {
	if target == nil || target.contentSHA256 == "" || strings.TrimSpace(target.description) == "" {
		return
	}
	store := r.recallImageDescriptionStore()
	if store == nil {
		return
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := store.SaveImageDescription(saveCtx, ImageDescriptionRecord{
		ContentSHA256:   target.contentSHA256,
		Description:     target.description,
		SourceSession:   sessionKey(event),
		SourceMessageID: firstNonEmpty(target.sourceMessageIDs...),
		Source:          target.descriptionSource,
		Version:         recallImageDescriptionVersion,
	}); err != nil {
		log.Printf("diana recall image description cache save failed: %v", err)
	}
}
