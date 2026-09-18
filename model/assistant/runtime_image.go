// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

func (r *Runtime) SetMediaStore(store *MediaStore) {
	r.mu.Lock()
	r.media = store
	r.mu.Unlock()
}

func (r *Runtime) SetLocalMediaSharer(sharer LocalMediaSharer) {
	r.mu.Lock()
	r.localMedia = sharer
	r.mu.Unlock()
	if r.plugins != nil {
		r.plugins.SetLocalMediaSharer(sharer)
	}
}

func OneBotGroupAvatarURL(groupID string) string {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return ""
	}
	escaped := url.PathEscape(groupID)
	return "https://p.qlogo.cn/gh/" + escaped + "/" + escaped + "/640"
}

func OneBotMemberAvatarURL(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	return "https://q1.qlogo.cn/g?b=qq&nk=" + url.QueryEscape(userID) + "&s=640"
}

func (r *Runtime) withFileParserVideoLimit(ctx context.Context, event MessageEvent) context.Context {
	settings := SettingValues(nil)
	if r.plugins != nil {
		_, effective, enabled := r.plugins.PluginWithSettingsForGroup(
			fileParserPluginID,
			r.pluginOverridesForEvent(event),
			r.pluginSettingOverridesForEvent(event),
		)
		if enabled {
			settings = effective
		}
	}
	return withVideoContextMaxBytes(ctx, fileParserVideoMaxBytes(settings))
}

func hasReplyCandidateImage(segments []MessageSegment) bool {
	for _, segment := range segments {
		if segment.Type == "image" && segment.Data["source_type"] != "video_frame" {
			return true
		}
	}
	return false
}

func promoteNewImageEvidence(decision *proactiveReplyDecision, event MessageEvent, newImageEvidence bool, threshold float64, chatIn chatInSettings) bool {
	if decision == nil || decision.allows(threshold, chatIn) || !decision.RequestsResponse || decision.Confidence < threshold {
		return false
	}
	if decision.Blocker != proactiveBlockerLowValue || !newImageEvidence {
		return false
	}
	originalReason := strings.TrimSpace(decision.Reason)
	decision.ShouldReply = true
	decision.Category = "needs_response"
	decision.Answerable = true
	decision.Substantive = true
	decision.TargetMessageID = strings.TrimSpace(event.MessageID)
	if decision.TargetMessageID != "" {
		decision.TurnMessageIDs = []string{decision.TargetMessageID}
	}
	decision.Reason = "当前请求附带了此前回答中不存在的新图片证据，不能按文字重复忽略"
	if originalReason != "" {
		decision.Reason += "；Intent Recognition 原判断：" + originalReason
	}
	return true
}

func (r *Runtime) replyRuleVoiceCQ(ctx context.Context, event MessageEvent, rule ReplyRule, reply string) (string, error) {
	if strings.TrimSpace(reply) == "" || isStandaloneRecordReply(reply) {
		return reply, nil
	}
	r.mu.RLock()
	localMedia := r.localMedia
	r.mu.RUnlock()
	var plugin *VoiceTTSPlugin
	var settings SettingValues
	if r.plugins != nil {
		pluginValue, effectiveSettings, enabled := r.plugins.PluginWithSettingsForGroup(
			voiceTTSPluginID,
			r.pluginOverridesForEvent(event),
			r.pluginSettingOverridesForEvent(event),
		)
		var ok bool
		plugin, ok = pluginValue.(*VoiceTTSPlugin)
		if !enabled || !ok {
			return "", fmt.Errorf("语音回复规则 %s 命中，但语音插件未启用", firstNonEmpty(rule.Name, rule.ID))
		}
		settings = effectiveSettings
	}
	if plugin == nil {
		plugin = NewVoiceTTSPlugin(nil)
	}
	plugin.SetLocalMediaSharer(localMedia)
	tool := &dianaTTSTool{plugin: plugin, settings: settings}
	output, err := tool.Run(ctx, map[string]any{"text": reply})
	if err != nil {
		return "", err
	}
	cq, ok := tool.TerminalResult(output)
	if !ok || strings.TrimSpace(cq) == "" {
		return "", fmt.Errorf("语音回复规则 %s 未生成可发送 record", firstNonEmpty(rule.Name, rule.ID))
	}
	return cq, nil
}

func (r *Runtime) visualIntentIdentityImages(event MessageEvent) []visualIntentIdentityImage {
	if event.Kind != EventKindGroup {
		return nil
	}
	cfg := r.effectiveConfigForEvent(event)
	botIDs := map[string]bool{}
	for _, id := range []string{event.SelfID, cfg.BotAccount} {
		if id = strings.TrimSpace(id); id != "" {
			botIDs[id] = true
		}
	}
	var images []visualIntentIdentityImage
	for _, userID := range mentionedUserIDs(event.Segments) {
		if botIDs[userID] {
			continue
		}
		images = append(images, visualIntentIdentityImage{
			Source: "mentioned_member_avatar",
			UserID: userID,
		})
	}
	return images
}

