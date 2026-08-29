// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 图片溯源的聊天入口。
//
// 做成工具而不是关键词命令：「这图哪来的」「谁画的」「出处」「源」的说法太多，
// 关键词表永远补不全，而模型本来就在读这句话。工具只负责取图、反查、回报。
const dianaImageSourceToolName = "diana.image_source"

// imageRecognitionKindSource 与 OCR、画面描述共用那张识别结果表，kind 区分用途。
const imageRecognitionKindSource = "source"

type dianaImageSourceTool struct {
	runtime  *Runtime
	event    MessageEvent
	plugin   *ImageSourcePlugin
	settings SettingValues
}

func newDianaImageSourceTool(runtime *Runtime, event MessageEvent, plugin *ImageSourcePlugin, settings SettingValues) *dianaImageSourceTool {
	return &dianaImageSourceTool{runtime: runtime, event: event, plugin: plugin, settings: settings}
}

func (t *dianaImageSourceTool) Name() string { return dianaImageSourceToolName }

func (t *dianaImageSourceTool) Description() string {
	return `反查一张图片的出处：把图上传到图库做以图搜图，返回可能的原作名、作者和链接（插画走 SauceNAO，番剧截图走 trace.moe）。` +
		`用户问「这图哪来的」「谁画的」「出处」「是哪部番」这类问题时用它——你自己看图只能看出画的是什么，看不出它出自哪里。` +
		`默认反查当前消息（或引用消息）里的图片；要查更早的图就传那条消息的 message_id。` +
		`结果是机器比对出来的候选，相似度不高时要如实说明这只是猜测，不要把它当定论。`
}

func (t *dianaImageSourceTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"message_id":  toolStringParam("要反查的图片所在的消息 ID；省略表示当前消息或它引用的那条。"),
		"image_index": toolIntParam("这条消息里的第几张图，从 1 开始，默认第 1 张。", 1, 8),
	})
}

type dianaImageSourceResult struct {
	OK        bool               `json:"ok"`
	Message   string             `json:"message"`
	MessageID string             `json:"message_id,omitempty"`
	Matches   []ImageSourceMatch `json:"matches,omitempty"`
	Notes     []string           `json:"notes,omitempty"`
	Cached    bool               `json:"cached,omitempty"`
}

func (t *dianaImageSourceTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil || t.plugin == nil {
		return "", fmt.Errorf("diana image source: runtime is not configured")
	}
	cfg := imageSourceConfigFromSettings(t.settings)
	if t.event.Kind == EventKindPrivate && !cfg.PrivateEnabled {
		return marshalImageSourceResult(dianaImageSourceResult{Message: "私聊里没有开启图片溯源。"}), nil
	}
	if !cfg.anyProviderUsable() {
		// 「开了 SauceNAO 但没填 Key」要说清楚，否则用户只会看到「没查到」，
		// 然后以为是图的问题。
		return marshalImageSourceResult(dianaImageSourceResult{Message: imageSourceUnavailableReason(cfg)}), nil
	}

	messageID := strings.TrimSpace(configToolString(input, "message_id"))
	index := imageSourceIndex(input)
	image, resolvedID, err := t.resolveImage(ctx, messageID, index)
	if err != nil {
		return marshalImageSourceResult(dianaImageSourceResult{Message: err.Error()}), nil
	}

	digest := imageSourceDigest(image)
	cacheKey := imageSourceCacheKey(digest, cfg)
	if cached, ok := t.cachedMatches(ctx, cacheKey); ok {
		return marshalImageSourceResult(dianaImageSourceResult{
			OK:        true,
			Cached:    true,
			MessageID: resolvedID,
			Matches:   cached,
			Message:   imageSourceSummary(cached, nil),
		}), nil
	}

	if limit := cfg.maxUploadBytes(); int64(len(image)) > limit {
		return marshalImageSourceResult(dianaImageSourceResult{
			MessageID: resolvedID,
			Message: fmt.Sprintf("这张图 %.1f MB，超过了 %d MB 的上传上限，没有发去反查。",
				float64(len(image))/(1024*1024), cfg.MaxUploadMB),
		}), nil
	}

	searchCtx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()
	matches, notes := t.plugin.search(searchCtx, cfg, image)
	if len(matches) > 0 {
		t.storeMatches(ctx, cacheKey, digest, matches)
	}
	return marshalImageSourceResult(dianaImageSourceResult{
		OK:        true,
		MessageID: resolvedID,
		Matches:   matches,
		Notes:     notes,
		Message:   imageSourceSummary(matches, notes),
	}), nil
}

