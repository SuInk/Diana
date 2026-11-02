package assistant

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// 不调用接话评分模型的两种触发模式，都以 rule_chance 为概率：
//   - rules（规则触发）：rule_chance 是基础概率，再按本地已有的信号乘几个系数。
//   - random（普通）：每条消息都按 rule_chance 原样抽签，不看上下文。
//
// 普通模式每条消息独立抽签：刷屏的群里一分钟几十次机会，冷却一过就插嘴；冷清的
// 群里有人冒泡也照样只有三成；有人接着机器人的回答追问、又没引用，也只能碰运气。
// 规则触发看这几样：
//   - 群里聊得快：最近 2 分钟真人发言超过 ruleChanceActivityRef 条，按条数摊薄，
//     让插话次数跟着时间走，而不是跟着消息条数走。
//   - 冷场后第一句：上一条消息在 10 分钟以前（或内存里没有），抬高。
//   - 问句（带问号，或以「吗」「呢」收尾）：抬高。
//   - 接着跟机器人对话：机器人最近几条之内、3 分钟内说过话，而且是在回这位发言者
//     （引用了他，或机器人那句之前最近的真人发言是他），大幅抬高。生产三天里语义
//     模式判为「在跟机器人说话」才回的 334 条（没 @、没引用、没叫名字），69% 满足
//     这个条件。机器人刚说过话、但说话的是别人，只略微抬高。
//   - 内容很少：去掉表情、图片、@ 和标点后不同的字不超过两个（「哈哈哈」「666」
//     「好的」、纯表情），压低。不维护词表。
//   - 两个人你来我往：最近几条真人发言只来自两个人、轮流说，插进去像抢话，压低。
//   - 对这个人的回复疲劳（reply_fatigue.go 记的快慢两档）：越累越压低。
//
// 最终概率封顶为基础概率的两倍，接着跟机器人对话时放宽到 2.5 倍；rule_chance 填
// 100 视为「总是接」，不做调整，填 0 仍然只回应明确触发。
const (
	triggerModeRules  = "rules"
	triggerModeRandom = "random"

	ruleChanceActivityWindow  = 2 * 60 // 秒
	ruleChanceActivityRef     = 6
	ruleChanceActivityFloor   = 0.2
	ruleChanceQuietGap        = 10 * 60 // 秒
	ruleChanceFollowupWindow  = 3 * 60  // 秒
	ruleChanceFollowupLookup  = 3
	ruleChancePartnerLookup   = 6
	ruleChancePartnerScale    = 2.5
	ruleChanceDuoWindow       = 3 * 60 // 秒
	ruleChanceDuoMessages     = 4
	ruleChanceMaxBoost        = 2.0
	ruleChanceLowContentRunes = 2
)

var ruleChanceQuestion = regexp.MustCompile(`[?？]|[吗呢][~～。!！]*$`)

type ruleChanceFactor struct {
	name  string
	scale float64
}

// ruleChatChance 返回这条消息的接话概率（0–1）和起作用的系数。history 是本会话
// 内存里的近期消息，可以含当前这条，按 MessageID 跳过。
func ruleChatChance(base int, event MessageEvent, text string, history []MessageEvent, botID string, fatigue float64, now time.Time) (float64, []ruleChanceFactor) {
	if base <= 0 {
		return 0, nil
	}
	if base >= 100 {
		return 1, nil
	}
	at := event.Time
	if at <= 0 {
		at = now.Unix()
	}
	var factors []ruleChanceFactor
	add := func(name string, scale float64) {
		if scale != 1 {
			factors = append(factors, ruleChanceFactor{name: name, scale: scale})
		}
	}

	var previous []MessageEvent // 当前这条之前的消息，新的在前
	for index := len(history) - 1; index >= 0; index-- {
		item := history[index]
		if event.MessageID != "" && item.MessageID == event.MessageID {
			continue
		}
		if item.Time > at {
			continue
		}
		previous = append(previous, item)
	}

	recentHumans := 1 // 当前这条
	for _, item := range previous {
		if item.Time <= 0 || at-item.Time > ruleChanceActivityWindow {
			break
		}
		if !assistantHistoryEvent(item, botID) {
			recentHumans++
		}
	}
	if recentHumans > ruleChanceActivityRef {
		add(fmt.Sprintf("群里聊得快（2 分钟 %d 条）", recentHumans), math.Max(ruleChanceActivityFloor, float64(ruleChanceActivityRef)/float64(recentHumans)))
	}

	if len(previous) == 0 || previous[0].Time > 0 && at-previous[0].Time >= ruleChanceQuietGap {
		add("冷场后第一句", 1.5)
	}

	content := replyFatigueNoise.ReplaceAllString(text, "")
	switch {
	case ruleChanceQuestion.MatchString(content):
		add("问句", 1.5)
	case ruleChanceLowContent(content):
		add("内容很少", 0.3)
	}

	maxBoost := ruleChanceMaxBoost
	switch partner, spoke := ruleChanceBotPartner(previous, botID, at); {
	case partner != "" && partner == event.UserID:
		add("接着跟机器人对话", ruleChancePartnerScale)
		maxBoost = ruleChancePartnerScale
	case spoke:
		add("机器人刚说过话", 1.3)
	}

	if ruleChanceDuoExchange(event, previous, botID, at) {
		add("两人正在对话", 0.5)
	}

	if fatigue > 0 {
		add(fmt.Sprintf("对此人回复疲劳 %.2f", fatigue), 1-0.7*math.Min(1, fatigue))
	}

	chance := float64(base) / 100
	for _, factor := range factors {
		chance *= factor.scale
	}
	return math.Min(chance, math.Min(1, float64(base)/100*maxBoost)), factors
}