func (r *Runtime) recordImageOperation(ctx context.Context, event MessageEvent, action string, message string, intentPrompt string, submittedPrompt string, model string, imageCount int, sourceCount int) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  action,
		Message: message,
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id":      event.GroupID,
			"user_id":       event.UserID,
			"model":         model,
			"image_count":   imageCount,
			"source_count":  sourceCount,
			"prompt":        truncateRunesFromStart(submittedPrompt, 2000),
			"intent_prompt": truncateRunesFromStart(intentPrompt, 1000),
		},
	})
}

func (r *Runtime) localImageEditSourceImages(event MessageEvent) []string {
	var out []string
	out = appendImageEditSourceImages(out, availableImageURLs(event.Segments)...)
	if event.Quoted != nil {
		out = appendImageEditSourceImages(out, availableImageURLs(event.Quoted.Segments)...)
	}
	out = appendImageEditSourceImages(out, r.semanticReferenceImageURLs(context.Background(), event)...)
	if len(out) > 0 {
		return out
	}
	history := r.contextHistory(event)
	out = appendImageEditSourceImages(out, recentHistoryImageBatch(history, event.MessageID)...)
	return out
}

// imageEditSourceImages 按优先级挑出可编辑的图片：当前消息与引用消息里的图、指代
// 解析选中的图、模型点名的头像来源，最后才退回最近历史图。identitySources 由模型
// 在调用 diana.image 时给出，运行时不再从用户措辞里推断要用谁的头像。
func (r *Runtime) imageEditSourceImages(ctx context.Context, event MessageEvent, identitySources []string) []string {
	var out []string
	out = appendImageEditSourceImages(out, availableImageURLs(event.Segments)...)
	if event.Quoted != nil {
		out = appendImageEditSourceImages(out, availableImageURLs(event.Quoted.Segments)...)
	}
	out = appendImageEditSourceImages(out, r.semanticReferenceImageURLs(ctx, event)...)
	if len(out) > 0 {
		return out
	}
	out = appendImageEditSourceImages(out, r.avatarIdentityImageURLs(ctx, event, identitySources)...)
	if len(out) > 0 {
		return out
	}
	history := r.contextHistory(event)
	out = appendImageEditSourceImages(out, r.preparedRecentHistoryImageBatch(ctx, history, event.MessageID)...)
	return out
}

func recentHistoryImageBatch(history []MessageEvent, currentMessageID string) []string {
	selected := recentHistoryImageIndexes(history, currentMessageID)
	var out []string
	for index, item := range history {
		if !selected[index] {
			continue
		}
		images := appendUniqueStrings(nil, availableImageURLs(item.Segments)...)
		if item.Quoted != nil {
			images = appendUniqueStrings(images, availableImageURLs(item.Quoted.Segments)...)
		}
		out = appendImageEditSourceImages(out, images...)
	}
	return out
}

func (r *Runtime) preparedRecentHistoryImageBatch(ctx context.Context, history []MessageEvent, currentMessageID string) []string {
	selected := recentHistoryImageIndexes(history, currentMessageID)
	var out []string
	for index, item := range history {
		if !selected[index] {
			continue
		}
		prepared := r.prepareHistoricalEventImages(ctx, item)
		if historicalImageStateChanged(item, prepared) {
			r.updateHistoricalImageState(prepared)
		}
		item = prepared
		images := appendUniqueStrings(nil, availableImageURLs(item.Segments)...)
		if item.Quoted != nil {
			images = appendUniqueStrings(images, availableImageURLs(item.Quoted.Segments)...)
		}
		out = appendImageEditSourceImages(out, images...)
	}
	return out
}

func recentHistoryImageIndexes(history []MessageEvent, currentMessageID string) map[int]bool {
	selected := map[int]bool{}
	started := false
	leadMessages := 0
	separatorMessages := 0
	newestImageTime := int64(0)
	for index := len(history) - 1; index >= 0; index-- {
		item := history[index]
		if strings.TrimSpace(currentMessageID) != "" && item.MessageID == currentMessageID {
			continue
		}
		imageCount := historicalStillImageCount(item)
		if imageCount == 0 {
			if started {
				separatorMessages++
				if separatorMessages > recentImageBatchSeparatorMessages {
					break
				}
				continue
			}
			leadMessages++
			if leadMessages > recentImageBatchLeadMessages {
				break
			}
			continue
		}
		if started && newestImageTime > 0 && item.Time > 0 && newestImageTime-item.Time > int64(recentImageBatchWindow/time.Second) {
			break
		}
		started = true
		separatorMessages = 0
		if newestImageTime == 0 {
			newestImageTime = item.Time
		}
		selected[index] = true
	}
	return selected
}

