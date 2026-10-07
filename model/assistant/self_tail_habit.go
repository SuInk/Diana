// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 句尾口癖是自己喂出来的。
//
// 生产上 2026-10-05 到 10-08 的 873 段回复里，约三成以括号尾巴收尾：「（逃」107 段、
// 半个「（」116 段，再加「（目移」「（探头」；以「w」收尾的又有 67 段。「（逃」不在
// 任何提示词里，是历史里自己以前的回复以 assistant 身份回放、被一轮轮照着学出来的。
// SOUL.md 里「别每句都挂同一个」这种笼统提醒压不住它；真人不会条条消息挂同一个尾巴。
//
// 所以每轮从最近的历史里数一数自己的回复用什么收尾，某一类已经成了口癖，就在尾部
// 点名这几个尾巴，让这一条别再用。只看机器人自己的消息，群友怎么收尾不管。

// selfTailHabitWindow 是往回看的自己的消息条数。
const selfTailHabitWindow = 12

// selfTailHabitMinSamples：自己的消息少于这个数时不下结论。
const selfTailHabitMinSamples = 5

// selfTailHabitMinCount 和 selfTailHabitMinPercent 同时满足才算口癖：
// 偶尔一两次是调味，三成以上是机械收尾。
const (
	selfTailHabitMinCount   = 3
	selfTailHabitMinPercent = 30
)

const promptSelfTailHabit = "你最近 {total} 条消息里有 {count} 条用 {tails} 收尾，已经成了口癖：真人不会条条消息挂同一个尾巴。这次别用这些收尾，话说完就停在最后一个字上。"

var promptSelfTailHabitSpec = tailSpec("self_tail_habit", "句尾口癖提醒", "最近自己的回复里同一类句尾（括号尾巴、w、喵）用得太多时注入，紧挨语气锚点：点名这些尾巴，让这一条不再用。",
	promptSelfTailHabit,
	PromptVar{Name: "total", Description: "参与统计的自己的消息条数"},
	PromptVar{Name: "count", Description: "其中用这类尾巴收尾的条数"},
	PromptVar{Name: "tails", Description: "具体的尾巴，如「（逃」「（」"})

var (
	// 括号尾巴：半个「（」，或者「（逃」「（目移）」这种不超过八个字的短括号。
	selfTailParenPattern = regexp.MustCompile(`[（(][^（()）\n]{0,8}[）)]?$`)
	selfTailWPattern     = regexp.MustCompile(`(?:\p{Han}|[！？～!?~])w+$`)
	selfTailMeowPattern  = regexp.MustCompile(`喵[～~！!？?…]*$`)
)

// selfTailKind 认出一条消息的句尾属于哪一类，返回类别和具体写法。
func selfTailKind(text string) (kind, tail string) {
	text = strings.TrimSpace(text)
	if m := selfTailParenPattern.FindString(text); m != "" {
		// 闭合括号不算进写法，「（逃）」和「（逃」是同一个尾巴。
		return "paren", strings.TrimRight(strings.Replace(m, "(", "（", 1), ")）")
	}
	if m := selfTailWPattern.FindString(text); m != "" {
		return "w", "w"
	}
	if selfTailMeowPattern.MatchString(text) {
		return "meow", "喵"
	}
	return "", ""
}

// selfTailHabit 统计最近自己的消息的句尾，返回成了口癖的那一类。
func selfTailHabit(history []MessageEvent, botID string) (total, count int, tails []string, ok bool) {
	var texts []string
	for i := len(history) - 1; i >= 0 && len(texts) < selfTailHabitWindow; i-- {
		event := history[i]
		if !assistantHistoryEvent(event, botID) || event.crossGroupContext {
			continue
		}
		if text := strings.TrimSpace(historyPlainText(event)); text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) < selfTailHabitMinSamples {
		return 0, 0, nil, false
	}
	counts := map[string]int{}
	written := map[string]map[string]int{}
	for _, text := range texts {
		kind, tail := selfTailKind(text)
		if kind == "" {
			continue
		}
		counts[kind]++
		if written[kind] == nil {
			written[kind] = map[string]int{}
		}
		written[kind][tail]++
	}
	best := ""
	for _, kind := range []string{"paren", "w", "meow"} {
		if counts[kind] > counts[best] {
			best = kind
		}
	}
	if best == "" || counts[best] < selfTailHabitMinCount || counts[best]*100 < selfTailHabitMinPercent*len(texts) {
		return 0, 0, nil, false
	}
	for tail := range written[best] {
		tails = append(tails, tail)
	}
	sort.Slice(tails, func(i, j int) bool {
		if written[best][tails[i]] != written[best][tails[j]] {
			return written[best][tails[i]] > written[best][tails[j]]
		}
		return tails[i] < tails[j]
	})
	return len(texts), counts[best], tails, true
}

// selfTailHabitPrompt 在自己的句尾成了口癖时给出提醒，否则返回空串。
func (r *Runtime) selfTailHabitPrompt(event MessageEvent, cfg BotConfig) string {
	r.mu.RLock()
	history := append([]MessageEvent(nil), r.history[sessionKey(event)]...)
	r.mu.RUnlock()
	total, count, tails, ok := selfTailHabit(history, firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(event.SelfID)))
	if !ok {
		return ""
	}
	return cfg.promptf(promptSelfTailHabitSpec, map[string]string{
		"total": strconv.Itoa(total),
		"count": strconv.Itoa(count),
		"tails": "「" + strings.Join(tails, "」「") + "」",
	})
}