// ruleChanceLowContent：去掉标点、符号（含 emoji）后不同的字不超过两个。
func ruleChanceLowContent(content string) bool {
	distinct := map[rune]bool{}
	for _, r := range strings.ToLower(content) {
		if unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r) || unicode.Is(unicode.Variation_Selector, r) || r == '‍' {
			continue
		}
		distinct[r] = true
		if len(distinct) > ruleChanceLowContentRunes {
			return false
		}
	}
	return true
}

// ruleChanceBotPartner 找机器人最近一句话（最近 ruleChancePartnerLookup 条之内、
// 窗口内）在回谁：引用了谁就是谁，否则取那句之前最近的真人发言者。spoke 表示机器人
// 在最近 ruleChanceFollowupLookup 条之内说过话。
func ruleChanceBotPartner(previous []MessageEvent, botID string, at int64) (partner string, spoke bool) {
	for index, item := range previous {
		if index >= ruleChancePartnerLookup || item.Time <= 0 || at-item.Time > ruleChanceFollowupWindow {
			return "", false
		}
		if !assistantHistoryEvent(item, botID) {
			continue
		}
		spoke = index < ruleChanceFollowupLookup
		if item.Quoted != nil && strings.TrimSpace(item.Quoted.UserID) != "" {
			return strings.TrimSpace(item.Quoted.UserID), spoke
		}
		for _, before := range previous[index+1:] {
			if !assistantHistoryEvent(before, botID) {
				return before.UserID, spoke
			}
		}
		return "", spoke
	}
	return "", false
}

// ruleChanceDuoExchange：当前这条连同之前的真人发言，最近 ruleChanceDuoMessages 条
// 都在窗口内、只来自两个人且至少换了三次手，就是两个人在对话。中间夹着机器人的话不算。
func ruleChanceDuoExchange(event MessageEvent, previous []MessageEvent, botID string, at int64) bool {
	speakers := []string{event.UserID}
	for _, item := range previous {
		if len(speakers) >= ruleChanceDuoMessages {
			break
		}
		if item.Time <= 0 || at-item.Time > ruleChanceDuoWindow || assistantHistoryEvent(item, botID) {
			return false
		}
		speakers = append(speakers, item.UserID)
	}
	if len(speakers) < ruleChanceDuoMessages {
		return false
	}
	distinct := map[string]bool{}
	turns := 0
	for index, speaker := range speakers {
		distinct[speaker] = true
		if index > 0 && speaker != speakers[index-1] {
			turns++
		}
	}
	return len(distinct) == 2 && turns >= ruleChanceDuoMessages-1
}

func describeRuleChanceFactors(factors []ruleChanceFactor) string {
	if len(factors) == 0 {
		return "无调整"
	}
	parts := make([]string, 0, len(factors))
	for _, factor := range factors {
		parts = append(parts, fmt.Sprintf("%s ×%.2g", factor.name, factor.scale))
	}
	return strings.Join(parts, "，")
}