func (r *Runtime) agentHistoryImageBatchMessage(ctx context.Context, history []MessageEvent, selected map[int]bool, currentTime int64) (llm.Message, error) {
	if len(selected) == 0 {
		return llm.Message{}, nil
	}
	var lines []string
	for index, item := range history {
		if !selected[index] {
			continue
		}
		line := agentImageHistoryPromptTextWithDescriptions(item, currentTime, r.historyImageCachedDescriptions(ctx, item))
		if line != "" {
			lines = append(lines, line)
		}
	}
	text := strings.Join(lines, "\n")
	if text == "" {
		return llm.Message{}, nil
	}
	return llm.Message{
		Role:     llm.RoleUser,
		Content:  text,
		Priority: llm.MessagePriorityHistory,
	}, nil
}

// sourceImagesAllAttached 判断某个引用来源的图片是否都已经以原图形式附给模型。
// 取不到 URL 的分片按「没附上」处理，宁可多给一句摘要，也不要让模型对着空手猜。
func sourceImagesAllAttached(source MessageEvent, attached map[string]bool) bool {
	urls := availableImageURLs(source.Segments)
	if len(urls) == 0 {
		return false
	}
	if len(urls) != historicalStillImageCount(source) {
		return false
	}
	for _, url := range urls {
		if !attached[strings.TrimSpace(url)] {
			return false
		}
	}
	return true
}

// agentCurrentHistoricalImageReference 为当前轮被引用、但原图没能附上的来源补一段
// 文字说明。attachedImageURLs 里已经有原图的来源直接跳过：模型既看到图又看到一句
// 「尚无缓存描述」只会自相矛盾。
func (r *Runtime) agentCurrentHistoricalImageReference(ctx context.Context, event MessageEvent, attachedImageURLs []string) string {
	attached := make(map[string]bool, len(attachedImageURLs))
	for _, url := range attachedImageURLs {
		if url = strings.TrimSpace(url); url != "" {
			attached[url] = true
		}
	}
	var lines []string
	seen := map[string]bool{}
	appendEvent := func(source MessageEvent) {
		messageID := strings.TrimSpace(source.MessageID)
		if messageID == "" || seen[messageID] || historicalStillImageCount(source) == 0 {
			return
		}
		seen[messageID] = true
		if len(attached) > 0 && sourceImagesAllAttached(source, attached) {
			return
		}
		lines = append(lines, agentImageHistoryPromptTextWithDescriptions(source, event.Time, r.historyImageCachedDescriptions(ctx, source)))
	}
	if event.Quoted != nil {
		quotedEvent := MessageEvent{
			Kind:       event.Kind,
			GroupID:    firstNonEmpty(event.Quoted.GroupID, event.GroupID),
			UserID:     event.Quoted.UserID,
			MessageID:  event.Quoted.MessageID,
			RawMessage: event.Quoted.RawMessage,
			Segments:   event.Quoted.Segments,
			SenderName: event.Quoted.SenderName,
		}
		appendEvent(quotedEvent)
	}
	for _, messageID := range eventSemanticSourceMessageIDs(event) {
		if source, found := r.findSemanticReferenceEvent(ctx, event, messageID); found {
			appendEvent(source)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "【当前消息引用的历史图片仍未附加原图】\n" + strings.Join(lines, "\n")
}

func segmentsWithoutHistoricalStillImages(segments []MessageSegment) []MessageSegment {
	out := make([]MessageSegment, 0, len(segments))
	for _, segment := range segments {
		if !recallStillImageSegment(segment) {
			out = append(out, segment)
		}
	}
	return out
}

func unavailableImageSegmentCount(segments []MessageSegment) int {
	count := 0
	for _, segment := range segments {
		if segment.Type == "image" && strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") {
			count++
		}
	}
	return count
}

func eventWithAvailableImages(event MessageEvent) MessageEvent {
	hadImages := hasImageSegment(event.Segments)
	event.Segments = segmentsWithAvailableImages(event.Segments)
	if hadImages && !hasImageSegment(event.Segments) && strings.TrimSpace(PlainText(event.Segments)) == "" {
		event.RawMessage = rawMessageWithoutImagePlaceholders(event.RawMessage)
	}
	if event.Quoted != nil {
		quoted := *event.Quoted
		hadQuotedImages := hasImageSegment(quoted.Segments)
		quoted.Segments = segmentsWithAvailableImages(quoted.Segments)
		if hadQuotedImages && !hasImageSegment(quoted.Segments) && strings.TrimSpace(PlainText(quoted.Segments)) == "" {
			quoted.RawMessage = rawMessageWithoutImagePlaceholders(quoted.RawMessage)
		}
		event.Quoted = &quoted
	}
	return event
}

func rawMessageWithoutImagePlaceholders(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "[CQ:") {
		return strings.TrimSpace(PlainText(CQToSegments(raw)))
	}
	return strings.TrimSpace(strings.ReplaceAll(raw, "[图片]", ""))
}

