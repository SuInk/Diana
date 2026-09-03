// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strings"
)

// 转发卡片过账号安全审核。
//
// 出站原本有两条路，审核只挂在其中一条上：模型自己写的回复走
// auditReplyAccountSafety，而转发卡片走 sendForwardNodesWithResult——那条路把封群、
// 回复抑制、打断这几道守卫都各自复制了一遍，唯独没有内容审核。
//
// 卡片里装的恰恰是最该看一眼的东西：链接解析把站外正文、作者昵称原样搬进来，
// 一个字都没经过模型。审核函数收的又是「模型写的那段话」，转发节点在它眼里
// 根本不存在，于是站外内容一路直达群里。
//
// 这里不跟 ReplyAccountSafetyAuditEnabled 那个开关走。那个开关默认关掉的理由是
// 成本——直接回复每条都多一次模型往返。转发卡片是有人贴链接才有的，频次低、
// 内容又是外面写的，那笔成本账在这里不成立。

// forwardNodeAuditText 把转发节点里会显示给人看的文字抽出来。
//
// 作者昵称也要审：截图里出事的就是这一处，正文尚可，发帖人昵称本身带着违禁词。
func forwardNodeAuditText(nodes []map[string]any) string {
	var builder strings.Builder
	appendLine := func(text string) {
		if text = strings.TrimSpace(text); text == "" {
			return
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(text)
	}
	for _, node := range nodes {
		data, _ := node["data"].(map[string]any)
		if data == nil {
			continue
		}
		if name, ok := data["name"].(string); ok {
			appendLine(name)
		}
		appendLine(forwardNodeContentText(data["content"]))
	}
	return builder.String()
}

// forwardNodeContentText 走一遍节点内容里的文字段。content 是 OneBot 消息段数组，
// 图片、视频这些没有文字的段跳过。
func forwardNodeContentText(content any) string {
	segments, ok := content.([]map[string]any)
	if !ok {
		if generic, isSlice := content.([]any); isSlice {
			segments = make([]map[string]any, 0, len(generic))
			for _, item := range generic {
				if segment, isMap := item.(map[string]any); isMap {
					segments = append(segments, segment)
				}
			}
		}
	}
	var parts []string
	for _, segment := range segments {
		for _, key := range []string{"text", "content", "summary", "title", "desc"} {
			// 段的 data 就地构造时是 map[string]string（buildOutgoingSegments），
			// 从 JSON 回来时是 map[string]any。两种都要认，只认一种会静默漏掉整段。
			if text := segmentDataString(segment["data"], key); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// segmentDataString 从消息段的 data 里取一个字符串字段，兼容两种 map 类型。
func segmentDataString(data any, key string) string {
	switch typed := data.(type) {
	case map[string]string:
		return strings.TrimSpace(typed[key])
	case map[string]any:
		if text, ok := typed[key].(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

// auditForwardNodesSafety 在发出转发卡片前审一遍卡片里的可见文字。
//
// 审核失败（超时、报错、返回读不出来）时放行，和 auditReplyAccountSafety 的既有
// 取舍一致：审核挂掉不该让机器人整个哑掉。返回的错误只有「审出来不该发」这一种。
func (r *Runtime) auditForwardNodesSafety(ctx context.Context, event MessageEvent, nodes []map[string]any) error {
	if r == nil || len(nodes) == 0 {
		return nil
	}
	text := forwardNodeAuditText(nodes)
	if text == "" {
		// 纯图片、纯视频或按 message_id 引用的节点没有文字可审，照旧发出。
		return nil
	}
	cfg := r.effectiveConfigForEvent(event)
	ctx = withLLMUsagePurpose(ctx, "forward_content_safety")
	decision, err := r.runReplyAudit(ctx, event, readableEventText(event, event.RawMessage), text, cfg)
	if err != nil {
		log.Printf("diana forward content safety audit skipped: %v", err)
		return nil
	}
	return accountSafetyError(decision)
}
