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
	// durableMediaIndexCandidateLimit 是并行收集时的候选上限：渲染时还要剔掉仍在历史
	// 窗口里的，所以多收一些。
	durableMediaIndexCandidateLimit = 96
	// durableMediaIndexDescriptionRunes 是每条媒体描述在索引里的长度上限。
	durableMediaIndexDescriptionRunes = 80
)

// durableMediaIndexEntry 是索引里的一条：渲染好的行，加上它来自哪条消息。
type durableMediaIndexEntry struct {
	event MessageEvent
	line  string
}

// durableMediaIndex 收集最近带媒体的消息，供 renderDurableMediaIndex 挑出已经掉出历史
// 窗口、模型看不到的那些。
//
// 它取代了 agent 模式下的前置指代路由：模型看得到「有哪些图、分别是什么」之后，
// 需要媒体原件时自己调 history_media 传 message_id 即可。索引只带描述不带
// bytes，所以每条只有几十 token；而路由器是一次完整的 LLM 调用，且那道门只看
// 「有没有旧媒体」不看「这句话像不像指代」，在发过图的群里对每条消息都会触发。
//
// 描述来自后台识图的缓存，取不到就留空——不编造，也不为此阻塞回复。
//
// 这里和读描述的回复准备并行跑，那时还不知道这一轮实际渲染进提示词的历史窗口，所以多收
// 一些候选（窗口内的会在渲染时剔掉）。
func (r *Runtime) durableMediaIndex(ctx context.Context, event MessageEvent) []durableMediaIndexEntry {
	history, _ := r.semanticReferenceHistory(ctx, event)
	entries := make([]durableMediaIndexEntry, 0, durableMediaIndexCandidateLimit)
	seen := make(map[string]bool, durableMediaIndexCandidateLimit)
	// 由新到旧：越近的媒体越可能被指代，条数被截断时先保住它们。
	for index := len(history) - 1; index >= 0 && len(entries) < durableMediaIndexCandidateLimit; index-- {
		item := history[index]
		messageID := strings.TrimSpace(item.MessageID)
		if messageID == "" || messageID == event.MessageID || seen[messageID] {
			continue
		}
		if !segmentsHaveReferenceContent(item.Segments) && !quotedMessageHasReferenceContent(item.Quoted) {
			continue
		}
		seen[messageID] = true
		if line := durableMediaIndexLine(ctx, r, item); line != "" {
			entries = append(entries, durableMediaIndexEntry{event: item, line: line})
		}
	}
	return entries
}

// renderDurableMediaIndex 只留下不在本轮历史窗口里的媒体（窗口里的模型本来就看得到），
// 最多 durableMediaIndexLimit 条，按时间正序。这样它只在窗口往前滑时才变，能放在历史
// 之前、跟着前缀缓存走。每条消息都在身份隐私代理里登记，message_id 和其他标识一样换成
// 别名——以前这些行没登记过，有一部分真实消息 ID 原样发给了模型。
func renderDurableMediaIndex(ctx context.Context, entries []durableMediaIndexEntry, window []MessageEvent) string {
	inWindow := make(map[string]bool, len(window))
	for _, item := range window {
		if id := strings.TrimSpace(item.MessageID); id != "" {
			inWindow[id] = true
		}
	}
	scope := identityPrivacyScopeFromContext(ctx)
	lines := make([]string, 0, durableMediaIndexLimit)
	for _, entry := range entries {
		if len(lines) == durableMediaIndexLimit {
			break
		}
		if inWindow[strings.TrimSpace(entry.event.MessageID)] {
			continue
		}
		if scope != nil {
			scope.registerEvent(entry.event)
		}
		lines = append(lines, entry.line)
	}
	if len(lines) == 0 {
		return ""
	}
	// 索引本身按时间正序读起来更自然，收集时是倒序，这里翻回来。
	for left, right := 0, len(lines)-1; left < right; left, right = left+1, right-1 {
		lines[left], lines[right] = lines[right], lines[left]
	}
	return "【更早的图片与文件索引，已不在下面的聊天记录里】\n" +
		"需要核对历史媒体时调用 history_media 并传入对应 message_id；" +
		"下面的描述是机器识别的概要，可能有误，不要当成原文逐字引用。\n" +
		strings.Join(lines, "\n")
}

func durableMediaIndexLine(ctx context.Context, runtime *Runtime, item MessageEvent) string {
	var builder strings.Builder
	builder.WriteString("- message_id=")
	builder.WriteString(strings.TrimSpace(item.MessageID))
	builder.WriteString(contextMessageTiming(item.Time, profileLocation(item.ProfileID)))
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