func historicalImageStateChanged(before, after MessageEvent) bool {
	return imageSegmentStateChanged(before.Segments, after.Segments) ||
		(before.Quoted != nil && after.Quoted != nil && imageSegmentStateChanged(before.Quoted.Segments, after.Quoted.Segments))
}

func imageSegmentStateChanged(before, after []MessageSegment) bool {
	if len(before) != len(after) {
		return true
	}
	for index := range before {
		if before[index].Type != "image" {
			continue
		}
		for _, key := range []string{"cached_file", imageUnavailableKey, imageSourceFailedKey, imageContentSHA256Key} {
			if before[index].Data[key] != after[index].Data[key] {
				return true
			}
		}
	}
	return false
}

func (r *Runtime) updateHistoricalImageState(event MessageEvent) {
	session := sessionKey(event)
	r.mu.Lock()
	for index := range r.history[session] {
		if r.history[session][index].MessageID == event.MessageID {
			r.history[session][index] = withoutReplyRuntimeState(event)
			break
		}
	}
	r.mu.Unlock()
	r.persistMessageEvent(event)
}

func segmentsWithAvailableImages(segments []MessageSegment) []MessageSegment {
	out := make([]MessageSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.Type == "image" && strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") {
			continue
		}
		out = append(out, segment)
	}
	return out
}

func appendImageEditSourceImages(out []string, images ...string) []string {
	for _, imageURL := range images {
		imageURL = strings.TrimSpace(imageURL)
		if imageURL == "" {
			continue
		}
		var seen bool
		for _, existing := range out {
			if existing == imageURL {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		out = append(out, imageURL)
	}
	return out
}

func (r *Runtime) generateImageWithFailover(ctx context.Context, req llm.ImageGenerateRequest) (*llm.ImageGenerateResponse, llm.ProviderConfig, error) {
	configs := r.imageProviderConfigs(ctx)
	if len(configs) == 0 {
		return nil, llm.ProviderConfig{}, fmt.Errorf("diana: llm profile store is not configured")
	}
	var lastErr error
	for _, cfg := range configs {
		if err := ctx.Err(); err != nil {
			return nil, llm.ProviderConfig{}, err
		}
		request := req
		request.Model = cfg.ImageModelWithDefault()
		resp, err := llm.GenerateImage(ctx, cfg, request)
		if err == nil {
			return resp, cfg, nil
		}
		lastErr = err
	}
	return nil, llm.ProviderConfig{}, lastErr
}

func (r *Runtime) editImageWithFailover(ctx context.Context, req llm.ImageEditRequest) (*llm.ImageGenerateResponse, llm.ProviderConfig, error) {
	configs := r.imageProviderConfigs(ctx)
	if len(configs) == 0 {
		return nil, llm.ProviderConfig{}, fmt.Errorf("diana: llm profile store is not configured")
	}
	var lastErr error
	for _, cfg := range configs {
		if err := ctx.Err(); err != nil {
			return nil, llm.ProviderConfig{}, err
		}
		request := req
		request.Model = cfg.ImageModelWithDefault()
		resp, err := llm.EditImage(ctx, cfg, request)
		if err == nil {
			return resp, cfg, nil
		}
		lastErr = err
	}
	return nil, llm.ProviderConfig{}, lastErr
}

func messagesContainImages(messages []llm.Message) bool {
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Type == llm.ContentPartImageURL {
				return true
			}
		}
	}
	return false
}

func messagesContainAudio(messages []llm.Message) bool {
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Type == llm.ContentPartInputAudio && strings.TrimSpace(part.AudioData) != "" {
				return true
			}
		}
	}
	return false
}

