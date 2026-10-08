// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// 闲聊插话的动态门控，照 MaiBot（maisaka/turn_trigger）的做法：
//
// 每批候选先用一条不花 token 的公式估「这里值得插一句」的概率，再在最近一小时的
// 窗口里让实际插话数贴近「预计值得插话的次数 × 档位比例」。阈值从窗口里的概率
// 分布推出来：按概率从高到低累计，累计到保留额度处的那个概率就是阈值；实际插话多于
// 目标时收紧，只收紧不放宽（一波消息刚到时预计数先涨、实际还没跟上，若据此放宽，
// 这波过去就收不回来）。
//
// 它替掉了以前「机器人近 10 分钟发言占比 ≥25% 就不插」那道闸：占比闸按一刀切的
// 比例在热闹群和安静群之间来回误伤；按预计需求折算目标，群热闹时额度自然多，冷清时
// 自然少。用户设的闲聊冷却照旧生效，只是最短间隔，总量由这里管。
//
// 只管闲聊这一支。「在跟机器人说话」照旧每条都问评分模型，不经过这里：曾试过照 MaiBot
// 用这条公式在调用模型之前就拦掉低概率消息，省下 13%～33% 的评分调用，但回放里会漏掉
// 2%～4% 真在跟机器人说话的消息（顺着机器人上一句接的「是米5c」、机器人要的确认码），
// 公式只看结构特征，看不出语义上的接话。
//
// 回放（10 个群 14 天、3.3 万次接话评分，模型结论用线上实际评分）：闲聊插话 1091 → 788，
// 单群单小时插话最多 25 → 16（回放未叠加 30 秒默认冷却），在跟机器人说话的消息零漏判。

const (
	chatInGateWindow        = time.Hour
	chatInGateRound         = 40 * time.Second
	chatInGateMinScores     = 20
	chatInGateHistoryWindow = 300
	// chatInGateNoBotSeconds 是上下文里没有机器人发言时用的间隔，与拟合时的缺省值一致。
	chatInGateNoBotSeconds = 3600
)

// chatInGateFrequency 是各闲聊档位的保留比例：窗口内「预计值得插话的次数」里留下几成。
// 比例按回放选的：这一组让总插话量和单小时峰值都低于旧的冷却加占比闸。
var chatInGateFrequency = map[string]float64{
	"minimal": 0.10,
	"low":     0.25,
	"medium":  0.50,
	"high":    0.75,
	"always":  1,
}

// chatInLikelihoodWeights 用线上接话评分拟合：标签是评分模型给的闲聊分 ≥0.5，样本是
// 09-23 到 10-07 进过评分的 3.3 万批消息。按时间切开（前 10 天拟合、后 4 天检验）AUC 0.85。
// 重新拟合后只需要换这里和 chatInGateStaticThresholds。
var chatInLikelihoodWeights = struct {
	intercept, mention, atOther, question, placeholder, selfRatio, recentCount,
	logSinceBot, pending, follow, rightAfter, engaged, length float64
}{
	intercept:   -1.5031,
	mention:     0.6446,
	atOther:     -2.9068,
	question:    0.0834,
	placeholder: -2.5455,
	selfRatio:   3.2535,
	recentCount: 0.0029,
	logSinceBot: -0.0374,
	pending:     -0.1179,
	follow:      0.911,
	rightAfter:  0.5537,
	engaged:     0.3373,
	length:      0.0087,
}

// chatInGateStaticThresholds 是窗口里评分不足 chatInGateMinScores 个时的阈值表：
// 线上 14 天的概率分布里，保留各比例的预计插话所对应的概率。
var chatInGateStaticThresholds = [][2]float64{
	{0, 1}, {0.1, 0.746}, {0.2, 0.653}, {0.3, 0.565}, {0.4, 0.479}, {0.5, 0.389},
	{0.6, 0.296}, {0.7, 0.202}, {0.8, 0.143}, {0.9, 0.096}, {1, 0},
}

type chatInLikelihoodInput struct {
	Mention, AtOther, Question, Placeholder bool
	SelfRatio                               float64
	RecentCount                             int
	SecondsSinceBot                         float64
	Pending                                 int
	Follow, RightAfter, Engaged             bool
	Length                                  int
}

func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

