// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// RandomRepeatConfig 是行为设置；群配置留空时跟随机器人。
type RandomRepeatConfig struct {
	Enabled         bool `json:"enabled"`
	Threshold       int  `json:"threshold"`
	Probability     int  `json:"probability"`
	CooldownSeconds int  `json:"cooldown_seconds"`
}

func copyRandomRepeat(value *RandomRepeatConfig) *RandomRepeatConfig {
	if value == nil {
		return nil
	}
	result := value.normalized()
	return &result
}
func (value *RandomRepeatConfig) normalized() RandomRepeatConfig {
	if value == nil {
		return RandomRepeatConfig{Threshold: 2, Probability: 30, CooldownSeconds: 60}
	}
	result := *value
	result.Threshold = max(2, min(10, result.Threshold))
	result.Probability = max(0, min(100, result.Probability))
	result.CooldownSeconds = max(-1, min(600, result.CooldownSeconds))
	return result
}

type repeatChain struct {
	text, messageID    string
	users              map[string]bool
	attempted          bool
	triggered          bool
	updated, lastReply time.Time
}

const randomRepeatAlreadyTriggeredOutcome = "ignored_random_repeat"

type repeatDecision struct {
	text             string
	alreadyTriggered bool
}

// randomRepeater keeps one bounded chain per robot and conversation.
// Observation never sends: runtime admission and suppression checks run before takeReply.
type randomRepeater struct {
	mu     sync.Mutex
	chains map[string]*repeatChain
	now    func() time.Time
	roll   func() int
}

func newRandomRepeater() *randomRepeater {
	return &randomRepeater{chains: make(map[string]*repeatChain), now: time.Now, roll: func() int { return rand.IntN(100) }}
}

func repeatText(event MessageEvent) string {
	if event.Kind != EventKindGroup || event.GroupID == "" || event.UserID == "" || event.MessageID == "" || event.SenderIsBot || event.Outbound || event.selfSent || event.UserID == event.SelfID || event.ToMe || event.Quoted != nil || event.Edited || len(event.MentionTargets) > 0 {
		return ""
	}
	for _, segment := range event.Segments {
		if segment.Type != "text" {
			return ""
		}
	}
	var builder strings.Builder
	for _, segment := range event.Segments {
		builder.WriteString(segment.Data["text"])
	}
	text := builder.String()
	if len(event.Segments) == 0 {
		text = event.RawMessage
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || utf8.RuneCountInString(text) > 500 || strings.Contains(text, "[CQ:") || strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	return text
}

func repeatKey(event MessageEvent) string {
	return event.Platform + "\x00" + event.ProfileID + "\x00" + event.SelfID + "\x00" + sessionKey(event) + "\x00" + event.MessageThreadID
}

func (p *randomRepeater) observe(_ context.Context, event MessageEvent) MessageEvent {
	if event.Kind != EventKindGroup {
		return event
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	key := repeatKey(event)
	chain := p.chains[key]
	if chain != nil && chain.messageID == event.MessageID {
		return event
	}
	text := repeatText(event)
	if chain == nil {
		// Bound memory by evicting the least recently observed conversation only
		// when capacity is reached. Elapsed time alone never restarts a chain.
		if len(p.chains) >= 4096 {
			oldestKey := ""
			var oldest time.Time
			for candidate, existing := range p.chains {
				if oldestKey == "" || existing.updated.Before(oldest) {
					oldestKey, oldest = candidate, existing.updated
				}
			}
			delete(p.chains, oldestKey)
		}
		chain = &repeatChain{}
		p.chains[key] = chain
	}
	if text == "" || text != chain.text {
		chain.text, chain.users = text, make(map[string]bool)
		chain.triggered = false
	}
	// Each new message gets one chance; duplicate delivery cannot reroll.
	chain.attempted = false
	chain.messageID, chain.updated = event.MessageID, now
	if text != "" && len(chain.users) < 10 {
		chain.users[event.UserID] = true
	}
	return event
}

func (p *randomRepeater) takeReply(event MessageEvent, settings RandomRepeatConfig) repeatDecision {
	settings = settings.normalized()
	p.mu.Lock()
	defer p.mu.Unlock()
	chain := p.chains[repeatKey(event)]
	if chain == nil || chain.text == "" || chain.messageID != event.MessageID {
		return repeatDecision{}
	}
	if settings.CooldownSeconds == -1 && chain.triggered {
		return repeatDecision{alreadyTriggered: true}
	}
	if chain.attempted || len(chain.users) < max(2, min(10, settings.Threshold)) {
		return repeatDecision{}
	}
	chain.attempted = true
	now := p.now()
	if (!chain.lastReply.IsZero() && now.Sub(chain.lastReply) < time.Duration(max(0, min(600, settings.CooldownSeconds)))*time.Second) || p.roll() >= max(0, min(100, settings.Probability)) {
		return repeatDecision{}
	}
	// -1 模式在抽中后结束这一轮；非负值继续按冷却和每条消息的概率判断。
	chain.triggered = true
	chain.lastReply = now
	return repeatDecision{text: chain.text}
}

func (r *Runtime) observeRandomRepeat(ctx context.Context, event MessageEvent) {
	p := r.repeater
	if p == nil {
		return
	}
	enabled := r.effectiveConfigForEvent(event).RandomRepeat.normalized().Enabled
	if !enabled {
		p.mu.Lock()
		delete(p.chains, repeatKey(event))
		p.mu.Unlock()
		return
	}
	// A manually marked bot and a direct request break the chain too.
	event.SenderIsBot = event.SenderIsBot || r.accountMarkedAsBot(event)
	event.ToMe = event.ToMe || r.shouldHandle(event, PlainText(event.Segments))
	p.observe(ctx, event)
}

func (r *Runtime) tryRandomRepeat(ctx context.Context, event MessageEvent) string {
	if event.mutedJudgeOnly != "" || r.accountMarkedAsBot(event) || !r.admits(r.effectiveConfigForEvent(event), event) {
		return ""
	}
	settings := r.effectiveConfigForEvent(event).RandomRepeat.normalized()
	p := r.repeater
	if !settings.Enabled || p == nil {
		return ""
	}
	decision := p.takeReply(event, settings)
	if decision.alreadyTriggered {
		r.record(r.decisionEventRecord(event, event.RawMessage, randomRepeatAlreadyTriggeredOutcome))
		return randomRepeatAlreadyTriggeredOutcome
	}
	reply := decision.text
	if reply == "" {
		return ""
	}
	record := r.decisionEventRecord(event, reply, "replied")
	record.Reason = "群友连续复读，随机跟读"
	record.Reply = reply
	record.Handled = true
	if err := r.sendOutgoing(withReplySuppressionSendGuard(ctx), event, OutgoingMessage{Text: reply, LiteralText: true}); err != nil {
		record.Outcome = "error"
		record.Error = err.Error()
		record.Reply = ""
		record.Reason = "随机复读发送失败：" + err.Error()
	}
	r.record(record)
	return record.Outcome
}