func videoOnlyMessage(event MessageEvent, fallback string) bool {
	if len(event.Segments) > 0 {
		hasVideo := false
		for _, segment := range event.Segments {
			switch segment.Type {
			case "video":
				hasVideo = true
			case "file":
				if !videoFileSegment(segment) {
					return false
				}
				hasVideo = true
			case "text":
				if strings.TrimSpace(segment.Data["text"]) != "" {
					return false
				}
			case "at", "reply", "image":
				// 允许 @/引用/图片跟视频一起出现，只要没有正文就不触发 LLM。
			default:
				return false
			}
		}
		return hasVideo
	}
	raw := strings.TrimSpace(firstNonEmpty(event.RawMessage, fallback))
	if raw == "" {
		return false
	}
	if strings.Contains(raw, "[CQ:video") {
		return videoOnlyMessage(MessageEvent{Segments: CQToSegments(raw)}, "")
	}
	return strings.TrimSpace(strings.ReplaceAll(raw, "[视频]", "")) == ""
}

func appendUniqueForwardMedia(segments, media []MessageSegment) []MessageSegment {
	out := append([]MessageSegment(nil), segments...)
	for _, candidate := range media {
		duplicate := false
		for _, existing := range out {
			if existing.Data["forward_id"] == candidate.Data["forward_id"] &&
				existing.Data["source_message_id"] == candidate.Data["source_message_id"] &&
				mediaSegmentsMatch(existing, candidate) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, candidate)
		}
	}
	return out
}

func forwardMediaSegmentsFromOneBotData(data map[string]any, forwardID string) []MessageSegment {
	if len(data) == 0 {
		return nil
	}
	nodes := firstNonNil(data["messages"], data["message"], data["forward"])
	var out []MessageSegment
	collectForwardMediaSegments(nodes, forwardMediaSource{ForwardID: forwardID}, 0, &out)
	return out
}

func collectForwardMediaSegments(value any, source forwardMediaSource, depth int, out *[]MessageSegment) {
	if value == nil || depth > 4 {
		return
	}
	switch item := value.(type) {
	case []any:
		for _, entry := range item {
			collectForwardMediaSegments(entry, source, depth, out)
		}
		return
	case []map[string]any:
		for _, entry := range item {
			collectForwardMediaSegments(entry, source, depth, out)
		}
		return
	case map[string]any:
		collectForwardMediaMap(item, source, depth, out)
		return
	case []MessageSegment:
		for _, segment := range item {
			appendForwardMediaSegment(segment, source, out)
		}
		return
	}

	segments := messageSegmentsFromAny(value)
	for _, segment := range segments {
		appendForwardMediaSegment(segment, source, out)
	}
}

func collectForwardMediaMap(node map[string]any, source forwardMediaSource, depth int, out *[]MessageSegment) {
	typeName := strings.ToLower(stringFromAny(node["type"]))
	data, _ := node["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	if typeName == "image" || typeName == "video" || typeName == "file" {
		if segment, ok := messageSegmentFromMap(node); ok {
			appendForwardMediaSegment(segment, source, out)
		}
		return
	}
	if typeName == "node" {
		source = forwardMediaSourceFromMap(source, node)
		source = forwardMediaSourceFromMap(source, data)
		content := firstNonNil(data["content"], data["message"], node["message"])
		collectForwardMediaSegments(content, source, depth+1, out)
		if nested := firstNonNil(data["messages"], data["forward"], node["messages"]); nested != nil {
			collectForwardMediaSegments(nested, source, depth+1, out)
		}
		return
	}
	if typeName == "forward" {
		collectForwardMediaSegments(firstNonNil(data["content"], data["message"], data["messages"]), source, depth+1, out)
		return
	}

	// NapCat returns full OneBot message objects for received merged forwards,
	// while go-cqhttp-style implementations may return node segments.
	source = forwardMediaSourceFromMap(source, node)
	collectForwardMediaSegments(firstNonNil(node["message"], node["content"]), source, depth+1, out)
	if nested := firstNonNil(node["messages"], node["forward"]); nested != nil {
		collectForwardMediaSegments(nested, source, depth+1, out)
	}
}

func forwardMediaSourceFromMap(source forwardMediaSource, data map[string]any) forwardMediaSource {
	sender, _ := data["sender"].(map[string]any)
	source.MessageID = firstNonEmpty(stringFromAny(data["message_id"]), stringFromAny(data["message_seq"]), source.MessageID)
	source.GroupID = firstNonEmpty(stringFromAny(data["group_id"]), source.GroupID)
	source.UserID = firstNonEmpty(stringFromAny(data["user_id"]), stringFromAny(data["uin"]), stringFromAny(sender["user_id"]), source.UserID)
	source.Name = firstNonEmpty(
		stringFromAny(data["name"]),
		stringFromAny(data["nickname"]),
		stringFromAny(sender["card"]),
		stringFromAny(sender["nickname"]),
		source.Name,
	)
	return source
}