// ruleTriggerEnabled 报告是否不调用接话评分模型：规则触发和普通两种模式都是。
func (cfg BotConfig) ruleTriggerEnabled() bool {
	if cfg.Participation == nil {
		return false
	}
	mode := cfg.Participation.TriggerMode
	return mode == triggerModeRules || mode == triggerModeRandom
}

// Stable per bot, conversation and message: retries do not draw another ticket.
func ruleTriggerSample(event MessageEvent, chance float64) bool {
	if chance <= 0 || strings.TrimSpace(event.MessageID) == "" {
		return false
	}
	key := fmt.Sprintf("%q/%q/%q/%q", event.Platform, firstNonEmpty(event.ProfileID, event.SelfID), sessionKey(event), event.MessageID)
	digest := sha256.Sum256([]byte(key))
	value := float64(binary.BigEndian.Uint64(digest[:8])>>11) / (1 << 53)
	return value < math.Min(chance, 1)
}

// ruleTriggerHistory 只读内存里的本会话历史：这条在接话热路径上，规则模式本来就是
// 为了省事，不为一个概率去查库或做跨群检索。
func (r *Runtime) ruleTriggerHistory(event MessageEvent) []MessageEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]MessageEvent(nil), r.history[sessionKey(event)]...)
}

// routeRuleReply 判断一批候选（几秒内连发、攒到一起的消息）要不要接话。规则触发
// 按每条各自的概率挑最高的那条抽签：前面是问句、最后一句是「哈哈」，不能只看最后一句。
func (r *Runtime) routeRuleReply(candidates []proactiveReplyCandidate, cfg BotConfig) (MessageEvent, string, []proactiveReplyCandidate, bool) {
	p := cfg.participationPreferences()
	latest := candidates[len(candidates)-1]
	event, text := latest.Event, latest.Text
	label := "规则触发"
	if p.TriggerMode == triggerModeRandom {
		label = "普通模式"
	}
	allowed := false
	switch {
	case event.Kind != EventKindGroup || !r.admits(cfg, event) || r.replyGateBlocksUser(cfg, event):
		event.routingReason = label + "：当前会话或权限不允许主动接话"
	case p.RuleChance == 0:
		event.routingReason = label + "：仅回应 @、引用回复和配置的称呼，未调用接话评分"
	default:
		now := time.Now()
		if stop, ok := r.activeGroupStop(event, now); ok {
			event.routingReason = groupStopRoutingReason(stop, now.In(profileLocation(event.ProfileID)))
		} else if !r.chatInCooldownAllows(event, time.Duration(p.CooldownSeconds)*time.Second) {
			event.routingReason = label + "：闲聊冷却中，未调用接话评分"
		} else if p.TriggerMode == triggerModeRandom {
			allowed = ruleTriggerSample(event, float64(p.RuleChance)/100)
			event.routingReason = fmt.Sprintf("普通模式：固定接话概率 %d%%，命中=%t，未调用接话评分", p.RuleChance, allowed)
		} else {
			history := r.ruleTriggerHistory(event)
			best, bestChance, bestFactors := -1, -1.0, []ruleChanceFactor(nil)
			for index, candidate := range candidates {
				if index < len(candidates)-1 && (!r.admits(cfg, candidate.Event) || r.replyGateBlocksUser(cfg, candidate.Event)) {
					continue
				}
				chance, factors := ruleChatChance(p.RuleChance, candidate.Event, candidate.Text, history, cfg.BotAccount, r.replyFatigueLevel(candidate.Event, now), now)
				if chance > bestChance {
					best, bestChance, bestFactors = index, chance, factors
				}
			}
			event, text = candidates[best].Event, candidates[best].Text
			allowed = ruleTriggerSample(event, bestChance)
			picked := ""
			if len(candidates) > 1 {
				picked = fmt.Sprintf("，按连发 %d 条里概率最高的第 %d 条", len(candidates), best+1)
			}
			event.routingReason = fmt.Sprintf("规则触发：基础概率 %d%%，本条 %.0f%%（%s）%s，命中=%t，未调用接话评分", p.RuleChance, bestChance*100, describeRuleChanceFactors(bestFactors), picked, allowed)
		}
	}
	event.proactiveReply, event.chatInReply = allowed, allowed
	return event, text, []proactiveReplyCandidate{{Event: event, Text: text}}, allowed
}
