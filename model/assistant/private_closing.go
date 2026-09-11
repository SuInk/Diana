// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// privateClosingAuditWindow 决定私聊里什么时候值得为「是不是在收尾」多判一项。
	//
	// 取 3 分钟的依据是那次 57 条私聊的实测节奏：机器人发完一条之后，对方的下一条
	// 最长只隔 11 秒；机器人两条回复之间最长 48 秒（20:07:02 → 20:07:50，含生成
	// 耗时）。也就是说一次仍在进行中的来回，距离上一条机器人回复几乎不会超过一分钟。
	// 3 分钟留了约 4 倍余量，同时保证真正的「新开一段对话」——私聊沉默几分钟后的
	// 第一句——一分钱都不多花：那种场景本来也该照常回答。
	privateClosingAuditWindow = 3 * time.Minute
	// privateClosingRetention 是收尾计数的存活时间。它跟着对话本身过期：隔了
	// 半小时再说话就是新的一段，之前道过几次别不该再算数。
	privateClosingRetention = 30 * time.Minute
	// privateClosingAuditConfidence 和拒答计数用的是同一道门槛：只有高置信结论
	// 才拿来改变「发不发」。
	privateClosingAuditConfidence = 0.90
)

// errConversationClosing 表示对方在收尾，而这条候选回复只是又一句告别。
// 判断发生在发送之前，所以这条根本不会发出去。
var errConversationClosing = errors.New("diana: conversation already closing")

// errStopRequested 表示对方明确要求不要再回。和收尾不同，它不给宽限次数。
var errStopRequested = errors.New("diana: recipient asked the bot to stop replying")

// privateClosingState 记录一个私聊会话已经互相道别了几轮。
//
// 「一轮」的定义是：这一条入站被判为收尾，而机器人为它准备的回复也只是另一句
// 告别。两个条件都由同一次发送前审核看着「原消息 + 候选回复」判出来——只看入站
// 会把「拜拜，对了刚才那个链接你看了吗」也算进去，只看回复则分不清是谁在收尾。
type privateClosingState struct {
	userID    string
	count     int
	updatedAt time.Time
}

func privateClosingKey(event MessageEvent) string {
	if event.Kind != EventKindPrivate {
		return ""
	}
	session := strings.TrimSpace(sessionKey(event))
	if session == "" {
		return ""
	}
	return strings.TrimSpace(event.ProfileID) + "|" + strings.TrimSpace(event.Platform) + "|" + session
}

func privateClosingGrace(cfg BotConfig) int {
	if cfg.PrivateClosingGrace > 0 {
		return cfg.PrivateClosingGrace
	}
	return defaultPrivateClosingGrace
}

// privateClosingCount 读出当前累计轮数，顺手清掉过期的会话。
func (r *Runtime) privateClosingCount(event MessageEvent, now time.Time) int {
	key := privateClosingKey(event)
	if r == nil || key == "" {
		return 0
	}
	r.privateClosingMu.Lock()
	defer r.privateClosingMu.Unlock()
	r.prunePrivateClosingLocked(now)
	state, ok := r.privateClosingBySession[key]
	if !ok {
		return 0
	}
	return state.count
}

// notePrivateClosingExchange 记下「这一轮告别照常发出去了」，返回累计轮数。
func (r *Runtime) notePrivateClosingExchange(event MessageEvent, now time.Time) int {
	key := privateClosingKey(event)
	if r == nil || key == "" {
		return 0
	}
	r.privateClosingMu.Lock()
	defer r.privateClosingMu.Unlock()
	r.prunePrivateClosingLocked(now)
	if r.privateClosingBySession == nil {
		r.privateClosingBySession = map[string]*privateClosingState{}
	}
	state, ok := r.privateClosingBySession[key]
	if !ok {
		state = &privateClosingState{userID: strings.TrimSpace(event.UserID)}
		r.privateClosingBySession[key] = state
	}
	state.count++
	state.updatedAt = now
	return state.count
}

// notePrivateClosingSilence 让收尾计数从「模型自己闭嘴了」这件事上学到东西。
//
// 静默那一轮没有审核可看：模型在生成阶段就决定不说话，发送前审核根本没跑，
// 计数器不可能从审核结论里知道对方是不是在收尾。但这一轮的终态和兜底拦下来的
// 那一轮完全一样——机器人没有再补一句告别。所以按同一个终态记：把计数顶到宽限
// 上限（不是加一），这段对话此后就处在「已经道过别」的状态，对方再来一句纯告别
// 时，兜底会直接拦住模型这次生成的回复。
//
// 顶到上限而不是累加，是因为静默这一轮机器人没说话，它不是一次互相道别，只是
// 宣告这段对话在机器人这边已经结束。对方说了实质内容时，审核照旧把计数清零
// （见 privateClosingVerdict），机器人也就照常接着聊；隔了 privateClosingRetention
// 之后计数自然过期。
func (r *Runtime) notePrivateClosingSilence(event MessageEvent, cfg BotConfig, now time.Time) int {
	key := privateClosingKey(event)
	if r == nil || key == "" {
		return 0
	}
	grace := privateClosingGrace(cfg)
	r.privateClosingMu.Lock()
	defer r.privateClosingMu.Unlock()
	r.prunePrivateClosingLocked(now)
	if r.privateClosingBySession == nil {
		r.privateClosingBySession = map[string]*privateClosingState{}
	}
	state, ok := r.privateClosingBySession[key]
	if !ok {
		state = &privateClosingState{userID: strings.TrimSpace(event.UserID)}
		r.privateClosingBySession[key] = state
	}
	if state.count < grace {
		state.count = grace
	}
	state.updatedAt = now
	return state.count
}

