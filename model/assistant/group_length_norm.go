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

// groupLengthNormMinLimit 是每条上限的下限：群友都在刷「6」「草」时，照着写「不超过
// 5 字」连一句完整的话都放不下。
const groupLengthNormMinLimit = 15

// groupLengthNormEnabled 只给离线重放做对照用（见 live_soul_replay_test.go）。
var groupLengthNormEnabled = true

// groupNoNewlineMaxPercent：群友消息里带换行的比例不超过这个值，才说「这个群不换行」。
const groupNoNewlineMaxPercent = 10

const promptGroupNoNewline = "大家的消息几乎不换行（只有 {percent}% 带换行），你也一条消息只写一行，要换行的地方就拆成下一条发。"

var promptGroupNoNewlineSpec = tailSpec("group_length_norm.no_newline", "本群消息不换行", "群友消息里带换行的不超过一成时，接在「本群消息长度」后面：让回复一条只写一行。",
	promptGroupNoNewline,
	PromptVar{Name: "percent", Description: "最近群友消息里带换行的百分比"})

// 上限直接取群友的中位数，而不是九成分位：写多少模型都会超出一截。群里另一个
// 机器人的人设写「不超过 15 字」，实际中位落在 32 字，和群友差不多；写得宽了，
// 超出去的那一截就又是一大段。
const promptGroupLengthNorm = "这个群里大家一条消息一般 {median} 字左右。你每条也尽量不超过 {limit} 字：一句话长了就断开，另起一条接着说，不要塞在同一条里；接一句就停，不先复述问题，不分段讲原理，不在最后总结。代码、命令、报错原文和对方明确要的完整步骤不受这个限制。"

var promptGroupLengthNormSpec = tailSpec("group_length_norm", "本群消息长度", "群聊里最近有足够多的群友消息时注入，紧挨语气锚点：告诉模型这个群的人一条消息一般多长，让回复照着对齐。",
	promptGroupLengthNorm,
	PromptVar{Name: "median", Description: "最近群友消息长度的中位数（字）"},
	PromptVar{Name: "limit", Description: "每条消息的建议上限（字）：群友中位数，至少 15"})

// groupLengthNormPrompt 从最近的群聊里算群友消息的长度，不够样本时返回空串。
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
	text := cfg.promptf(promptGroupLengthNormSpec, map[string]string{
		"median": strconv.Itoa(norm.median),
		"limit":  strconv.Itoa(max(norm.median, groupLengthNormMinLimit)),
	})
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
