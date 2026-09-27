// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"sync"
	"time"
)

// 回复密度：机器人回某个账号回得很密时，发送前审核要额外判断这一连串来回有没有
// 明确任务。有任务（下棋报步、解题、一起做事且在推进）照常回；没有任务（漫无目的地
// 互相接戏、续剧情、斗嘴、为回应而回应）计入空转次数，累计够了暂停。
//
// 以前防循环只认「对方是自动 AI」或「这一来一回没有任何内容」。两台 AI 你一句我一句地
// 演一个没有尽头的故事，每条都有内容，审核永远不计数，线上一小时回了同一台 AI 一百三十
// 多条。密度本身不能当结论——下棋也会很密——它只决定什么时候该问「这是在干什么」。
//
// 只对被标记为机器人的账号才递密度、问目的：
// 真人斗嘴、调侃来调侃去，在群聊里再正常不过，线上被这一项暂停的真人就是这么来的。
//
// 这里只数回了几条，不拦任何消息。以前判到空转还会按账号「降欲望」：不主动接、只接
// 点名、点名也按条冷却。它把被 @ 的真人也晾在一边，已经删掉；判到空转只计数，累计
// 够了走暂停，那一层有明确的提示和解除方式。
const (
	replyDensityWindow = 10 * time.Minute
	// 10 分钟内回同一个账号达到这么多条，就把密度证据交给发送前审核，让它判这一串
	// 来回有没有目的。
	//
	// 问这一句不额外花钱：空转判断和表达质量、账号安全共用发送前那一次审核调用，
	// 密度只是同一份载荷里多一个字段。两条起判：一来一回两次之后才谈得上「一连串
	// 来回」，再早就没有东西可判。判成「无目的」的后果由审核自己的判据兜底——有明确
	// 任务在推进一律 false、拿不准一律 false——密度只负责把问题递上去。
	replyDensityDenseLimit = 2
)

type replyDensityHit struct {
	MessageID string
	At        time.Time
}

type replyDensityTracker struct {
	mu    sync.Mutex
	byKey map[string][]replyDensityHit
}

// replyDensity 是发给审核的密度证据。
type replyDensity struct {
	BotRepliesToSender int `json:"bot_replies_to_sender"`
	WindowMinutes      int `json:"window_minutes"`
}

func pruneReplyDensityHits(hits []replyDensityHit, now time.Time) []replyDensityHit {
	kept := hits[:0]
	for _, hit := range hits {
		if age := now.Sub(hit.At); age >= 0 && age <= replyDensityWindow {
			kept = append(kept, hit)
		}
	}
	return kept
}

func (r *Runtime) replyDensityApplies(event MessageEvent) bool {
	cfg := r.effectiveConfigForEvent(event)
	userID := strings.TrimSpace(event.UserID)
	if userID == "" || cfg.IsOwnerEvent(event) || !boolValue(cfg.BotReplyLoopDetectionEnabled, true) {
		return false
	}
	botID := firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(event.SelfID))
	return userID != botID && (event.Kind == EventKindGroup || event.Kind == EventKindPrivate)
}

// recordReplyDensitySend 在回复成功发出后记一次。同一条消息只记一次。
func (r *Runtime) recordReplyDensitySend(event MessageEvent, now time.Time) {
	if !r.replyDensityApplies(event) {
		return
	}
	key := botReplyLoopKey(event, event.UserID)
	r.replyDensity.mu.Lock()
	defer r.replyDensity.mu.Unlock()
	hits := pruneReplyDensityHits(r.replyDensity.byKey[key], now)
	messageID := strings.TrimSpace(event.MessageID)
	if messageID != "" {
		for _, hit := range hits {
			if hit.MessageID == messageID {
				r.replyDensity.byKey[key] = hits
				return
			}
		}
	}
	if r.replyDensity.byKey == nil {
		r.replyDensity.byKey = map[string][]replyDensityHit{}
	}
	r.replyDensity.byKey[key] = append(hits, replyDensityHit{MessageID: messageID, At: now})
}

// replyDensityForAudit 在回得很密时返回给审核的密度证据。
func (r *Runtime) replyDensityForAudit(event MessageEvent, now time.Time) (replyDensity, bool) {
	if !r.replyDensityApplies(event) {
		return replyDensity{}, false
	}
	key := botReplyLoopKey(event, event.UserID)
	r.replyDensity.mu.Lock()
	defer r.replyDensity.mu.Unlock()
	hits := pruneReplyDensityHits(r.replyDensity.byKey[key], now)
	if len(hits) == 0 {
		delete(r.replyDensity.byKey, key)
	} else {
		r.replyDensity.byKey[key] = hits
	}
	if len(hits) < replyDensityDenseLimit {
		return replyDensity{}, false
	}
	return replyDensity{BotRepliesToSender: len(hits), WindowMinutes: int(replyDensityWindow / time.Minute)}, true
}

func (r *Runtime) resetReplyDensityUser(userID string) {
	userID = strings.TrimSpace(userID)
	if r == nil || userID == "" {
		return
	}
	suffix := "\x00" + userID
	r.replyDensity.mu.Lock()
	for key := range r.replyDensity.byKey {
		if strings.HasSuffix(key, suffix) {
			delete(r.replyDensity.byKey, key)
		}
	}
	r.replyDensity.mu.Unlock()
}
