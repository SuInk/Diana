// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
)

// 接话评分的上下文以前是整份 proactiveReplyPayload 的 JSON：recent_messages 按从新到旧
// 排，谁回复谁、@ 了谁藏在每条的 addressing 对象里，后面还跟着一整段回复阶段才用得上
// 的工具目录。判断「是不是在跟机器人说话」要的恰恰是时间顺序和指向——线上漏判最多的
// 就是「机器人刚答完某人，那人紧接着追问一句」，这层关系在倒序 JSON 里要跨好几个字段
// 拼才看得出来。
//
// 拿三天线上日志里 159 条人工盲标的难例离线回放：同一个判断模型，换成按时间排的对话
// 之后，gemini-3.8-flash-low 的错判从 15 条降到 11～12 条（漏判 10 → 4～5），jev 从
// 44 条降到 33 条。两次重跑结论一致。
//
// 这里只改接话评分这一路喂给模型的正文；payload 本身照旧算，发言占比、冷却这些程序
// 判断仍然读它。旧契约（should_reply）那一路不动。
//
// 每条消息带着离现在多久。「几个人正快速你来我往时别插嘴」是闲聊分的头一条判据，
// 没有时间，一分钟里的五条来回和一下午的五条闲聊在对话稿里长得一样。
//
// 后来每行又从「发送者（回复谁，@谁）：正文」改成了 <msg> 标签，见 router_transcript_tagged.go。

// burstCandidateNumbers 给当前消息以外的候选按先后编号，从 1 开始；接话评分按这个
// 编号逐条回 burst_k。
func burstCandidateNumbers(payload proactiveReplyPayload) map[string]int {
	numbers := map[string]int{}
	for _, candidate := range payload.Candidates {
		if candidate.IsCurrent || candidate.MessageID == "" || numbers[candidate.MessageID] > 0 {
			continue
		}
		numbers[candidate.MessageID] = len(numbers) + 1
	}
	return numbers
}

// burstRepliedByBot 找出机器人已经回过的连发：之后有机器人的发言引用了它，或 @ 了它的发送者。
func burstRepliedByBot(payload proactiveReplyPayload, burst map[string]int) map[string]bool {
	replied := map[string]bool{}
	if len(burst) == 0 {
		return replied
	}
	senders := map[string]string{}
	for _, candidate := range payload.Candidates {
		if burst[candidate.MessageID] > 0 {
			senders[candidate.MessageID] = strings.TrimSpace(candidate.UserID)
		}
	}
	// RecentMessages 从新到旧：先见到的机器人发言都在后面那几条连发之后。
	var laterBot []proactiveReplyHistoryItem
	for _, item := range payload.RecentMessages {
		if item.IsBot {
			laterBot = append(laterBot, item)
			continue
		}
		if burst[item.MessageID] == 0 {
			continue
		}
		for _, bot := range laterBot {
			if bot.Addressing.ReplyMessageID != "" && bot.Addressing.ReplyMessageID == item.MessageID {
				replied[item.MessageID] = true
			}
			for _, mention := range bot.Addressing.Mentions {
				if sender := senders[item.MessageID]; sender != "" && mention.UserID == sender {
					replied[item.MessageID] = true
				}
			}
		}
	}
	return replied
}

// transcriptAge 把离现在的秒数写成行首的「[25秒前] 」，没有时间就不写。
func transcriptAge(seconds *int64) string {
	if seconds == nil || *seconds < 0 {
		return ""
	}
	switch s := *seconds; {
	case s < 5:
		return "[刚刚] "
	case s < 60:
		return fmt.Sprintf("[%d秒前] ", s)
	case s < 3600:
		return fmt.Sprintf("[%d分钟前] ", s/60)
	default:
		return fmt.Sprintf("[%d小时前] ", s/3600)
	}
}

// lastBotFact 在当前消息没 @、没引用本人时，写明本人最近一次发言在多久以前。
//
// 没有这行时，模型只能自己从对话稿里找本人说过什么，找错了就会编出「本人刚接了他的
// 话」，再把一句没点名的「你」判成在叫本人。本人最近一条正好冲着当前发送者、中间
// 没人插话的那种情况另有一行，这里不写。
func lastBotFact(payload proactiveReplyPayload, botName string) string {
	if payload.Addressing.ReplyTarget == "self" {
		return ""
	}
	for _, mention := range payload.Addressing.Mentions {
		if mention.Target == "self" {
			return ""
		}
	}
	last := payload.LastBotMessage
	if last == nil {
		return "（这段记录里" + botName + "没有发过言）"
	}
	age := strings.Trim(strings.TrimSpace(transcriptAge(last.AgeSeconds)), "[]")
	fact := "（" + botName + "最近一次发言"
	if age != "" {
		fact += "在" + age
	}
	if payload.OthersSpokeAfterLastBot {
		fact += "，之后已有别人说话"
	}
	return fact + "）"
}