func imageSourceUnavailableReason(cfg imageSourceConfig) string {
	if cfg.SauceNAOEnabled && cfg.SauceNAOKey == "" {
		return "SauceNAO 还没有填 API Key，trace.moe 也没启用，暂时查不了出处。"
	}
	return "图片溯源的两条线路都没启用。"
}

// imageSourceSummary 给模型一句结论，省得它对着空数组自由发挥。
func imageSourceSummary(matches []ImageSourceMatch, notes []string) string {
	if len(matches) == 0 {
		summary := "没有查到匹配的出处，可能是原创图、二次加工过，或者图库里没有收录。"
		if len(notes) > 0 {
			summary += "（" + strings.Join(notes, "；") + "）"
		}
		return summary
	}
	best := matches[0]
	return fmt.Sprintf("查到 %d 条候选，最高相似度 %.1f%%（来自 %s）。相似度低于 80%% 时要说明这只是可能的出处。",
		len(matches), best.Similarity, best.Provider)
}

func imageSourceIndex(input map[string]any) int {
	indexes, err := positiveIntegerList(input["image_index"])
	if err == nil && len(indexes) > 0 {
		return indexes[0]
	}
	return 1
}

// resolveImage 找出要反查的那张图并解码成字节。
//
// 顺序是：指定的消息 → 当前消息自己带的图 → 引用/语义来源。当前消息带图时
// 优先用它，那通常就是刚发出来、正在问的这张。
func (t *dianaImageSourceTool) resolveImage(ctx context.Context, messageID string, index int) ([]byte, string, error) {
	if messageID == "" {
		if image, err := t.imageFromSegments(ctx, t.event.Segments, index); err == nil {
			return image, strings.TrimSpace(t.event.MessageID), nil
		}
		messageID = imageSourceFallbackMessageID(t.event)
		if messageID == "" {
			return nil, "", fmt.Errorf("这条消息里没有图片；要查更早的图，请先用 %s 找到那条消息的 message_id 再传进来", dianaChatHistoryToolName)
		}
	}

	source, found, persistState := newDianaHistoryImagesTool(t.runtime, t.event).findSourceEvent(ctx, messageID)
	if !found {
		return nil, "", fmt.Errorf("当前会话里找不到消息 %s", messageID)
	}
	original := cloneHistoricalImageEvent(source)
	source = cloneHistoricalImageEvent(source)
	refs := historicalStillImageRefs(source)
	if len(refs) == 0 {
		return nil, "", fmt.Errorf("消息 %s 里没有图片", messageID)
	}
	if index < 1 || index > len(refs) {
		return nil, "", fmt.Errorf("消息 %s 里只有 %d 张图，没有第 %d 张", messageID, len(refs), index)
	}
	ref := refs[index-1]
	segment := t.runtime.prepareHistoricalImageSegment(ctx, source, ref)
	setHistoricalStillImageSegment(&source, ref, segment)
	if persistState && historicalImageStateChanged(original, source) {
		t.runtime.updateHistoricalImageState(source)
	}
	image, err := t.imageFromSegments(ctx, []MessageSegment{segment}, 1)
	if err != nil {
		return nil, "", fmt.Errorf("消息 %s 的原图已失效或读取失败", messageID)
	}
	return image, messageID, nil
}

