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
	// RecentMessages 是从新到旧攒的，倒着读回来。
	seen := map[string]bool{}
	for i := len(payload.RecentMessages) - 1; i >= 0; i-- {
		item := payload.RecentMessages[i]
		if item.MessageID != "" {
			seen[item.MessageID] = true
		}
		lines = append(lines, transcriptLine(item.Sender, item.IsBot, item.Addressing, item.Text, item.Images, botName))
	}
	// 同一批攒进来的其它消息通常到达时就进了历史；没进的补在当前消息前面，它们各自
	// 回复了谁、@ 了谁不能丢。最后一条候选就是当前消息，不在这里重复。
	for i := 0; i+1 < len(payload.Candidates); i++ {
		candidate := payload.Candidates[i]
		if candidate.IsCurrent || (candidate.MessageID != "" && seen[candidate.MessageID]) {
			continue
		}
		lines = append(lines, transcriptLine(candidate.Sender, false, candidate.Addressing, candidate.Text, candidate.Images, botName))
	}
	current := transcriptLine(payload.CurrentSender, false, payload.Addressing, payload.CurrentText, payload.CurrentImages, botName)
	if quoted := strings.TrimSpace(payload.QuotedText); quoted != "" {
		who := strings.TrimSpace(payload.QuotedSender)
		if payload.QuotedIsBot {
			who = botName
		}
		current += fmt.Sprintf("（引用 %s：%s）", firstNonEmpty(who, "某人"), truncateRunes(strings.Join(strings.Fields(quoted), " "), 80))
	}
	lines = append(lines, "【当前消息】"+current)
	if payload.LastBotAddressedCurrentSender {
		lines = append(lines, "（"+botName+"最近一条发言是冲着当前发送者说的）")
	}
	if notebook := strings.TrimSpace(payload.NotebookContext); notebook != "" {
		lines = append(lines, "", "群内术语：", notebook)
	}
	return strings.Join(lines, "\n")
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
