// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// imageEditSourceMemoryTTL 是「重试」「继续」还能找回上一次原图的时间窗。
	// 超过这个时间，用户多半已经在聊别的，再拿旧图去改反而会改错。
	imageEditSourceMemoryTTL = 30 * time.Minute
	// imageEditSourceMemoryLimit 限制记住的会话数，超出时丢最旧的。
	imageEditSourceMemoryLimit = 256
	// imageEditQuoteChainDepth 是顺着「引用的引用」往上找原图的最大层数。
	imageEditQuoteChainDepth = 4
)

// imageEditSourceMemory 按会话记住最近一次改图任务用的原图。
//
// 改图失败后，用户通常引用机器人那条失败通知或「在画了」回一句「重试」「继续」。
// 被引用的是纯文字，原图在更早的消息里，最近聊天记录又早被群聊刷过去了，
// 这时唯一可靠的线索就是上一次任务自己用过的原图。
type imageEditSourceMemory struct {
	mu      sync.Mutex
	entries map[string]imageEditSourceEntry
}

type imageEditSourceEntry struct {
	sources []string
	at      time.Time
}

func (m *imageEditSourceMemory) remember(session string, sources []string, now time.Time) {
	session = strings.TrimSpace(session)
	if session == "" || len(sources) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[string]imageEditSourceEntry{}
	}
	m.entries[session] = imageEditSourceEntry{sources: append([]string(nil), sources...), at: now}
	for len(m.entries) > imageEditSourceMemoryLimit {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range m.entries {
			if oldestKey == "" || entry.at.Before(oldest) {
				oldestKey, oldest = key, entry.at
			}
		}
		delete(m.entries, oldestKey)
	}
}

func (m *imageEditSourceMemory) recall(session string, now time.Time) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[strings.TrimSpace(session)]
	if !ok {
		return nil
	}
	if now.Sub(entry.at) > imageEditSourceMemoryTTL {
		delete(m.entries, strings.TrimSpace(session))
		return nil
	}
	return append([]string(nil), entry.sources...)
}

// quotedBotTextWithoutImage 判断用户引用的是不是机器人自己的一条不带图的消息，
// 典型是改图失败通知或「在画了」。
func (r *Runtime) quotedBotTextWithoutImage(event MessageEvent) bool {
	if event.Quoted == nil || len(availableImageURLs(event.Quoted.Segments)) > 0 {
		return false
	}
	quotedUser := strings.TrimSpace(event.Quoted.UserID)
	if quotedUser == "" {
		return false
	}
	botID := firstNonEmpty(strings.TrimSpace(r.effectiveConfigForEvent(event).BotAccount), strings.TrimSpace(event.SelfID))
	return quotedUser == botID
}

// quotedChainImageURLs 顺着引用消息自己的引用往上找图。
//
// 机器人回复改图请求时会引用用户那条消息，用户那条又引用了原图；用户再引用机器人
// 的回复说「继续」，原图就在两三层之外。只看一层引用永远够不着。
func (r *Runtime) quotedChainImageURLs(ctx context.Context, event MessageEvent) []string {
	if event.Quoted == nil {
		return nil
	}
	seen := map[string]bool{strings.TrimSpace(event.MessageID): true, strings.TrimSpace(event.Quoted.MessageID): true}
	pending := replyReferenceIDs(event.Quoted.Segments)
	for depth := 0; depth < imageEditQuoteChainDepth && len(pending) > 0; depth++ {
		id := strings.TrimSpace(pending[0])
		if id == "" || seen[id] {
			return nil
		}
		seen[id] = true
		source, ok := r.findSemanticReferenceEvent(ctx, event, id)
		if !ok {
			return nil
		}
		prepared := r.prepareHistoricalEventImages(ctx, source)
		if historicalImageStateChanged(source, prepared) {
			r.updateHistoricalImageState(prepared)
		}
		images := appendImageEditSourceImages(nil, availableImageURLs(prepared.Segments)...)
		if prepared.Quoted != nil {
			images = appendImageEditSourceImages(images, availableImageURLs(prepared.Quoted.Segments)...)
		}
		if len(images) > 0 {
			return images
		}
		pending = replyReferenceIDs(prepared.Segments)
		if len(pending) == 0 && prepared.Quoted != nil {
			pending = []string{prepared.Quoted.MessageID}
		}
	}
	return nil
}

// maxImageEditSourceMessages 限制一次改图能指认的消息条数。
const maxImageEditSourceMessages = 8

// imageEditSourcesFromMessages 取出模型指认的那几条消息里的全部图片，按指认顺序排。
//
// 指认的消息里没有图时直接报错，而不是悄悄退回自动猜：模型认错了消息，
// 让它当场改正，比拿别的图改出一张对不上的结果强。
func (r *Runtime) imageEditSourcesFromMessages(ctx context.Context, event MessageEvent, messageIDs []string) ([]string, error) {
	out, _, err := r.imageEditSourcesFromMessagesDetailed(ctx, event, messageIDs)
	return out, err
}

// imageEditSourcesFromMessagesDetailed 另外交出每条消息的来历（谁发的、几张图）。
func (r *Runtime) imageEditSourcesFromMessagesDetailed(ctx context.Context, event MessageEvent, messageIDs []string) ([]string, []imageEditSourceUsed, error) {
	var out []string
	var used []imageEditSourceUsed
	var missing, imageless []string
	for _, messageID := range messageIDs {
		messageID = strings.TrimSpace(messageID)
		if messageID == "" {
			continue
		}
		source, ok := r.findSemanticReferenceEvent(ctx, event, messageID)
		if !ok && event.Quoted != nil && strings.TrimSpace(event.Quoted.MessageID) == messageID {
			source = MessageEvent{
				Platform: event.Platform, ProfileID: event.ProfileID, ContextNamespace: event.ContextNamespace,
				Kind: event.Kind, GroupID: firstNonEmpty(event.Quoted.GroupID, event.GroupID),
				UserID: firstNonEmpty(event.Quoted.UserID, event.UserID), MessageID: messageID,
				Segments: event.Quoted.Segments,
			}
			ok = true
		}
		if !ok {
			missing = append(missing, messageID)
			continue
		}
		prepared := r.prepareHistoricalEventImages(ctx, source)
		if historicalImageStateChanged(source, prepared) {
			r.updateHistoricalImageState(prepared)
		}
		images := availableImageURLs(prepared.Segments)
		if len(images) == 0 && prepared.Quoted != nil {
			images = availableImageURLs(prepared.Quoted.Segments)
		}
		if len(images) == 0 {
			imageless = append(imageless, messageID)
			continue
		}
		before := len(out)
		out = appendImageEditSourceImages(out, images...)
		used = append(used, imageEditSourceUsed{
			Kind: imageSourceKindMessage, MessageID: messageID,
			UserID: strings.TrimSpace(source.UserID), User: strings.TrimSpace(source.SenderName),
			Images: len(out) - before,
		})
	}
	var problems []string
	if len(missing) > 0 {
		problems = append(problems, "当前会话里找不到 message_id="+strings.Join(missing, "、"))
	}
	if len(imageless) > 0 {
		problems = append(problems, "message_id="+strings.Join(imageless, "、")+" 里没有可用的图片")
	}
	if len(problems) > 0 {
		return nil, nil, fmt.Errorf("source_message_ids 有误：%s。这次没有开始画；请核对聊天记录里带图消息的 message_id 后重新调用，确实找不到就请用户重新发送或引用那张图", strings.Join(problems, "；"))
	}
	if len(out) == 0 {
		return nil, nil, errImageEditSourceNotFound
	}
	return out, used, nil
}
