// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// 回复欲望衰减：机器人回某个账号回得很密时，发送前审核要额外判断这一连串来回有没有
// 明确任务。有任务（下棋报步、解题、一起做事且在推进）照常回；没有任务（漫无目的地
// 互相接戏、续剧情、斗嘴、为回应而回应）就降欲望，并计入空转次数，累计够了暂停。
//
// 以前防循环只认「对方是自动 AI」或「这一来一回没有任何内容」。两台 AI 你一句我一句地
// 演一个没有尽头的故事，每条都有内容，审核永远不计数，线上一小时回了同一台 AI 一百三十
// 多条。密度本身不能当结论——下棋也会很密——它只决定什么时候该问「这是在干什么」。
//
// 线上 7 天的数据：真人在 10 分钟内最多被回复 17 次，两台 AI 分别是 40 和 67 次。
const (
	replyDampingWindow = 10 * time.Minute
	// 10 分钟内回同一个账号达到这么多条，就把密度证据交给发送前审核，让它判这一串
	// 来回有没有目的；降欲望期间点名消息的冷却也从这条起按条累加。
	//
	// 这个数以前是 10，照着上面那句「真人 10 分钟内最多被回复 17 次」留的余量。但
	// 密度在这里决定的不是结论，只是什么时候该问一句「这是在干什么」，而问这一句
	// 不额外花钱：空转判断和表达质量、账号安全共用发送前那一次审核调用，密度只是
	// 同一份载荷里多一个字段。既然问是免费的，就没有理由让它先转够十轮。
	//
	// 两条起判：一来一回两次之后才谈得上「一连串来回」，再早就没有东西可判。判成
	// 「无目的」的后果由审核自己的判据兜底——有明确任务在推进一律 false、拿不准一律
	// false——密度只负责把问题递上去。被标记成机器人的账号走的也是这个数，不再单设
	// 一档：判据本来就与对方是不是机器人无关。
	replyDampingDenseLimit = 2
	// 降欲望期间点名消息的冷却，每多回一条就再加一档。
	replyDampingCooldownStep = 20 * time.Second
	// 判到空转之后，降欲望持续这么久；期间只要再判到有目的就立刻解除。
	replyDampingPurposelessRetention = 10 * time.Minute
)

// 降欲望的三种起因，原样写进事件理由。
const (
	replyDampingCausePurposeless = "最近判断这串来回没有明确目的"
	replyDampingCauseMeaningless = "最近判断这一来一回已经没有实质内容"
	replyDampingCauseSelfRepeat  = "最近判断机器人在把自己说过的话换个说法重复"
)

type replyDampingHit struct {
	MessageID string
	At        time.Time
}

type replyDampingState struct {
	Sent []replyDampingHit
	// PurposelessAt 是最近一次判到空转的时间，零值表示当前没有降欲望。
	PurposelessAt time.Time
	// PurposelessCause 是那次判的是哪一种空转。写进事件理由时要如实说：停下来的
	// 原因可能是「在复读自己」，而不是「这串来回没有目的」——理由写错会让下一个
	// 排查的人照着错的方向找。
	PurposelessCause string
}

type replyDamping struct {
	mu    sync.Mutex
	byKey map[string]*replyDampingState
}

type replyDampingVerdict struct {
	Skip   bool
	Reason string
}

// replyDensity 是发给审核的密度证据。
type replyDensity struct {
	BotRepliesToSender int `json:"bot_replies_to_sender"`
	WindowMinutes      int `json:"window_minutes"`
}

func pruneReplyDampingHits(hits []replyDampingHit, now time.Time) []replyDampingHit {
	kept := hits[:0]
	for _, hit := range hits {
		if age := now.Sub(hit.At); age >= 0 && age <= replyDampingWindow {
			kept = append(kept, hit)
		}
	}
	return kept
}

func (r *Runtime) replyDampingApplies(event MessageEvent) bool {
	cfg := r.effectiveConfigForEvent(event)
	userID := strings.TrimSpace(event.UserID)
	if userID == "" || cfg.IsOwnerEvent(event) || !boolValue(cfg.BotReplyLoopDetectionEnabled, true) {
		return false
	}
	botID := firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(event.SelfID))
	return userID != botID && (event.Kind == EventKindGroup || event.Kind == EventKindPrivate)
}

// replyDampingStateLocked 取出并清理这个账号的状态；调用方持有锁。
func (r *Runtime) replyDampingStateLocked(event MessageEvent, now time.Time, create bool) *replyDampingState {
	key := botReplyLoopKey(event, event.UserID)
	state := r.replyDamping.byKey[key]
	if state == nil {
		if !create {
			return nil
		}
		if r.replyDamping.byKey == nil {
			r.replyDamping.byKey = map[string]*replyDampingState{}
		}
		state = &replyDampingState{}
		r.replyDamping.byKey[key] = state
	}
	state.Sent = pruneReplyDampingHits(state.Sent, now)
	if !state.PurposelessAt.IsZero() && now.Sub(state.PurposelessAt) > replyDampingPurposelessRetention {
		state.PurposelessAt = time.Time{}
	}
	return state
}