func appendForwardMediaSegment(segment MessageSegment, source forwardMediaSource, out *[]MessageSegment) {
	if segment.Type != "image" && segment.Type != "video" && segment.Type != "file" {
		return
	}
	segment.Data = cloneSegmentData(segment.Data)
	segment.Data["forward_id"] = source.ForwardID
	if source.MessageID != "" {
		segment.Data["source_message_id"] = source.MessageID
	}
	if source.GroupID != "" {
		segment.Data["source_group_id"] = source.GroupID
	}
	if source.UserID != "" {
		segment.Data["source_user_id"] = source.UserID
	}
	if source.Name != "" {
		segment.Data["forward_sender_name"] = source.Name
	}
	*out = append(*out, segment)
}

// historicalMediaSummary 把媒体计数压成「图片×2、语音×1」这种只列非零项的短句。
func historicalMediaSummary(imageCount, videoCount, videoFrameCount, audioCount, fileCount int) string {
	parts := make([]string, 0, 5)
	for _, item := range []struct {
		label string
		count int
	}{
		{"图片", imageCount},
		{"视频", videoCount},
		{"视频关键帧", videoFrameCount},
		{"语音", audioCount},
		{"文件", fileCount},
	} {
		if item.count > 0 {
			parts = append(parts, item.label+"×"+itoa(item.count))
		}
	}
	return strings.Join(parts, "、")
}

func historicalStillImageCount(event MessageEvent) int {
	return len(historicalStillImageSegments(event))
}

func historicalMediaCount(event MessageEvent) int {
	return historicalStillImageCount(event) + historicalVideoCount(event) + historicalVideoFrameCount(event) + historicalAudioCount(event) + historicalFileCount(event)
}

func historicalVideoCount(event MessageEvent) int {
	count := 0
	countSegments := func(segments []MessageSegment) {
		for _, segment := range segments {
			if segment.Type == "video" || (segment.Type == "file" && videoFileSegment(segment)) {
				count++
			}
		}
	}
	countSegments(event.Segments)
	if event.Quoted != nil {
		countSegments(event.Quoted.Segments)
	}
	return count
}

func historicalVideoFrameCount(event MessageEvent) int {
	count := 0
	countSegments := func(segments []MessageSegment) {
		for _, segment := range segments {
			if segment.Type == "image" && strings.EqualFold(strings.TrimSpace(segment.Data["source_type"]), "video_frame") {
				count++
			}
		}
	}
	countSegments(event.Segments)
	if event.Quoted != nil {
		countSegments(event.Quoted.Segments)
	}
	return count
}

func historicalAudioCount(event MessageEvent) int {
	return historicalSegmentCount(event, func(segment MessageSegment) bool { return segment.Type == "record" })
}

// historyImageCachedDescriptions 只同步读取已有缓存；缺失描述只进入后台队列，
// 不在每轮常规回复的关键路径里等待识图网络调用。
func (r *Runtime) historyImageCachedDescriptions(ctx context.Context, event MessageEvent) []string {
	r.enqueueHistoryImageDescriptions(event)
	segments := append([]MessageSegment(nil), event.Segments...)
	if event.Quoted != nil {
		segments = append(segments, event.Quoted.Segments...)
	}
	return r.historyImageCachedSegmentDescriptions(ctx, segments)
}

