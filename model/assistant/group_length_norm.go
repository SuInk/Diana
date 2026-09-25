// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"sort"
	"strconv"
	"strings"
)

// 一条回复该多长，照这个群里真人一条消息多长来定。
//
// 「一两句」「不超过三十字」写在人设里，模型基本不当回事：生产上几个群的群友一条
// 消息中位 10 到 30 字，九成不超过 40 到 70 字，机器人的中位却是 58 到 110 字，
// 一回就是群友的三到九倍。笼统的「简短」它有自己的理解，一个具体的、这个群自己的
// 数字它才会照着对齐。所以每轮从最近的聊天里现算群友消息的中位和九成长度，紧挨着
// 语气锚点告诉它。只算长度，不引用任何人的原话。
//
// 换行同理：群友的消息带换行的只有百分之零到五，机器人是百分之二三十到将近一半，
// 一条消息里分好几行，是一眼就能认出的「这是机器在写」。群友很少换行时，同一段
// 再补一句让它一条只写一行，要换行的地方拆成下一条。

// groupLengthNormMinSamples 是算长度要的最少群友消息数：太少时一两条长消息就能把
// 中位带偏，不如不说。
const groupLengthNormMinSamples = 15

// groupLengthNormEnabled 只给离线重放做对照用（见 live_soul_replay_test.go）。
var groupLengthNormEnabled = true

// groupNoNewlineMaxPercent：群友消息里带换行的比例不超过这个值，才说「这个群不换行」。
const groupNoNewlineMaxPercent = 10

// 腔调同理：历史里本来就有几十条群友原话，但模型更爱模仿自己以前的回复（它们以
// assistant 身份回放，离它最近）。明说学的是群友，不是自己。
const promptGroupVoice = "说话的腔调也学群里的人：他们怎么用词、怎么接梗、怎么打标点、爱用哪些口头禅和表情，你就怎么说；学的是上面群友的消息，不是你自己以前的回复。你是谁、怎么称呼自己仍按最开头的人设。"

var promptGroupVoiceSpec = tailSpec("group_length_norm.voice", "学群友的腔调", "群聊里最近有足够多的群友消息时注入，紧挨语气锚点：让回复的用词、接梗和标点照着群友学，而不是照着机器人自己以前的回复。", promptGroupVoice)

const promptGroupNoNewline = "大家的消息几乎不换行（只有 {percent}% 带换行），你也一条消息只写一行，要换行的地方就拆成下一条发。"

var promptGroupNoNewlineSpec = tailSpec("group_length_norm.no_newline", "本群消息不换行", "群友消息里带换行的不超过一成时，接在「学群友的腔调」后面：让回复一条只写一行。",
	promptGroupNoNewline,
	PromptVar{Name: "percent", Description: "最近群友消息里带换行的百分比"})

// groupLengthNormPrompt 按最近的群聊给出「学群友怎么说话」那一段，不够样本时返回空串。
//
// 以前这里还有一句「你每条也尽量不超过 {群友中位} 字」。长度确实压下来了，口吻却跟着
// 变冲：字数一卡死，模型先删的是缓和语气的词和客气话，剩下「有话快说」这种硬邦邦
// 的短句。篇幅交给人设和上面那句「学群友」，不再给具体字数。
func (r *Runtime) groupLengthNormPrompt(event MessageEvent, cfg BotConfig) string {
	if !groupLengthNormEnabled || event.Kind != EventKindGroup {
		return ""
	}
	r.mu.RLock()
	history := append([]MessageEvent(nil), r.history[sessionKey(event)]...)
	r.mu.RUnlock()
	norm, ok := groupMessageNorm(history, firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(event.SelfID)))
	if !ok {
		return ""
	}
	text := cfg.prompt(promptGroupVoiceSpec)
	if norm.newlinePercent <= groupNoNewlineMaxPercent {
		text += cfg.promptf(promptGroupNoNewlineSpec, map[string]string{"percent": strconv.Itoa(norm.newlinePercent)})
	}
	return text
}

type groupMessageNormStats struct {
	median, p90, newlinePercent int
}

// groupMessageNorm 算群友文字消息的中位和九成分位长度（按 5 字取整），以及带换行的
// 百分比。机器人自己的回复不算——正是它在拉长均值、多出换行；纯图片、纯表情这类
// 没有文字的消息也不算。
func groupMessageNorm(history []MessageEvent, botID string) (groupMessageNormStats, bool) {
	lengths := make([]int, 0, len(history))
	newlines := 0
	for _, event := range history {
		if assistantHistoryEvent(event, botID) || event.crossGroupContext {
			continue
		}
		text := strings.TrimSpace(historyPlainText(event))
		if n := len([]rune(text)); n > 0 {
			lengths = append(lengths, n)
			if strings.Contains(text, "\n") {
				newlines++
			}
		}
	}
	if len(lengths) < groupLengthNormMinSamples {
		return groupMessageNormStats{}, false
	}
	sort.Ints(lengths)
	round := func(n int) int {
		if n < 5 {
			return 5
		}
		return (n + 2) / 5 * 5
	}
	return groupMessageNormStats{
		median:         round(lengths[len(lengths)/2]),
		p90:            round(lengths[len(lengths)*9/10]),
		newlinePercent: newlines * 100 / len(lengths),
	}, true
}