func estimateChatInProbability(in chatInLikelihoodInput) float64 {
	w := chatInLikelihoodWeights
	logit := w.intercept +
		w.mention*boolFloat(in.Mention) +
		w.atOther*boolFloat(in.AtOther) +
		w.question*boolFloat(in.Question) +
		w.placeholder*boolFloat(in.Placeholder) +
		w.selfRatio*math.Min(1, math.Max(0, in.SelfRatio)) +
		w.recentCount*float64(max(0, in.RecentCount)) +
		w.logSinceBot*math.Log1p(math.Min(chatInGateNoBotSeconds, math.Max(0, in.SecondsSinceBot))) +
		w.pending*float64(min(20, max(0, in.Pending))) +
		w.follow*boolFloat(in.Follow) +
		w.rightAfter*boolFloat(in.RightAfter) +
		w.engaged*boolFloat(in.Engaged) +
		w.length*float64(min(60, max(0, in.Length)))
	return 1 / (1 + math.Exp(-logit))
}

func historyAge(item proactiveReplyHistoryItem) int64 {
	if item.AgeSeconds == nil {
		return 0
	}
	return *item.AgeSeconds
}

// chatInLikelihoodFromPayload 从接话评分已经拼好的上下文里取特征，不额外查库。
// recent_messages 按时间倒序，当前消息不在里面。
func chatInLikelihoodFromPayload(payload proactiveReplyPayload, currentUserID string) chatInLikelihoodInput {
	text := strings.TrimSpace(payload.CurrentText)
	in := chatInLikelihoodInput{
		AtOther:     payload.Addressing.ReplyTarget == "other" || payload.Addressing.MentionsOther,
		Question:    strings.ContainsAny(text, "?？"),
		Placeholder: text == "",
		Length:      utf8.RuneCountInString(text),
	}
	lower := strings.ToLower(text)
	for _, alias := range payload.BotAliases {
		if alias = strings.ToLower(strings.TrimSpace(alias)); alias != "" && strings.Contains(lower, alias) {
			in.Mention = true
			break
		}
	}
	since := float64(-1)
	recent, recentSelf, pending := 0, 0, 0
	counting := true
	var lastBot *proactiveReplyHistoryItem
	for i := range payload.RecentMessages {
		item := payload.RecentMessages[i]
		age := historyAge(item)
		if item.IsBot && since < 0 {
			since, lastBot = float64(age), &payload.RecentMessages[i]
		}
		if counting {
			if item.IsBot {
				counting = false
			} else if age <= 120 {
				pending++
			}
		}
		if age <= chatInGateHistoryWindow {
			recent++
			if item.IsBot {
				recentSelf++
			}
		}
		if age > 3600 || in.Engaged {
			continue
		}
		user := strings.TrimSpace(currentUserID)
		switch {
		case item.IsBot && user != "" && item.Addressing.ReplyUserID == user:
			in.Engaged = true
		case item.IsBot && user != "":
			for _, mention := range item.Addressing.Mentions {
				if mention.UserID == user {
					in.Engaged = true
				}
			}
		case !item.IsBot && user != "" && item.UserID == user:
			in.Engaged = item.Addressing.ReplyTarget == "self" || item.Addressing.MentionsSelf
		}
	}
	if since < 0 {
		since = chatInGateNoBotSeconds
	}
	in.SecondsSinceBot = since
	in.RecentCount = recent + 1
	in.SelfRatio = float64(recentSelf) / float64(recent+1)
	in.Pending = pending + 1
	in.RightAfter = len(payload.RecentMessages) > 0 && payload.RecentMessages[0].IsBot && historyAge(payload.RecentMessages[0]) <= 60
	in.Follow = lastBot != nil && since <= 120 && payload.LastBotAddressedCurrentSender
	return in
}

type chatInGateScore struct {
	at          time.Time
	probability float64
}

// chatInGate 是单个会话的门控状态。
type chatInGate struct {
	scores    []chatInGateScore
	replies   []time.Time
	roundOpen time.Time
}

type chatInGateDecision struct {
	Allowed     bool
	Probability float64
	Threshold   float64
	Expected    float64
	Target      float64
	Actual      int
}

func (d chatInGateDecision) reason() string {
	return fmt.Sprintf("插话概率 %.2f / 阈值 %.2f，近一小时预计 %.1f、目标 %.1f、已插 %d", d.Probability, d.Threshold, d.Expected, d.Target, d.Actual)
}

