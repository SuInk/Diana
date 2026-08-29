// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
)

const (
	// durableMediaIndexLimit 限制索引条数。它是每轮都注入的静态文本，条数上去了
	// 就从「便宜的替代方案」变成新的固定开销。
	durableMediaIndexLimit = 24
	// durableMediaIndexDescriptionRunes 是每条媒体描述在索引里的长度上限。
	durableMediaIndexDescriptionRunes = 80
)

// durableMediaIndex 列出已经掉出历史窗口、模型看不到的媒体消息。
//
// 它取代了 agent 模式下的前置指代路由：模型看得到「有哪些图、分别是什么」之后，
// 需要媒体原件时自己调 diana.history_media 传 message_id 即可。索引只带描述不带
// bytes，所以每条只有几十 token；而路由器是一次完整的 LLM 调用，且那道门只看
// 「有没有旧媒体」不看「这句话像不像指代」，在发过图的群里对每条消息都会触发。
//
// 描述来自后台识图的缓存，取不到就留空——不编造，也不为此阻塞回复。
func (r *Runtime) durableMediaIndex(ctx context.Context, event MessageEvent) string {
	recent := r.contextHistory(event)
	recentIDs := make(map[string]bool, len(recent))
	for _, item := range recent {
		if id := strings.TrimSpace(item.MessageID); id != "" {
			recentIDs[id] = true
		}
	}
	history, _ := r.semanticReferenceHistory(ctx, event)

	lines := make([]string, 0, durableMediaIndexLimit)
	seen := make(map[string]bool, durableMediaIndexLimit)
	// 由新到旧：越近的媒体越可能被指代，条数被截断时先保住它们。
	for index := len(history) - 1; index >= 0 && len(lines) < durableMediaIndexLimit; index-- {
		item := history[index]
		messageID := strings.TrimSpace(item.MessageID)
		if messageID == "" || messageID == event.MessageID || seen[messageID] || recentIDs[messageID] {
			continue
		}
		if !segmentsHaveReferenceContent(item.Segments) && !quotedMessageHasReferenceContent(item.Quoted) {
			continue
		}
		seen[messageID] = true
		if line := durableMediaIndexLine(ctx, r, item, event.Time); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	// 索引本身按时间正序读起来更自然，收集时是倒序，这里翻回来。
	for left, right := 0, len(lines)-1; left < right; left, right = left+1, right-1 {
		lines[left], lines[right] = lines[right], lines[left]
	}
	return "【更早的图片与文件索引，已不在上面的聊天记录里】\n" +
		"需要核对历史媒体时调用 diana.history_media 并传入对应 message_id；" +
		"下面的描述是机器识别的概要，可能有误，不要当成原文逐字引用。\n" +
		strings.Join(lines, "\n")
}

func durableMediaIndexLine(ctx context.Context, runtime *Runtime, item MessageEvent, currentTime int64) string {
	var builder strings.Builder
	builder.WriteString("- message_id=")
	builder.WriteString(strings.TrimSpace(item.MessageID))
	builder.WriteString(contextMessageTiming(item.Time, currentTime))
	if sender := strings.TrimSpace(item.SenderNameOrID()); sender != "" {
		builder.WriteString(" ")
		builder.WriteString(sender)
	}
	if kinds := durableMediaKinds(item); kinds != "" {
		builder.WriteString(" · ")
		builder.WriteString(kinds)
	}
	if text := strings.TrimSpace(rawMessageWithoutImagePlaceholders(historyPlainText(item))); text != "" {
		builder.WriteString(" · 附言：")
		builder.WriteString(truncateRunes(strings.Join(strings.Fields(text), " "), durableMediaIndexDescriptionRunes))
	}
	for _, description := range runtime.historyImageCachedDescriptions(ctx, item) {
		description = strings.TrimSpace(description)
		if description == "" {
			continue
		}
		builder.WriteString("\n    ")
		builder.WriteString(truncateRunes(compactRecallImageDescription(description), durableMediaIndexDescriptionRunes))
	}
	return builder.String()
}

// durableMediaKinds 概括一条消息里的媒体构成，让模型不用取原图就能排除明显不
// 相关的候选。
func durableMediaKinds(item MessageEvent) string {
	images, videos, audio, files := 0, 0, 0, 0
	count := func(segments []MessageSegment) {
		for _, segment := range segments {
			switch segment.Type {
			case "image":
				if segment.Data["source_type"] != "video_frame" {
					images++
				}
			case "video":
				videos++
			case "record":
				audio++
			case "file":
				if videoFileSegment(segment) {
					videos++
				} else {
					files++
				}
			}
		}
	}
	count(item.Segments)
	if item.Quoted != nil {
		count(item.Quoted.Segments)
	}
	parts := make([]string, 0, 4)
	for _, entry := range []struct {
		label string
		total int
	}{{"图片", images}, {"视频", videos}, {"语音", audio}, {"文件", files}} {
		if entry.total > 0 {
			parts = append(parts, fmt.Sprintf("%s×%d", entry.label, entry.total))
		}
	}
	return strings.Join(parts, " ")
}
