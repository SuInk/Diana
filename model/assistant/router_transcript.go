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
// 每行开头标着离现在多久。「几个人正快速你来我往时别插嘴」是闲聊分的头一条判据，
// 没有时间，一分钟里的五条来回和一下午的五条闲聊在对话稿里长得一样。

// proactiveReplyTranscript 把评分上下文排成从早到晚的对话，当前消息放最后并单独标出。
func proactiveReplyTranscript(payload proactiveReplyPayload) string {
	botName := "机器人"
	for _, alias := range payload.BotAliases {
		if alias = strings.TrimSpace(alias); alias != "" {
			botName = alias
			break
		}
	}
	lines := make([]string, 0, len(payload.RecentMessages)+4)
	if len(payload.BotAliases) > 0 {
		lines = append(lines, "机器人的称呼："+strings.Join(payload.BotAliases, "、"))
	}
	lines = append(lines, "对话按时间从早到晚：")
	// 防抖、积压合并进来的候选（见 proactive_route_debounce.go）和当前消息挨着发，但话题
	// 可能各不相同。每条原地标上【连发N】、各判各的：整组只给一个结论时，最后那条在跟
	// 谁说话会把前面的一起带偏，回放里漏判就出在这。机器人已经回过的标「已回」。
	burst := burstCandidateNumbers(payload)
	replied := burstRepliedByBot(payload, burst)
	label := func(id string) string {
		mark := fmt.Sprintf("【连发%d】", burst[id])
		if replied[id] {
			mark += "（" + botName + "已回）"
		}
		return mark
	}
	// RecentMessages 是从新到旧攒的，倒着读回来。
	seen := map[string]bool{}
	for i := len(payload.RecentMessages) - 1; i >= 0; i-- {
		item := payload.RecentMessages[i]
		if item.MessageID != "" {
			seen[item.MessageID] = true
		}
		line := transcriptAge(item.AgeSeconds) + transcriptLine(item.Sender, item.IsBot, item.Addressing, item.Text, item.Images, botName)
		if burst[item.MessageID] > 0 && !item.IsBot {
			line = label(item.MessageID) + line
		}
		lines = append(lines, line)
	}
	// 同一批攒进来的其它消息通常到达时就进了历史；没进的补在当前消息前面，它们各自
	// 回复了谁、@ 了谁不能丢。最后一条候选就是当前消息，不在这里重复。
	for i := 0; i+1 < len(payload.Candidates); i++ {
		candidate := payload.Candidates[i]
		if candidate.IsCurrent || (candidate.MessageID != "" && seen[candidate.MessageID]) {
			continue
		}
		line := transcriptAge(candidate.AgeSeconds) + transcriptLine(candidate.Sender, false, candidate.Addressing, candidate.Text, candidate.Images, botName)
		if burst[candidate.MessageID] > 0 {
			line = label(candidate.MessageID) + line
		}
		lines = append(lines, line)
	}
	current := transcriptLine(payload.CurrentSender, false, payload.Addressing, payload.CurrentText, payload.CurrentImages, botName)
	if quoted := strings.TrimSpace(payload.QuotedText); quoted != "" {
		who := strings.TrimSpace(payload.QuotedSender)
		if payload.QuotedIsBot {
			who = botName
		}
		current += fmt.Sprintf("（引用 %s：%s）", firstNonEmpty(who, "某人"), truncateRunes(strings.Join(strings.Fields(quoted), " "), 80))
	}
	lines = append(lines, "【当前消息】[刚刚] "+current)
	if n := len(burst); n > 0 {
		lines = append(lines, fmt.Sprintf("（【连发1】到【连发%d】和当前消息是几秒内接连发出的，合并成一次评分，但话题可能不同，要逐条分开判断：relevance 和 chat_in 只评【当前消息】；每条【连发k】另外输出 burst_k: {\"directed\": true 或 false, \"reason\": \"…\"}，按同样的判据只看这一条自己在跟谁说话，不要被前后几条带偏。标了「%s已回」的已经回过，directed 填 false）", n, botName))
	}
	// 机器人之后有别人插进来时不写这条：评分判据里「紧接着它刚才的话」要求中间没有别人，
	// 这条提示会让模型跳过中间那几句，把「你复现了」这种问别人的话认成在问机器人。
	if payload.LastBotAddressedCurrentSender && !payload.OthersSpokeAfterLastBot {
		lines = append(lines, "（"+botName+"最近一条发言是冲着当前发送者说的）")
	}
	if notebook := strings.TrimSpace(payload.NotebookContext); notebook != "" {
		lines = append(lines, "", "群内术语：", notebook)
	}
	return strings.Join(lines, "\n")
}

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

// transcriptLine 渲染一行：「发送者（回复谁，@谁）：正文」。机器人自己的发言标成
// 「<称呼>（机器人）」，指向机器人的回复和 @ 也写成它的称呼，模型不用再对账号。
func transcriptLine(sender string, isBot bool, addressing messageAddressing, text string, images int, botName string) string {
	name := strings.TrimSpace(sender)
	if isBot {
		name = botName + "（机器人）"
	}
	var targets []string
	switch addressing.ReplyTarget {
	case "self":
		targets = append(targets, "回复"+botName)
	case "other":
		targets = append(targets, "回复"+firstNonEmpty(strings.TrimSpace(addressing.ReplySender), "别人"))
	}
	for _, mention := range addressing.Mentions {
		if mention.Target == "self" {
			targets = append(targets, "@"+botName)
			continue
		}
		targets = append(targets, "@"+firstNonEmpty(strings.TrimSpace(mention.Username), "别人"))
	}
	if len(targets) > 0 {
		name += "（" + strings.Join(targets, "，") + "）"
	}
	body := strings.Join(strings.Fields(text), " ")
	if images > 0 {
		body = strings.TrimSpace(body + fmt.Sprintf(" [图片×%d]", images))
	}
	return name + "：" + body
}
