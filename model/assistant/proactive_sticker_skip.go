// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"time"
)

// 群友之间发的纯表情包，接话评分几乎从不放行：生产一周约 2600 次评分落在这类消息上，
// 闲聊分全是 0.05–0.15，而除「always」外最低的闲聊线也要 0.30。每次评分约 3k token，
// 这里不调模型直接沉默。
//
// 例外是机器人刚说过话：群友拿表情包回应它（「承接机器人上一句」），评分会判成在跟
// 机器人说话并放行，一周约 60 次；还有人刚 @ 过机器人、回复还没出来就补一个表情。
// 所以最近一段时间里机器人在本会话发过言、或有人找过它，就照常评分。按这个条件回查
// 生产一周里得到回复的 38 条纯表情包，一条都不会被跳过。
// @、引用和点名本身就不算「没在找机器人」，也照常评分。

// stickerSkipBotQuietWindow 是机器人多久没在本会话说话，纯表情包才直接跳过评分。
const stickerSkipBotQuietWindow = 10 * time.Minute

// stickerOnlyUnaddressed 判断消息是不是只有表情包、没有文字、引用或 @。
func stickerOnlyUnaddressed(event MessageEvent) bool {
	if event.ToMe || event.Quoted != nil || len(event.MentionTargets) > 0 {
		return false
	}
	stickers := 0
	for _, segment := range event.Segments {
		switch segment.Type {
		case "face", "mface":
			stickers++
		case "image":
			if _, ok := StickerSegmentLabel(segment); !ok {
				return false
			}
			stickers++
		case "text":
			if strings.TrimSpace(segment.Data["text"]) != "" {
				return false
			}
		default:
			return false
		}
	}
	return stickers > 0
}

// skipStickerOnlyRouting 在整批候选都是没人招呼的纯表情包、机器人最近也没在本会话
// 说话时返回 true。闲聊档位是 always 时什么都放行，不跳过。
func (r *Runtime) skipStickerOnlyRouting(candidates []proactiveReplyCandidate, cfg BotConfig, chatLevel string) bool {
	if len(candidates) == 0 || chatLevel == "always" {
		return false
	}
	for _, candidate := range candidates {
		if !stickerOnlyUnaddressed(candidate.Event) {
			return false
		}
	}
	latest := candidates[len(candidates)-1].Event
	botID := firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(latest.SelfID))
	now := time.Now()
	if latest.Time > 0 {
		now = time.Unix(latest.Time, 0)
	}
	history := r.contextHistory(latest)
	for index := len(history) - 1; index >= 0; index-- {
		item := history[index]
		if item.Time > 0 && now.Sub(time.Unix(item.Time, 0)) > stickerSkipBotQuietWindow {
			break
		}
		if assistantHistoryEvent(item, botID) || item.ToMe {
			return false
		}
	}
	return true
}
