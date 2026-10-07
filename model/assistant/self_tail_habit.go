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
// 参照的是同一段聊天里的真人：每轮数一数自己最近的回复和群友（私聊里就是对方）最近的
// 消息各用什么收尾。某一类尾巴自己用得明显比真人多，就在尾部把两边的数字和这几个
// 尾巴摆出来，让这一条照真人的样子收尾。真人自己也爱这么写，那是这个群的风格，不提醒。

// selfTailHabitWindow 和 selfTailHabitPeerWindow 是往回看的自己和真人的消息条数。
const (
	selfTailHabitWindow     = 12
	selfTailHabitPeerWindow = 30
)

// 样本太少不下结论：自己的回复不到 5 条，或者真人的消息不到 10 条。
const (
	selfTailHabitMinSamples     = 5
	selfTailHabitMinPeerSamples = 10
)

// 自己这一类尾巴至少 3 条、占三成以上，并且比真人的比例高出 20 个百分点、到两倍以上，
// 才算口癖：偶尔一两次是调味，和真人差不多就是这个群的说话方式。
const (
	selfTailHabitMinCount   = 3
	selfTailHabitMinPercent = 30
	selfTailHabitMinGap     = 20
)

const promptSelfTailHabit = "大家最近 {peer_total} 条消息里只有 {peer_count} 条用这种尾巴收尾，你最近 {total} 条里却有 {count} 条用 {tails} 收尾，成了口癖。照大家的样子说话：话说完就停，这次别用这些收尾。"

var promptSelfTailHabitSpec = tailSpec("self_tail_habit", "句尾口癖提醒", "自己最近的回复里某一类句尾（括号尾巴、w、喵）用得明显比聊天里的真人多时注入，紧挨语气锚点：摆出两边的数字和这几个尾巴，让这一条照真人的样子收尾。",
	promptSelfTailHabit,
	PromptVar{Name: "peer_total", Description: "参与统计的真人消息条数"},
	PromptVar{Name: "peer_count", Description: "其中用同一类尾巴收尾的条数"},
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

type selfTailHabitStats struct {
	total, count, peerTotal, peerCount int
	tails                              []string
}

// selfTailHabit 比较最近自己和真人的句尾，返回自己用得明显比真人多的那一类。
func selfTailHabit(history []MessageEvent, botID string, markedBots []string) (selfTailHabitStats, bool) {
	var own, peers []string
	for i := len(history) - 1; i >= 0 && (len(own) < selfTailHabitWindow || len(peers) < selfTailHabitPeerWindow); i-- {
		event := history[i]
		if event.crossGroupContext {
			continue
		}
		text := strings.TrimSpace(historyPlainText(event))
		if text == "" {
			continue
		}
		if otherBotHistoryEvent(event, markedBots) {
			continue
		}
		if assistantHistoryEvent(event, botID) {
			if len(own) < selfTailHabitWindow {
				own = append(own, text)
			}
		} else if len(peers) < selfTailHabitPeerWindow {
			peers = append(peers, text)
		}
	}
	if len(own) < selfTailHabitMinSamples || len(peers) < selfTailHabitMinPeerSamples {
		return selfTailHabitStats{}, false
	}
	counts := map[string]int{}
	written := map[string]map[string]int{}
	for _, text := range own {
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
	peerCounts := map[string]int{}
	for _, text := range peers {
		if kind, _ := selfTailKind(text); kind != "" {
			peerCounts[kind]++
		}
	}
	best, bestGap := "", 0
	for _, kind := range []string{"paren", "w", "meow"} {
		count := counts[kind]
		percent := count * 100 / len(own)
		peerPercent := peerCounts[kind] * 100 / len(peers)
		if count < selfTailHabitMinCount || percent < selfTailHabitMinPercent || percent-peerPercent < selfTailHabitMinGap || percent < 2*peerPercent {
			continue
		}
		if gap := percent - peerPercent; gap > bestGap {
			best, bestGap = kind, gap
		}
	}
	if best == "" {
		return selfTailHabitStats{}, false
	}
	stats := selfTailHabitStats{total: len(own), count: counts[best], peerTotal: len(peers), peerCount: peerCounts[best]}
	for tail := range written[best] {
		stats.tails = append(stats.tails, tail)
	}
	sort.Slice(stats.tails, func(i, j int) bool {
		if written[best][stats.tails[i]] != written[best][stats.tails[j]] {
			return written[best][stats.tails[i]] > written[best][stats.tails[j]]
		}
		return stats.tails[i] < stats.tails[j]
	})
	return stats, true
}

// selfTailHabitPrompt 在自己的句尾比真人明显多时给出提醒，否则返回空串。
func (r *Runtime) selfTailHabitPrompt(event MessageEvent, cfg BotConfig) string {
	r.mu.RLock()
	history := append([]MessageEvent(nil), r.history[sessionKey(event)]...)
	r.mu.RUnlock()
	stats, ok := selfTailHabit(history, firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(event.SelfID)), cfg.MarkedBotIDs)
	if !ok {
		return ""
	}
	return cfg.promptf(promptSelfTailHabitSpec, map[string]string{
		"peer_total": strconv.Itoa(stats.peerTotal),
		"peer_count": strconv.Itoa(stats.peerCount),
		"total":      strconv.Itoa(stats.total),
		"count":      strconv.Itoa(stats.count),
		"tails":      "「" + strings.Join(stats.tails, "」「") + "」",
	})
}
