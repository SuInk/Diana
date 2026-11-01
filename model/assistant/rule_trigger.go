package assistant

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

func (cfg BotConfig) ruleTriggerEnabled() bool {
	return cfg.Participation != nil && cfg.Participation.TriggerMode == "rules"
}

// Stable per bot, conversation and message: retries do not draw another ticket.
func ruleTriggerSample(event MessageEvent, chance int) bool {
	if chance <= 0 || strings.TrimSpace(event.MessageID) == "" {
		return false
	}
	key := fmt.Sprintf("%q/%q/%q/%q", event.Platform, firstNonEmpty(event.ProfileID, event.SelfID), sessionKey(event), event.MessageID)
	digest := sha256.Sum256([]byte(key))
	value := float64(binary.BigEndian.Uint64(digest[:8])>>11) / (1 << 53)
	return value < float64(min(chance, 100))/100
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
		if stop, ok := r.activeGroupStop(event, time.Now()); ok {
			event.routingReason = groupStopRoutingReason(stop, time.Now().In(profileLocation(event.ProfileID)))
		} else if !r.chatInCooldownAllows(event, time.Duration(p.CooldownSeconds)*time.Second) {
			event.routingReason = "规则触发：闲聊冷却中，未调用接话评分"
		} else {
			allowed = ruleTriggerSample(event, p.RuleChance)
			event.routingReason = fmt.Sprintf("规则触发：随机接话概率 %d%%，命中=%t，未调用接话评分", p.RuleChance, allowed)
		}
	}
	event.proactiveReply, event.chatInReply = allowed, allowed
	return event, text, []proactiveReplyCandidate{{Event: event, Text: text}}, allowed
}