func (r *Runtime) historyImageCachedSegmentDescriptions(ctx context.Context, segments []MessageSegment) []string {
	store := r.recallImageDescriptionStore()
	var lines []string
	imageIndex := 0
	videoFrameIndex := 0
	for _, segment := range segments {
		if !historyDescribableImageSegment(segment) {
			continue
		}
		videoFrame := strings.EqualFold(strings.TrimSpace(segment.Data["source_type"]), "video_frame")
		label := "图片"
		index := 0
		if videoFrame {
			videoFrameIndex++
			label = "视频关键帧"
			index = videoFrameIndex
		} else {
			imageIndex++
			index = imageIndex
		}
		description := strings.TrimSpace(segment.Data[recallImageDescriptionKey])
		if description == "" && store != nil {
			if hash, ok := imageSegmentContentSHA256(segment); ok {
				if record, found, err := store.GetImageDescription(ctx, hash); err == nil && found {
					description = strings.TrimSpace(record.Description)
				} else if err != nil {
					log.Printf("diana history image description cache load failed: %v", err)
				}
			}
		}
		if description == "" {
			lines = append(lines, fmt.Sprintf("%s%d摘要=尚无缓存描述", label, index))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s%d摘要=%s", label, index, truncateRunes(compactRecallImageDescription(description), historyImageDescriptionMaxRunes)))
	}
	lines = append(lines, historicalNonImageMediaDescriptions(segments)...)
	return lines
}

func historicalNonImageMediaDescriptions(segments []MessageSegment) []string {
	lines := make([]string, 0)
	audioIndex, fileIndex := 0, 0
	for _, segment := range segments {
		switch segment.Type {
		case "record":
			audioIndex++
			transcript := strings.TrimSpace(segment.Data[voiceSTTTranscriptKey])
			if transcript == "" {
				lines = append(lines, fmt.Sprintf("语音%d摘要=尚无可用转写", audioIndex))
				continue
			}
			lines = append(lines, fmt.Sprintf("语音%d转写=%s", audioIndex, truncateRunes(strings.Join(strings.Fields(transcript), " "), historyImageDescriptionMaxRunes)))
		case "file":
			fileIndex++
			name := strings.TrimSpace(firstNonEmpty(segment.Data["name"], segment.Data["filename"], segment.Data["fileName"], segment.Data["file"]))
			if name == "" {
				name = "未命名文件"
			}
			format := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
			if format == "" {
				format = "未知"
			}
			description := strings.TrimSpace(firstNonEmpty(segment.Data["summary"], segment.Data["description"], segment.Data["parsed_text"], segment.Data["content"]))
			line := fmt.Sprintf("文件%d摘要=文件名：%s；格式：%s", fileIndex, name, format)
			if description != "" {
				line += "；内容摘要：" + truncateRunes(strings.Join(strings.Fields(description), " "), historyImageDescriptionMaxRunes)
			} else if isSupportedFileName(name) {
				line += "；正文尚未解析"
			} else {
				line += "；当前格式不支持正文解析"
			}
			lines = append(lines, line)
		}
	}
	return lines
}

// videoFailureReason 补上兜底文案：拿不到具体原因时也不能把这句写成空的，
// 否则提示词会变成「原因：把这个原因告诉用户」。
func videoFailureReason(reason string) string {
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		return trimmed + " "
	}
	return "未知，日志里也没有更多线索。 "
}

func forwardVideoFrameManifest(event MessageEvent) string {
	segments := event.Segments
	if event.Quoted != nil {
		segments = append(append([]MessageSegment(nil), segments...), event.Quoted.Segments...)
	}
	type source struct {
		name, messageID string
		frames          int
	}
	order := make([]string, 0, 2)
	sources := map[string]*source{}
	for _, segment := range segments {
		if segment.Type != "image" || segment.Data["source_type"] != "video_frame" || strings.TrimSpace(segment.Data["forward_id"]) == "" {
			continue
		}
		key := strings.Join([]string{segment.Data["forward_id"], segment.Data["source_message_id"], segment.Data["video_index"]}, "\x00")
		if sources[key] == nil {
			order = append(order, key)
			sources[key] = &source{name: strings.TrimSpace(segment.Data["forward_sender_name"]), messageID: strings.TrimSpace(segment.Data["source_message_id"])}
		}
		sources[key].frames++
	}
	if len(order) == 0 {
		return ""
	}
	var lines []string
	for index, key := range order {
		item := sources[key]
		label := fmt.Sprintf("视频节点 %d", index+1)
		if item.name != "" {
			label += "，发送者 " + item.name
		}
		if item.messageID != "" {
			label += "，源消息 " + item.messageID
		}
		lines = append(lines, fmt.Sprintf("%s，已附加 %d 张画面。", label, item.frames))
	}
	return "\n" + strings.Join(lines, "\n") + "\n"
}

func hasVideoSegment(segments []MessageSegment) bool {
	for _, segment := range segments {
		if videoFileSegment(segment) {
			return true
		}
	}
	return false
}

func pluginImageURLs(responses []PluginResponse) []string {
	var out []string
	for _, resp := range responses {
		out = append(out, resp.ImageURLs...)
	}
	return out
}

func withoutMessageImageURLs(imageURLs []string, messages []llm.Message) []string {
	seen := map[string]bool{}
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Type == llm.ContentPartImageURL && strings.TrimSpace(part.ImageURL) != "" {
				seen[part.ImageURL] = true
			}
		}
	}
	filtered := make([]string, 0, len(imageURLs))
	for _, imageURL := range imageURLs {
		if imageURL = strings.TrimSpace(imageURL); imageURL != "" && !seen[imageURL] {
			filtered = append(filtered, imageURL)
		}
	}
	return filtered
}