// recordReplyDampingSend 在回复成功发出后记一次。同一条消息只记一次。
func (r *Runtime) recordReplyDampingSend(event MessageEvent, now time.Time) {
	if !r.replyDampingApplies(event) {
		return
	}
	r.replyDamping.mu.Lock()
	defer r.replyDamping.mu.Unlock()
	state := r.replyDampingStateLocked(event, now, true)
	messageID := strings.TrimSpace(event.MessageID)
	if messageID != "" {
		for _, hit := range state.Sent {
			if hit.MessageID == messageID {
				return
			}
		}
	}
	state.Sent = append(state.Sent, replyDampingHit{MessageID: messageID, At: now})
}

// replyDensityForAudit 在回得很密时返回给审核的密度证据。
func (r *Runtime) replyDensityForAudit(event MessageEvent, now time.Time) (replyDensity, bool) {
	if !r.replyDampingApplies(event) {
		return replyDensity{}, false
	}
	r.replyDamping.mu.Lock()
	defer r.replyDamping.mu.Unlock()
	state := r.replyDampingStateLocked(event, now, false)
	if state == nil || len(state.Sent) < replyDampingDenseLimit {
		return replyDensity{}, false
	}
	return replyDensity{BotRepliesToSender: len(state.Sent), WindowMinutes: int(replyDampingWindow / time.Minute)}, true
}

// markReplyPurpose 记下审核对这一串来回的判断：判到空转就开始降欲望，判到有目的就解除。
// cause 说明这次是哪一种空转，只在 purposeless 为真时有意义。
func (r *Runtime) markReplyPurpose(event MessageEvent, purposeless bool, cause string, now time.Time) {
	if !r.replyDampingApplies(event) {
		return
	}
	r.replyDamping.mu.Lock()
	defer r.replyDamping.mu.Unlock()
	state := r.replyDampingStateLocked(event, now, true)
	if purposeless {
		state.PurposelessAt, state.PurposelessCause = now, cause
	} else {
		state.PurposelessAt, state.PurposelessCause = time.Time{}, ""
	}
}

// replyDampingNamed 是降欲望期间仍然会接的消息：私聊、@、引用机器人或叫了机器人的名字。
func (r *Runtime) replyDampingNamed(event MessageEvent, text string) bool {
	cfg := r.effectiveConfigForEvent(event)
	return event.Kind == EventKindPrivate || eventExplicitlyMentionsBot(event, cfg) ||
		explicitlyRepliesToBot(event, cfg) || len(matchedGroupAliases(event, cfg, text)) > 0
}

// replyDampingJudge 在决定回复之后、真正生成之前判断这条要不要因为降欲望而放掉。
// proactive 表示这一轮是主动接话（不是被直接触发）。
func (r *Runtime) replyDampingJudge(event MessageEvent, text string, proactive bool, now time.Time) replyDampingVerdict {
	if !r.replyDampingApplies(event) {
		return replyDampingVerdict{}
	}
	named := r.replyDampingNamed(event, text)
	r.replyDamping.mu.Lock()
	defer r.replyDamping.mu.Unlock()
	state := r.replyDampingStateLocked(event, now, false)
	if state == nil || state.PurposelessAt.IsZero() {
		return replyDampingVerdict{}
	}
	sent := len(state.Sent)
	cause := state.PurposelessCause
	if strings.TrimSpace(cause) == "" {
		cause = replyDampingCausePurposeless
	}
	prefix := fmt.Sprintf("回复欲望衰减：%d 分钟内已回复该账号 %d 次，%s", int(replyDampingWindow/time.Minute), sent, cause)
	if proactive {
		return replyDampingVerdict{Skip: true, Reason: prefix + "，暂不主动接它的话"}
	}
	if !named {
		return replyDampingVerdict{Skip: true, Reason: prefix + "，只接 @、引用或叫名字的消息"}
	}
	dense := replyDampingDenseLimit
	if sent < dense || len(state.Sent) == 0 {
		return replyDampingVerdict{}
	}
	cooldown := replyDampingCooldownStep * time.Duration(sent-dense+1)
	if since := now.Sub(state.Sent[len(state.Sent)-1].At); since < cooldown {
		return replyDampingVerdict{Skip: true, Reason: fmt.Sprintf("%s，距上次回复 %d 秒，冷却 %d 秒内不再接话", prefix, int(since/time.Second), int(cooldown/time.Second))}
	}
	return replyDampingVerdict{}
}

// replyDampingSkipsUnnamed 用在还要先调模型才能知道算不算触发的地方：已经在降欲望、
// 这条又没点名时直接放掉，省掉那次调用。
func (r *Runtime) replyDampingSkipsUnnamed(event MessageEvent, text string, now time.Time) (string, bool) {
	if r.replyDampingNamed(event, text) {
		return "", false
	}
	verdict := r.replyDampingJudge(event, text, false, now)
	return verdict.Reason, verdict.Skip
}

func (r *Runtime) resetReplyDampingUser(userID string) {
	userID = strings.TrimSpace(userID)
	if r == nil || userID == "" {
		return
	}
	suffix := "\x00" + userID
	r.replyDamping.mu.Lock()
	for key := range r.replyDamping.byKey {
		if strings.HasSuffix(key, suffix) {
			delete(r.replyDamping.byKey, key)
		}
	}
	r.replyDamping.mu.Unlock()
}
