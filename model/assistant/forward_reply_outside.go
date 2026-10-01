// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// 合并转发卡片只装正文，开场和收尾的那句话留在卡片外面。
//
// 回复一过阈值就整条进卡片，「给你整理好了」「有问题再问我」也跟着进去：群里只看到
// 一张卡片，像是甩了份文档过来。真人的做法是先说一句、再丢资料、最后补一句。卡片里
// 的 @ 不提醒人、也挂不上引用，开场那句放在外面才点得到名。
//
// 哪几条算「那句话」由发送层判断，不让模型写标记：标记要改提示词，模型对新标记的遵从
// 一向靠不住，而首尾这两条在形状上很好认——单行、短，紧挨着的那条是一整块正文。只看
// 首尾各一条：中间夹的话分不清是闲话还是资料，宁可留在卡片里。

// forwardOutsideBubbleMaxRunes 是留在卡片外那条的长度上限，按原文计（含 @ 标记）。
const forwardOutsideBubbleMaxRunes = 50

// forwardBlockBubbleMinRunes：紧挨着的那条单行时，至少这么长才算一整块正文。
const forwardBlockBubbleMinRunes = 100

// 清单项、标题、引用块：形状短，但属于正文。
var forwardListItemPattern = regexp.MustCompile(`^(\d+[.、)）]|[（(]\d+[)）]|[-*•·#>]|[一二三四五六七八九十]+[、.])`)

// forwardTalkBubble 判断一条是不是可以放到卡片外的那句话。
func forwardTalkBubble(chunk string) bool {
	chunk = strings.TrimSpace(chunk)
	if chunk == "" || strings.ContainsAny(chunk, "\n\r") || len([]rune(chunk)) > forwardOutsideBubbleMaxRunes {
		return false
	}
	if strings.Contains(chunk, "```") || strings.Contains(chunk, "[CQ:") {
		return false
	}
	return !forwardListItemPattern.MatchString(chunk)
}

// forwardBlockBubble 判断一条是不是成块的正文。首尾那句只有挨着正文块时才拆出去：
// 一串短气泡凑够条数进的卡片，首尾那条和中间的是同一种东西，拆出去就把清单拆散了。
func forwardBlockBubble(chunk string) bool {
	chunk = strings.TrimSpace(chunk)
	return strings.Contains(chunk, "\n") || len([]rune(chunk)) >= forwardBlockBubbleMinRunes
}

// splitForwardReplyOutside 把要进卡片的回复切成：卡片前一条、卡片、卡片后一条。
// 拆完剩下的正文要是不够阈值，就不拆那一头——卡片得是因为正文长才存在的。
func splitForwardReplyOutside(reply string, event MessageEvent, cfg BotConfig) (lead string, card []string, tail string) {
	reply, event = prepareReplyDelivery(reply, event)
	control, rest, hasControl := consumeOutgoingReplyControl(reply)
	if hasControl {
		reply = rest
	}
	card = splitForwardReply(reply, chatSplitLimitsForEvent(cfg, event))
	stillForward := func(chunks []string) bool {
		return len(chunks) > 0 && shouldUseForwardReplyFor(cfg, strings.Join(chunks, "\n"), chunks)
	}
	if len(card) >= 2 && forwardTalkBubble(card[0]) && forwardBlockBubble(card[1]) && stillForward(card[1:]) {
		lead, card = card[0], card[1:]
	}
	if last := len(card) - 1; last >= 1 && forwardTalkBubble(card[last]) && forwardBlockBubble(card[last-1]) && stillForward(card[:last]) {
		tail, card = card[last], card[:last]
	}
	// 引用标记跟着开场那句走：节点里挂不上引用，外面那条挂得上。
	if lead != "" && hasControl && control != "" {
		lead = replyMarkerPrefix + control + "]" + lead
	}
	return lead, card, tail
}

// sendForwardReplyDecorated 发一条要走合并转发的回复：开场一句、卡片、收尾一句，
// 按原文顺序。引用和 @ 挂在开场那句上；没有开场时和原来一样，卡片不挂。
func (r *Runtime) sendForwardReplyDecorated(ctx context.Context, event MessageEvent, reply string, chunks []string, cfg BotConfig, decoration outboundDecoration) ([]string, error) {
	lead, card, tail := splitForwardReplyOutside(reply, event, cfg)
	var messageIDs []string
	if lead != "" {
		sent, err := r.deliverChunks(ctx, event, []string{lead}, cfg, decoration)
		if err != nil {
			return nil, err
		}
		messageIDs = append(messageIDs, sent...)
		decoration = outboundDecoration{}
	}
	messageID, err := r.sendForwardChunksWithResult(ctx, event, card, reply, cfg)
	if err != nil {
		if ctx.Err() != nil {
			return messageIDs, ctx.Err()
		}
		// 卡片被账号安全审核拦下时不能退回逐条发送：逐条发的是同一段文字，那条路
		// 不再审核。卡片只在回复链路没审过这段话时才审，这时它是唯一一次审核。
		var safetyErr *replyAccountSafetyRejectedError
		if errors.As(err, &safetyErr) {
			return messageIDs, err
		}
		// Some OneBot implementations do not support merged forwards. Continue
		// through the normal chunk path so long replies are still delivered.
		rest := chunks
		if lead != "" || tail != "" {
			// 开场已经发出去了，剩下的按卡片里的分条补发，不再发第二遍开场。
			rest = card
			if tail != "" {
				rest = append(append([]string(nil), card...), tail)
			}
		}
		sent, err := r.deliverChunks(ctx, event, rest, cfg, decoration)
		return append(messageIDs, sent...), err
	}
	if messageID != "" {
		messageIDs = append(messageIDs, messageID)
	}
	if tail != "" {
		sent, err := r.deliverChunks(ctx, event, []string{tail}, cfg, outboundDecoration{})
		if err != nil {
			return messageIDs, err
		}
		messageIDs = append(messageIDs, sent...)
	}
	return messageIDs, nil
}