// resolveOutgoingLocalImages 把消息里的本地图片路径换成桥能访问的共享 URL。
// 视频早有这层转换(prepareResolverVideoDelivery),图片一直漏着:X 图片下载
// 到宿主机临时目录后,绝对路径被直接塞进转发节点,桥运行在容器或另一台机器
// 上时根本读不到——合并转发、暂存、散装三条路挨个失败,重试耗尽后整条事件
// 被丢弃。换不成时保留原路径,桥与宿主同机的部署行为不变。
func (r *Runtime) resolveOutgoingLocalImages(msg OutgoingMessage) OutgoingMessage {
	if len(msg.ImageURLs) == 0 {
		return msg
	}
	// TelegramChannel 与后端在同一进程，绝对路径应直接走 multipart 上传；换成
	// WebUI 分享 URL 后 Telegram 服务器可能拿到登录页或代理错误页并报媒体类型错误。
	if NormalizePlatformID(msg.Platform) == PlatformTelegram {
		return msg
	}
	resolved := make([]string, 0, len(msg.ImageURLs))
	changed := false
	for _, imageURL := range msg.ImageURLs {
		if path := localMediaPath(imageURL); path != "" {
			if sharedURL, ok := r.shareLocalMedia(path); ok {
				resolved = append(resolved, sharedURL)
				changed = true
				continue
			}
		}
		resolved = append(resolved, imageURL)
	}
	if changed {
		msg.ImageURLs = resolved
	}
	return msg
}

func (r *Runtime) shareLocalMedia(path string) (string, bool) {
	r.mu.RLock()
	sharer := r.localMedia
	r.mu.RUnlock()
	if sharer == nil {
		return "", false
	}
	return sharer.Share(path, resolverLocalMediaTTL)
}

func splitForwardResolverVideoUploads(messages []OutgoingMessage) ([]OutgoingMessage, []resolverVideoUpload) {
	forwardMessages := make([]OutgoingMessage, 0, len(messages))
	uploads := make([]resolverVideoUpload, 0)
	for _, msg := range messages {
		directVideoURLs, uploadVideos := splitResolverVideoUploads(msg.VideoURLs)
		msg.VideoURLs = directVideoURLs
		if !outgoingMessageEmpty(msg) {
			forwardMessages = append(forwardMessages, msg)
		}
		uploads = append(uploads, uploadVideos...)
	}
	return forwardMessages, uploads
}

func resolverVideoUploadNotice(upload resolverVideoUpload) string {
	if upload.SizeMB > 0 {
		return fmt.Sprintf("解析视频 %.1f MB，已改用文件发送，请稍等...", upload.SizeMB)
	}
	return "解析视频已改用文件发送，请稍等..."
}

func resolverPluginResponseVideoURLs(resp PluginResponse, messages []OutgoingMessage) []string {
	out := append([]string(nil), resp.VideoURLs...)
	for _, msg := range messages {
		out = append(out, msg.VideoURLs...)
	}
	return dedupeStrings(out)
}

func (r *Runtime) uploadResolverVideoFile(ctx context.Context, event MessageEvent, upload resolverVideoUpload) error {
	platform, err := r.outboundPlatformForEvent(event)
	if err != nil {
		return err
	}
	if !IsOneBotPlatform(platform) {
		event.Platform = platform
		return r.sendOutgoing(ctx, event, routeOutgoingToEvent(event, OutgoingMessage{VideoURLs: []string{upload.Path}}))
	}
	if r.channel == nil {
		return fmt.Errorf("diana: channel is not configured")
	}
	file := upload.Path
	// 桥可能运行在容器或另一台机器上，宿主机路径对它不可见；能生成共享
	// URL 时优先传 URL，桥端会自行下载后再上传。
	if sharedURL, ok := r.shareLocalMedia(upload.Path); ok {
		file = sharedURL
	}
	params := map[string]any{
		"file": file,
		"name": upload.Name,
	}
	action := "upload_private_file"
	if event.Kind == EventKindGroup {
		groupID, err := strconv.ParseInt(event.GroupID, 10, 64)
		if err != nil {
			return fmt.Errorf("diana: invalid group id %q", event.GroupID)
		}
		action = "upload_group_file"
		params["group_id"] = groupID
	} else {
		userID, err := strconv.ParseInt(event.UserID, 10, 64)
		if err != nil {
			return fmt.Errorf("diana: invalid user id %q", event.UserID)
		}
		params["user_id"] = userID
	}
	if blockedErr := r.blockedGroupSendError(event); blockedErr != nil {
		return blockedErr
	}
	_, err = r.executeOutboundCall(ctx, event, action, func(callCtx context.Context) (map[string]any, error) {
		return r.callOneBotAPIForEvent(callCtx, event, action, params)
	})
	return err
}

func appendHistoryImageSegments(segments []MessageSegment, imageURLs []string) []MessageSegment {
	for _, imageURL := range imageURLs {
		imageURL = strings.TrimSpace(imageURL)
		if imageURL == "" {
			continue
		}
		segments = append(segments, MessageSegment{
			Type: "image",
			Data: map[string]string{"file": imageURL},
		})
	}
	return segments
}