func (g *chatInGate) prune(now time.Time) {
	cut := now.Add(-chatInGateWindow)
	i := 0
	for i < len(g.scores) && g.scores[i].at.Before(cut) {
		i++
	}
	g.scores = g.scores[i:]
	j := 0
	for j < len(g.replies) && g.replies[j].Before(cut) {
		j++
	}
	g.replies = g.replies[j:]
}

// recordDemand 记下这一轮预计的插话需求。40 秒内到的消息算同一轮、只计一次：
// 不设门控时评分也是攒批跑的，按条计会把热闹时段的需求算多好几倍。
func (g *chatInGate) recordDemand(probability float64, now time.Time) {
	if !g.roundOpen.IsZero() && now.Sub(g.roundOpen) < chatInGateRound && len(g.scores) > 0 {
		g.scores[len(g.scores)-1].probability = probability
		return
	}
	g.roundOpen = now
	g.scores = append(g.scores, chatInGateScore{at: now, probability: probability})
}

func (g *chatInGate) evaluate(probability, frequency float64, now time.Time) chatInGateDecision {
	g.prune(now)
	expected := 0.0
	scores := make([]float64, 0, len(g.scores))
	for _, score := range g.scores {
		expected += score.probability
		scores = append(scores, score.probability)
	}
	actual := len(g.replies)
	frequency = math.Min(1, math.Max(0, frequency))
	target := frequency * expected
	keep := frequency
	if frequency < 1 && expected > 0 {
		feedback := math.Min(1, math.Max(0, 1+(target-float64(actual))/math.Max(1, target)))
		keep = math.Min(1, math.Max(0, frequency*feedback))
	}
	threshold := chatInGateThreshold(scores, expected, keep)
	return chatInGateDecision{
		Allowed:     probability >= threshold,
		Probability: probability,
		Threshold:   threshold,
		Expected:    expected,
		Target:      target,
		Actual:      actual,
	}
}

func chatInGateThreshold(scores []float64, expected, keep float64) float64 {
	if keep >= 1 {
		return 0
	}
	if keep <= 0 {
		return 1
	}
	if len(scores) < chatInGateMinScores {
		table := chatInGateStaticThresholds
		for i := 1; i < len(table); i++ {
			if keep <= table[i][0] {
				lo, hi := table[i-1], table[i]
				return lo[1] + (hi[1]-lo[1])*(keep-lo[0])/(hi[0]-lo[0])
			}
		}
		return 0
	}
	sorted := append([]float64(nil), scores...)
	sort.Sort(sort.Reverse(sort.Float64Slice(sorted)))
	budget, accumulated := keep*expected, 0.0
	for _, score := range sorted {
		accumulated += score
		if accumulated >= budget {
			return score
		}
	}
	return 0
}

// chatInGateCheck 记下这批候选的需求并给出闲聊分支能否放行。档位为 off 或不认识时
// 不放行；always 比例为 1，阈值恒为 0。
func (r *Runtime) chatInGateCheck(event MessageEvent, payload proactiveReplyPayload, chatLevel string, now time.Time) chatInGateDecision {
	frequency, ok := chatInGateFrequency[chatLevel]
	probability := estimateChatInProbability(chatInLikelihoodFromPayload(payload, event.UserID))
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.chatInGates == nil {
		r.chatInGates = map[string]*chatInGate{}
	}
	key := chatInCooldownKey(event)
	gate := r.chatInGates[key]
	if gate == nil {
		gate = &chatInGate{}
		r.chatInGates[key] = gate
	}
	gate.recordDemand(probability, now)
	if !ok {
		return chatInGateDecision{Probability: probability, Threshold: 1}
	}
	decision := gate.evaluate(probability, frequency, now)
	// 评分模型已经跑过这一轮，下一批消息属于新的一轮。
	gate.roundOpen = time.Time{}
	return decision
}

// recordChatInGateReply 在闲聊插话真正发出去后计入实际次数。
func (r *Runtime) recordChatInGateReply(event MessageEvent, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if gate := r.chatInGates[chatInCooldownKey(event)]; gate != nil {
		gate.replies = append(gate.replies, now)
	}
}