// resetPrivateClosing 在对方说了实质内容之后把计数清零：一句「对了还有件事」
// 就说明这段对话又活过来了，之前道过几次别不该再往后累。
func (r *Runtime) resetPrivateClosing(event MessageEvent) {
	key := privateClosingKey(event)
	if r == nil || key == "" {
		return
	}
	r.privateClosingMu.Lock()
	defer r.privateClosingMu.Unlock()
	delete(r.privateClosingBySession, key)
}

// resetPrivateClosingUser 在主人解除响应限制时把这个账号的收尾计数一并清掉。
// 「解除」就该是干净的一笔勾销，不能留着计数让机器人下一句又闭嘴。
func (r *Runtime) resetPrivateClosingUser(userID string) {
	userID = strings.TrimSpace(userID)
	if r == nil || userID == "" {
		return
	}
	r.privateClosingMu.Lock()
	defer r.privateClosingMu.Unlock()
	for key, state := range r.privateClosingBySession {
		if state != nil && state.userID == userID {
			delete(r.privateClosingBySession, key)
		}
	}
}

func (r *Runtime) prunePrivateClosingLocked(now time.Time) {
	for key, state := range r.privateClosingBySession {
		if state == nil || now.Sub(state.updatedAt) > privateClosingRetention {
			delete(r.privateClosingBySession, key)
		}
	}
}

// privateFollowUpAuditDue 判断这条私聊回复值不值得顺带多判几项（收尾，以及
// 私聊里的空转）。
//
// 只有「机器人刚刚在这个会话里说过话」才判：私聊里的第一句、或者隔了很久之后的
// 第一句，本来就该正常回答——那既不可能是收尾的第 N 轮，也不可能是空转，为它
// 多带几个字段只是白花钱。
func (r *Runtime) privateFollowUpAuditDue(event MessageEvent, now time.Time) bool {
	if r == nil || event.Kind != EventKindPrivate {
		return false
	}
	last, ok := r.lastPrivateBotReplyAt(event)
	if !ok {
		return false
	}
	gap := now.Sub(last)
	return gap >= 0 && gap <= privateClosingAuditWindow
}

// lastPrivateBotReplyAt 找这个会话里机器人最后一次说话的时间。历史里既有
// botReply 这类运行时标记，也有从对端回读到的自己发的消息，两种都算。
func (r *Runtime) lastPrivateBotReplyAt(event MessageEvent) (time.Time, bool) {
	cfg := r.effectiveConfigForEvent(event)
	botID := firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(event.SelfID))
	history, _ := r.sessionContextHistory(event)
	for i := len(history) - 1; i >= 0; i-- {
		item := history[i]
		if strings.TrimSpace(item.MessageID) != "" && item.MessageID == event.MessageID {
			continue
		}
		if strings.TrimSpace(item.botReply) == "" && !assistantHistoryEvent(item, botID) {
			continue
		}
		if item.Time <= 0 {
			continue
		}
		return time.Unix(item.Time, 0), true
	}
	return time.Time{}, false
}

// privateClosingVerdict 把审核结论里的收尾部分变成「这条还发不发」。
//
// 明确要求停止当场生效，不看宽限次数；单纯的收尾则先答满 grace 轮，第 grace+1
// 轮才收声。计数只在真的发出去的那些轮上累加——被拦下的那一轮机器人没说话，
// 算不上一次互相道别。
func (r *Runtime) privateClosingVerdict(event MessageEvent, decision proactiveReplyQualityDecision, cfg BotConfig, now time.Time) error {
	if decision.stopCounts() {
		r.resetPrivateClosing(event)
		return &conversationClosedError{err: errStopRequested, reason: privateStopRequestedReason(decision)}
	}
	if !decision.closingCounts() {
		// 说了实质内容：这段对话还没结束，计数归零。
		r.resetPrivateClosing(event)
		return nil
	}
	grace := privateClosingGrace(cfg)
	if r.privateClosingCount(event, now) >= grace {
		return &conversationClosedError{err: errConversationClosing, reason: privateClosingReason(grace)}
	}
	r.notePrivateClosingExchange(event, now)
	return nil
}

func privateClosingReason(grace int) string {
	return fmt.Sprintf("对方在收尾，已互相道别 %d 次，不再追加", grace)
}

func privateStopRequestedReason(decision proactiveReplyQualityDecision) string {
	reason := strings.TrimSpace(decision.ClosingReason)
	if reason == "" {
		return "对方明确要求不要再回复，已停止接话"
	}
	return "对方明确要求不要再回复，已停止接话：" + reason
}

// conversationClosedError 带着给事件详情看的中文理由，同时保留可以 errors.Is
// 的哨兵，好让运行时按收尾还是叫停分出两个 outcome。
type conversationClosedError struct {
	err    error
	reason string
}

func (e *conversationClosedError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.reason) == "" {
		return e.err.Error()
	}
	return e.reason
}

func (e *conversationClosedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}
