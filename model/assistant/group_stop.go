// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// 群里有人叫停之后的静默窗口。
//
// 线上：糖 @ 机器人「闭嘴」，机器人回了「好的，我闭嘴汪」，6 秒后又把一条给 Winter
// 的回复发了出去——那条是接话评分放行的，生成时已经看到了「闭嘴」，正文里还写着
// 「糖宝嫌吵我都闭嘴了」。之后的接话评分倒是都以「刚被要求闭嘴」为由沉默，但这只靠
// 那句「闭嘴」还留在最近 20 条上下文里，群里再聊一会儿就滑出去了。
//
// 所以叫停记成状态，不靠上下文窗口。发送前审核在私聊里本来就判 stop_requested，群里
// 被 @、引用、点名的消息也搭同一次调用判一下；判出叫停就给本群记一个窗口。窗口内
// 接话评分直接跳过不跑模型，已经在路上的接话回复发送前丢掉；@、引用、点名照答——
// 「闭嘴」说的是别插话，不是不理人。也不套私聊那套「暂停响应这个账号半小时」：群里
// 叫停的人下一句 @ 它，它照样回。

// groupStopWindow 是叫停后接话暂停的时长。「闭嘴」说的是眼下这阵子，不是今天。
const groupStopWindow = 10 * time.Minute

type groupStopState struct {
	UserID string
	At     time.Time
	Until  time.Time
	Reason string
}

func groupStopKey(event MessageEvent) string {
	if event.Kind != EventKindGroup {
		return ""
	}
	session := strings.TrimSpace(sessionKey(event))
	if session == "" {
		return ""
	}
	return strings.TrimSpace(event.ProfileID) + "|" + strings.TrimSpace(event.Platform) + "|" + session
}

// noteGroupStop 给这个群记一个静默窗口。已经有一个更晚到期的就不动它。
func (r *Runtime) noteGroupStop(event MessageEvent, reason string, now time.Time) (groupStopState, bool) {
	key := groupStopKey(event)
	if r == nil || key == "" {
		return groupStopState{}, false
	}
	r.groupStopMu.Lock()
	defer r.groupStopMu.Unlock()
	if r.groupStopBySession == nil {
		r.groupStopBySession = map[string]groupStopState{}
	}
	for existing, state := range r.groupStopBySession {
		if !state.Until.After(now) {
			delete(r.groupStopBySession, existing)
		}
	}
	if existing, ok := r.groupStopBySession[key]; ok && existing.Until.After(now.Add(groupStopWindow)) {
		return existing, false
	}
	state := groupStopState{UserID: strings.TrimSpace(event.UserID), At: now, Until: now.Add(groupStopWindow), Reason: strings.TrimSpace(reason)}
	r.groupStopBySession[key] = state
	return state, true
}

func (r *Runtime) activeGroupStop(event MessageEvent, now time.Time) (groupStopState, bool) {
	key := groupStopKey(event)
	if r == nil || key == "" {
		return groupStopState{}, false
	}
	r.groupStopMu.Lock()
	defer r.groupStopMu.Unlock()
	state, ok := r.groupStopBySession[key]
	if !ok {
		return groupStopState{}, false
	}
	if !state.Until.After(now) {
		delete(r.groupStopBySession, key)
		return groupStopState{}, false
	}
	return state, true
}

// groupStopAuditDue 报告这条群消息值不值得为「是不是在叫停」多判一项：它得是冲着
// 机器人来的（@、引用、点名），而且机器人刚在这个群里说过话——没说话就谈不上叫停，
// 和私聊收尾判断用同一个时间门槛。
func (r *Runtime) groupStopAuditDue(event MessageEvent, cfg BotConfig, text string, now time.Time) bool {
	if r == nil || event.Kind != EventKindGroup {
		return false
	}
	if !eventDirectlyMentionsBot(event, cfg) && !eventRepliesToBot(event, cfg) && len(matchedGroupAliases(event, cfg, text)) == 0 {
		return false
	}
	last, ok := r.lastBotReplyAt(event)
	if !ok {
		return false
	}
	gap := now.Sub(last)
	return gap >= 0 && gap <= privateClosingAuditWindow
}

// applyGroupStopVerdict 把审核里的叫停结论记成窗口。这条回复本身照发：被 @ 着说
// 「闭嘴」，回一句「好的」是正常的，之后的接话才是要停的。
func (r *Runtime) applyGroupStopVerdict(ctx context.Context, event MessageEvent, decision proactiveReplyQualityDecision, now time.Time) {
	if !decision.stopCounts() {
		return
	}
	state, activated := r.noteGroupStop(event, decision.ClosingReason, now)
	if !activated {
		return
	}
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	logCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer stop()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind: applog.KindOperation, Level: applog.LevelInfo,
		Action: "group_stop_requested", Message: "群里有人要求闭嘴，接话暂停", Actor: oneBotEventActor(event), Target: event.MessageID,
		Metadata: map[string]any{
			"group_id":     event.GroupID,
			"user_id":      event.UserID,
			"until":        state.Until.Format(time.RFC3339),
			"confidence":   decision.ClosingConfidence,
			"model_reason": decision.ClosingReason,
		},
	})
}

func groupStopRoutingReason(state groupStopState, now time.Time) string {
	minutes := int(now.Sub(state.At).Minutes())
	return fmt.Sprintf("群里 %d 分钟前有人要求闭嘴，接话暂停到 %s，只回 @、引用和点名", minutes, state.Until.Local().Format("15:04"))
}

// groupStopDropsReply 拦下叫停之后才轮到发送的接话回复。接话评分在窗口里本来就不会
// 放行，能走到这里的是叫停到达时已经在生成的那一条——「闭嘴」之后 6 秒又说一段，
// 对方看到的就是不听话。冲着机器人来的回复不在此列。
func (r *Runtime) groupStopDropsReply(event MessageEvent, proactive bool, now time.Time) error {
	if event.Kind != EventKindGroup || !proactive {
		return nil
	}
	state, ok := r.activeGroupStop(event, now)
	if !ok {
		return nil
	}
	return &conversationClosedError{err: errStopRequested, reason: "群里有人要求闭嘴，已丢弃这条接话回复：" + firstNonEmpty(state.Reason, "对方明确要求不要再回复")}
}
