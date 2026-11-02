package assistant

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// 规则触发的接话概率：rule_chance 是基础概率，再按本地已有的信号乘几个系数，
// 全程不调用模型。
//
// 以前每条消息都按同一个概率独立抽签：刷屏的群里一分钟几十次机会，冷却一过就插嘴；
// 冷清的群里有人冒泡也照样只有三成。现在看这几样：
//   - 群里聊得快：最近 2 分钟真人发言超过 ruleChanceActivityRef 条，按条数摊薄，
//     让插话次数跟着时间走，而不是跟着消息条数走。
//   - 冷场后第一句：上一条消息在 10 分钟以前（或内存里没有），抬高。
//   - 问句：抬高，有人在问就更值得接。
//   - 刚说过话：机器人最近几条之内、3 分钟内说过话，有人接着聊，抬高。
//   - 单字附和、纯表情图片：压低。
//   - 两个人你来我往：最近几条真人发言只来自两个人、轮流说，插进去像抢话，压低。
//   - 对这个人的回复疲劳（reply_fatigue.go 记的快慢两档）：越累越压低。
//
// 最终概率封顶为基础概率的两倍；rule_chance 填 100 视为「总是接」，不做调整，
// 填 0 仍然只回应明确触发。
const (
	ruleChanceActivityWindow = 2 * 60 // 秒
	ruleChanceActivityRef    = 6
	ruleChanceActivityFloor  = 0.2
	ruleChanceQuietGap       = 10 * 60 // 秒
	ruleChanceFollowupWindow = 3 * 60  // 秒
	ruleChanceFollowupLookup = 3
	ruleChanceDuoWindow      = 3 * 60 // 秒
	ruleChanceDuoMessages    = 4
	ruleChanceMaxBoost       = 2.0
)

var (
	ruleChanceQuestion = regexp.MustCompile(`[?？]|(吗|呢|么|嘛)[\s~～。!！]*$|为什么|为啥|怎么|咋|谁|哪|啥|什么|多少|几[个点号]|有没有|是不是|能不能|可不可以`)
	ruleChanceFiller   = regexp.MustCompile(`^(哈|呵|嘿|嘻|草|艹|笑|6|嗯|哦|噢|喔|啊|好|对|是|行|ok|OK|Ok|h|w|233|\.|。|!|！|~|～|\?|？)*$`)
)

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

	content := strings.TrimSpace(replyFatigueNoise.ReplaceAllString(text, ""))
	switch {
	case content == "" || ruleChanceFiller.MatchString(content) || utf8.RuneCountInString(content) <= 1:
		add("附和或表情", 0.3)
	case ruleChanceQuestion.MatchString(content):
		add("问句", 1.5)
	}

	for index, item := range previous {
		if index >= ruleChanceFollowupLookup || item.Time <= 0 || at-item.Time > ruleChanceFollowupWindow {
			break
		}
		if assistantHistoryEvent(item, botID) {
			add("接着机器人刚说的话", 1.3)
			break
		}
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
	return math.Min(chance, math.Min(1, float64(base)/100*ruleChanceMaxBoost)), factors
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

func (cfg BotConfig) ruleTriggerEnabled() bool {
	return cfg.Participation != nil && cfg.Participation.TriggerMode == "rules"
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

func (r *Runtime) routeRuleReply(event MessageEvent, text string, cfg BotConfig) (MessageEvent, string, []proactiveReplyCandidate, bool) {
	p := cfg.participationPreferences()
	allowed := false
	switch {
	case event.Kind != EventKindGroup || !r.admits(cfg, event) || r.replyGateBlocksUser(cfg, event):
		event.routingReason = "规则触发：当前会话或权限不允许主动接话"
	case p.RuleChance == 0:
		event.routingReason = "规则触发：仅回应 @、引用回复和配置的称呼，未调用接话评分"
	default:
		now := time.Now()
		if stop, ok := r.activeGroupStop(event, now); ok {
			event.routingReason = groupStopRoutingReason(stop, now.In(profileLocation(event.ProfileID)))
		} else if !r.chatInCooldownAllows(event, time.Duration(p.CooldownSeconds)*time.Second) {
			event.routingReason = "规则触发：闲聊冷却中，未调用接话评分"
		} else {
			chance, factors := ruleChatChance(p.RuleChance, event, text, r.ruleTriggerHistory(event), cfg.BotAccount, r.replyFatigueLevel(event, now), now)
			allowed = ruleTriggerSample(event, chance)
			event.routingReason = fmt.Sprintf("规则触发：基础概率 %d%%，本条 %.0f%%（%s），命中=%t，未调用接话评分", p.RuleChance, chance*100, describeRuleChanceFactors(factors), allowed)
		}
	}
	event.proactiveReply, event.chatInReply = allowed, allowed
	return event, text, []proactiveReplyCandidate{{Event: event, Text: text}}, allowed
}