// imageFromSegments 把消息段里的第 index 张图解码成原始字节。
func (t *dianaImageSourceTool) imageFromSegments(ctx context.Context, segments []MessageSegment, index int) ([]byte, error) {
	sources := availableImageURLs(segments)
	if index < 1 || index > len(sources) {
		return nil, fmt.Errorf("image source: no image at index %d", index)
	}
	ready, complete := loadLLMImageURLs(ctx, sources[index-1:index])
	if !complete || len(ready) == 0 {
		return nil, fmt.Errorf("image source: image could not be loaded")
	}
	data, _, err := imageOCRDataURLBytes(ready[0])
	if err != nil {
		return nil, err
	}
	return data, nil
}

// imageSourceFallbackMessageID 当前消息没带图时的退路：先看引用的那条，
// 再看语义指代解析出来的来源。
func imageSourceFallbackMessageID(event MessageEvent) string {
	if event.Quoted != nil && strings.TrimSpace(event.Quoted.MessageID) != "" {
		return strings.TrimSpace(event.Quoted.MessageID)
	}
	for _, id := range eventSemanticSourceMessageIDs(event) {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func imageSourceDigest(image []byte) string {
	sum := sha256.Sum256(image)
	return hex.EncodeToString(sum[:])
}

// imageSourceCacheKey 把过滤条件也算进键里：调高了相似度门槛或结果条数，
// 上一次的结果就不能直接顶上。
func imageSourceCacheKey(digest string, cfg imageSourceConfig) string {
	parts := []string{
		imageRecognitionKindSource,
		digest,
		fmt.Sprintf("%v", cfg.saucenaoUsable()),
		fmt.Sprintf("%v", cfg.TraceMoeEnabled),
		fmt.Sprintf("%.0f", cfg.MinSimilarity),
		fmt.Sprintf("%d", cfg.MaxResults),
		cfg.SauceNAOURL,
		cfg.TraceMoeURL,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// cachedMatches 复用 OCR 那张识别结果表。出处基本不变，但也不该永远不复查：
// 太旧的记录当作没有，重新查一次。
func (t *dianaImageSourceTool) cachedMatches(ctx context.Context, cacheKey string) ([]ImageSourceMatch, bool) {
	store := t.runtime.imageRecognitionStore()
	if store == nil {
		return nil, false
	}
	record, found, err := store.LoadImageRecognition(ctx, cacheKey)
	if err != nil || !found || strings.TrimSpace(record.Text) == "" {
		return nil, false
	}
	if record.CreatedAt > 0 && time.Since(time.Unix(record.CreatedAt, 0)) > imageSourceCacheTTL {
		return nil, false
	}
	var matches []ImageSourceMatch
	if err := json.Unmarshal([]byte(record.Text), &matches); err != nil || len(matches) == 0 {
		return nil, false
	}
	return matches, true
}

func (t *dianaImageSourceTool) storeMatches(ctx context.Context, cacheKey, digest string, matches []ImageSourceMatch) {
	store := t.runtime.imageRecognitionStore()
	if store == nil {
		return
	}
	payload, err := json.Marshal(matches)
	if err != nil {
		return
	}
	// 写缓存失败不影响这次回答，结果已经拿到了。
	_ = store.SaveImageRecognition(ctx, ImageRecognitionRecord{
		CacheKey:      cacheKey,
		ContentSHA256: digest,
		Kind:          imageRecognitionKindSource,
		Backend:       imageSourcePluginID,
		Text:          string(payload),
		CreatedAt:     time.Now().Unix(),
	})
}

func marshalImageSourceResult(result dianaImageSourceResult) string {
	payload, err := json.Marshal(result)
	if err != nil {
		return `{"ok":false,"message":"图片溯源结果序列化失败"}`
	}
	return string(payload)
}
