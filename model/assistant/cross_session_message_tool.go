// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// 跨会话发送：把这一轮的内容送进另一条会话，而不是发在当前这条里。
//
// 在这之前，一轮回复只能发回它自己的会话——群里有人说「私聊发给我」，机器人只能
// 回一句「等会儿你来私聊敲我一下」，把本该现在做完的事推给对方再来一次。要发的
// 内容此刻已经在手里，缺的只是一个出口。
//
// 出口开在工具上而不是回复通道上：另一条会话有自己的准入、屏蔽和开关，谁能收到
// 必须当场判定，不能由模型在正文里自己决定。写死在这里的约束——
//
//  1. 目的地不能是当前这条会话。这一条让工具没法被拿来代替正常回复：想对眼前的人
//     说话就写正文，这个工具只通向别处。
//  2. 私聊默认只能发给当前说话的人。群里的第三个人没有开口，凭一句「顺便告诉他」
//     就收到机器人的私信，那是骚扰不是服务。
//  3. 指定别人、以及往某个群里发，都只有主人能要求。
//  4. 目的地本来就不该收到消息时不发：私聊准入挡住的人、群准入或群开关关掉的群、
//     屏蔽名单里的人，都不该因为换了条通道就绕过去。
//  5. 同一个目标不连着发、同一个来源会话十分钟内有总量上限。模型的工具循环失败
//     重试时，代价是对方那边多出一串重复的小作文。
const (
	dianaCrossSessionToolName = "cross_session_message"
	// crossSessionMaxRunes 是单条正文的上限。这个工具的典型用途就是「当前会话里
	// 放不下的长篇」，所以给得比提醒正文宽，但仍要有个头：再长的内容该发文件。
	crossSessionMaxRunes = 4000
	// crossSessionTargetCooldown 是同一个目标两条之间的最小间隔。它挡的不是人的
	// 正常追问（追问会先经过一轮完整对话，早就过了这点时间），而是模型在同一轮里
	// 把同一条内容连发两遍。
	crossSessionTargetCooldown = 15 * time.Second
	// crossSessionWindow 内同一个来源会话最多发 crossSessionLimit 条。
	crossSessionWindow = 10 * time.Minute
	crossSessionLimit  = 5
)

var errCrossSessionRateLimited = fmt.Errorf("刚给这个目标发过，或者这个会话最近发得太多了，这条先不发")

// crossSessionTarget 是一次投递的目的地：要么某个人的私聊，要么某个群。
type crossSessionTarget struct {
	// event 是按目的地造出来的投递事件。
	event MessageEvent
	// key 用于限流分桶，也用于日志里指认目的地。
	key string
	// label 是报给模型和日志看的人话。
	label string
}

// dianaCrossSessionTool 把当前这轮想说的话发进另一条会话。
type dianaCrossSessionTool struct {
	runtime *Runtime
	event   MessageEvent
	owner   bool
}

func newDianaCrossSessionTool(runtime *Runtime, event MessageEvent, owner bool) *dianaCrossSessionTool {
	return &dianaCrossSessionTool{runtime: runtime, event: event, owner: owner}
}

func (t *dianaCrossSessionTool) Name() string { return dianaCrossSessionToolName }

func (t *dianaCrossSessionTool) Description() string {
	base := "把内容发到另一条会话里去，不用等对方先来找你。" +
		"最常见的用法是主动发起私聊：群里有人说「私聊发给我」「私信我」「别发群里」时，直接用它把完整内容送进你和对方的私聊窗口；" +
		"只和一个人有关的长篇（说话风格分析、单独的复盘或整理）也可以这样发。" +
		"不要用的时候：对方没要求，也没有只对他一个人说的理由；只是想避开别人看见；当前会话里已经说过的话再发一遍。" +
		"目的地不能是当前这条会话——想对眼前的人说话就照常写正文，这个工具只通向别处。"
	if t.owner {
		base += " 主人还可以指定发给谁（user_id）或者发到哪个群（group_id），两者只能填一个；不填就是发给当前说话的人的私聊。"
	} else {
		base += " 只能发给当前说话的人，不填 user_id 即可；指定别人或指定群只有主人能要求，你填了会被拒绝。"
	}
	return base + " 发完在当前会话里用一句话交代一下就够，不要把正文再复述一遍，也不要把同一条内容发第二次。" +
		"结果里 delivered 为 false、pending 为 true 时，表示对方还不是好友、当场没发出去，内容已经存下：这时要照工具给的说明告诉对方来加好友，并说清好友请求要机器人主人同意，不要说成已经发过去了。" +
		"被拒绝就照实说，不要换别的工具绕过去。"
}

func (t *dianaCrossSessionTool) InputSchema() map[string]any {
	properties := map[string]any{
		"message": toolStringParam("要发出去的完整正文，最多 " + itoa(crossSessionMaxRunes) + " 个字符。" +
			"按你平时说话的样子写完整，不要写成「见私聊」这种占位；需要分成几条发时用 [diana-msg] 分隔。"),
	}
	if t.owner {
		properties["user_id"] = toolStringParam("私聊目标的账号 ID；省略时发给当前说话的人。必须取自 @ 的结构化信息、被引用消息的发送者或成员查询结果，不按昵称猜。与 group_id 二选一。")
		properties["group_id"] = toolStringParam("目标群的 ID：把内容发到这个群里，而不是发给某个人。只能填机器人自己在、且没有被关掉的群，不能是当前这个群。与 user_id 二选一。")
	}
	return toolObjectSchema([]string{"message"}, properties)
}

type dianaCrossSessionResult struct {
	OK bool `json:"ok"`
	// Delivered 区分「已经送到」和「存下了等加好友」。模型必须据此说不同的话，
	// 所以不能把两种情况都压成 ok:true 了事。
	Delivered bool   `json:"delivered"`
	Pending   bool   `json:"pending,omitempty"`
	Target    string `json:"target"`
	Runes     int    `json:"runes"`
	Message   string `json:"message"`
}

// crossSessionOutcome 描述一次投递的去向。
type crossSessionOutcome struct {
	// Pending 为真表示没能当场发出，内容已经存下，等加上好友再自动发。
	Pending bool
	// Note 是给模型看的补充说明，直接进工具结果。
	Note string
}

func (t *dianaCrossSessionTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("跨会话发送：运行时不可用")
	}
	message := strings.TrimSpace(configToolString(input, "message"))
	if message == "" {
		return "", fmt.Errorf("message 不能为空：要发什么，在这里写完整")
	}
	if count := len([]rune(message)); count > crossSessionMaxRunes {
		return "", fmt.Errorf("正文 %d 个字符，超过上限 %d，自己先精简或分几次发", count, crossSessionMaxRunes)
	}
	target, err := t.resolveTarget(ctx, input)
	if err != nil {
		return "", err
	}
	outcome, err := t.runtime.sendCrossSessionMessage(ctx, t.event, target, message)
	if err != nil {
		return "", err
	}
	// 送出去的撤不回来，存下的会自己发出去：两种都得让这一轮回复活着到达。标记
	// 之后它不会再被后续消息打断丢弃——那边收到了东西这边却没交代固然糟，存了
	// 一条等好友却没人告诉对方去加好友更糟：那条话就一直躺到过期。
	markExternalSideEffect(ctx)
	result := dianaCrossSessionResult{
		OK:        true,
		Delivered: !outcome.Pending,
		Pending:   outcome.Pending,
		Target:    target.label,
		Runes:     len([]rune(message)),
		Message: "已经发到" + target.label + "了。在当前会话里只说一句交代，" +
			"不要把正文复述出来，也不要再调一次这个工具。",
	}
	if outcome.Pending {
		result.Message = outcome.Note
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// resolveTarget 决定这条消息发去哪，并挡掉不该收到它的目的地。
func (t *dianaCrossSessionTool) resolveTarget(ctx context.Context, input map[string]any) (crossSessionTarget, error) {
	owner := t.runtime.relationshipPolicy(ctx, t.event).Owner
	groupID := strings.TrimSpace(configToolString(input, "group_id"))
	userID := strings.TrimSpace(configToolString(input, "user_id"))
	if groupID != "" && userID != "" {
		return crossSessionTarget{}, fmt.Errorf("user_id 和 group_id 只能填一个：要么发给某个人，要么发到某个群")
	}
	if groupID != "" {
		if !owner {
			return crossSessionTarget{}, fmt.Errorf("往群里发消息只有主人能要求")
		}
		return t.resolveGroupTarget(groupID)
	}
	if userID != "" && !owner {
		return crossSessionTarget{}, fmt.Errorf("只能发给当前说话的人；指定别人只有主人能要求")
	}
	return t.resolveUserTarget(ctx, userID)
}

func (t *dianaCrossSessionTool) resolveUserTarget(ctx context.Context, userID string) (crossSessionTarget, error) {
	speaker := strings.TrimSpace(t.event.UserID)
	target := firstNonEmpty(userID, speaker)
	if target == "" {
		return crossSessionTarget{}, fmt.Errorf("跨会话发送：不知道该发给谁")
	}
	if err := validatePlatformTargetID(target); err != nil {
		return crossSessionTarget{}, err
	}
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	if selfID := firstNonEmpty(strings.TrimSpace(t.event.SelfID), strings.TrimSpace(cfg.BotAccount)); selfID != "" && target == selfID {
		return crossSessionTarget{}, fmt.Errorf("不能给机器人自己发消息")
	}
	// 已经在和这个人私聊还调这个工具，就是把本该直接说的话绕一圈送回同一个窗口。
	if t.event.Kind == EventKindPrivate && target == speaker {
		return crossSessionTarget{}, fmt.Errorf("你正在和对方私聊，直接把话说出来就行，不用这个工具")
	}
	event := crossSessionPrivateEvent(t.event, target, time.Now())
	if !privateAdmissionAllowsConfig(t.runtime.effectiveConfigForEvent(event), event) {
		return crossSessionTarget{}, fmt.Errorf("这台机器人的私聊准入没放行这个人，不能给他发私聊")
	}
	label := "对方的私聊"
	if target != speaker {
		label = "账号 " + target + " 的私聊"
	}
	return crossSessionTarget{event: event, key: "private|" + target, label: label}, nil
}

func (t *dianaCrossSessionTool) resolveGroupTarget(groupID string) (crossSessionTarget, error) {
	if err := validatePlatformTargetID(groupID); err != nil {
		return crossSessionTarget{}, fmt.Errorf("目标群必须是群 ID，不能使用群名")
	}
	if t.event.Kind == EventKindGroup && groupID == strings.TrimSpace(t.event.GroupID) {
		return crossSessionTarget{}, fmt.Errorf("这就是当前这个群，直接把话说出来就行，不用这个工具")
	}
	event := crossSessionGroupEvent(t.event, groupID, time.Now())
	cfg := t.runtime.effectiveConfigForEvent(event)
	// 准入名单和群开关是「这台机器人在这个群里不出声」的意思，不该因为主人换了条
	// 通道就绕过去——真要在那个群说话，先把群打开。
	if !t.runtime.admitsGroupScope(cfg, event) {
		return crossSessionTarget{}, fmt.Errorf("群 %s 没有准入或已被关掉，先在控制台打开再发", groupID)
	}
	if missing, verified := t.runtime.groupMissingFromOneBot(event); verified && missing {
		return crossSessionTarget{}, fmt.Errorf("机器人不在群 %s 里", groupID)
	}
	return crossSessionTarget{event: event, key: "group|" + groupID, label: "群 " + groupID}, nil
}

// crossSessionPrivateEvent 按目标账号造一条私聊事件。它只描述「发到哪」，不带当前
// 这轮的消息体——带过去会让引用、@ 和媒体索引指向另一条会话里的消息。
func crossSessionPrivateEvent(source MessageEvent, target string, now time.Time) MessageEvent {
	return MessageEvent{
		Kind:             EventKindPrivate,
		Platform:         source.Platform,
		PlatformScope:    source.PlatformScope,
		ProfileID:        source.ProfileID,
		ContextNamespace: source.ContextNamespace,
		SelfID:           source.SelfID,
		SelfUsername:     source.SelfUsername,
		UserID:           target,
		Time:             now.Unix(),
	}
}

// crossSessionGroupEvent 按目标群造一条群聊事件。
func crossSessionGroupEvent(source MessageEvent, groupID string, now time.Time) MessageEvent {
	return MessageEvent{
		Kind:             EventKindGroup,
		Platform:         source.Platform,
		PlatformScope:    source.PlatformScope,
		GuildID:          source.GuildID,
		ProfileID:        source.ProfileID,
		ContextNamespace: source.ContextNamespace,
		SelfID:           source.SelfID,
		SelfUsername:     source.SelfUsername,
		GroupID:          groupID,
		Time:             now.Unix(),
	}
}

// sendCrossSessionMessage 把 message 投递到 target 那条会话。
//
// 私聊有三条路，按「对方是不是好友」分：是好友就普通私聊；不是好友就借共同群走
// 临时会话；临时会话也走不通时把内容存下来，等加上好友再自动发（见
// pending_direct_message.go）。发群没有这些分支。
func (r *Runtime) sendCrossSessionMessage(ctx context.Context, source MessageEvent, target crossSessionTarget, message string) (crossSessionOutcome, error) {
	now := time.Now()
	event := target.event
	platform, err := r.outboundPlatformForEvent(event)
	if err != nil {
		return crossSessionOutcome{}, fmt.Errorf("当前平台发不出跨会话消息：%w", err)
	}
	event.Platform = platform
	stranger := false
	if event.Kind == EventKindPrivate {
		if r.userBlocked(event) {
			return crossSessionOutcome{}, fmt.Errorf("对方在屏蔽名单里，不给他发私聊")
		}
		event, stranger = r.resolvePrivateDelivery(ctx, source, event)
	}
	if !r.claimCrossSessionSend(source, target.key, now) {
		r.recordCrossSessionSent(ctx, source, event, target, message, errCrossSessionRateLimited)
		return crossSessionOutcome{}, errCrossSessionRateLimited
	}
	// 不是好友、又没有共同群可借：临时会话根本开不了，不必先白发一次再失败。
	if stranger && event.tempSessionGroupID == "" {
		return r.parkOrFail(ctx, source, event, message, nil)
	}
	_, sendErr := r.sendDecorated(ctx, event, message, outboundDecoration{})
	r.recordCrossSessionSent(ctx, source, event, target, message, sendErr)
	if sendErr == nil {
		return crossSessionOutcome{}, nil
	}
	// 临时会话可以被对方的隐私设置关掉，也不是每个 OneBot 实现都支持。这一步失败
	// 不代表这条话没救了，存下来等加好友即可；对好友的失败是真失败。
	if stranger {
		return r.parkOrFail(ctx, source, event, message, sendErr)
	}
	// 平台自己的拒绝原因要原样带回去：Telegram 的「机器人不能主动找没聊过的人」
	// 和 QQ 的「不是好友」是两件事，含糊成一句「发不出去」就没法跟用户交代。
	return crossSessionOutcome{}, fmt.Errorf("没发出去：%w", sendErr)
}

// resolvePrivateDelivery 决定这条私聊走普通私聊还是临时会话，并报告对方是不是
// 已知的陌生人。
//
// QQ 给非好友发私聊必须借共同群走临时会话；对好友反过来，带上 group_id 会把消息
// 塞进临时会话那个独立的对话框。所以先问名册，问得出来才按答案分路；问不出来时
// 按普通私聊发，让平台自己给出拒绝原因，而不是替它猜一个——也正因为没问出来，
// 这时候不算「确认是陌生人」，失败了不去托管。
func (r *Runtime) resolvePrivateDelivery(ctx context.Context, source, event MessageEvent) (MessageEvent, bool) {
	friend, known := r.oneBotFriendship(ctx, event, event.UserID)
	if !known || friend {
		return event, false
	}
	if source.Kind == EventKindGroup {
		if shared := strings.TrimSpace(source.GroupID); shared != "" {
			event.tempSessionGroupID = shared
		}
	}
	return event, true
}

// parkOrFail 把发不出去的私聊交给托管；托管也不成才算真的失败。
func (r *Runtime) parkOrFail(ctx context.Context, source, event MessageEvent, message string, cause error) (crossSessionOutcome, error) {
	reason := "对方不是好友，这里也没有共同群可以借临时会话"
	if cause != nil {
		reason = "临时会话发不出去（" + cause.Error() + "）"
	}
	if err := r.parkPendingDirectMessage(ctx, source, event, message); err != nil {
		return crossSessionOutcome{}, fmt.Errorf("%s，内容也存不下来：%w", reason, err)
	}
	return crossSessionOutcome{
		Pending: true,
		Note: reason + "。内容已经存下来了：对方加上好友之后会自动发出去，" +
			itoa(int(pendingDirectMessageTTL/(24*time.Hour))) + " 天内有效。" +
			"现在要在当前会话里告诉对方来加机器人好友，并且说清楚好友请求要等机器人主人同意——" +
			"你自己不能通过好友请求。不要把正文复述出来，也不要再调一次这个工具。",
	}, nil
}

// claimCrossSessionSend 同时检查单个目标的冷却和来源会话总量，通过才占用额度。
func (r *Runtime) claimCrossSessionSend(source MessageEvent, targetKey string, now time.Time) bool {
	session := sessionKey(source)
	bucket := session + "|" + targetKey
	r.crossSessionMu.Lock()
	defer r.crossSessionMu.Unlock()
	if r.crossSessionLastSent == nil {
		r.crossSessionLastSent = map[string]time.Time{}
	}
	if r.crossSessionSessionSent == nil {
		r.crossSessionSessionSent = map[string][]time.Time{}
	}
	if last, ok := r.crossSessionLastSent[bucket]; ok && now.Sub(last) < crossSessionTargetCooldown {
		return false
	}
	recent := r.crossSessionSessionSent[session][:0]
	for _, at := range r.crossSessionSessionSent[session] {
		if now.Sub(at) < crossSessionWindow {
			recent = append(recent, at)
		}
	}
	if len(recent) >= crossSessionLimit {
		r.crossSessionSessionSent[session] = recent
		return false
	}
	r.crossSessionLastSent[bucket] = now
	r.crossSessionSessionSent[session] = append(recent, now)
	for key, at := range r.crossSessionLastSent {
		if now.Sub(at) > time.Hour {
			delete(r.crossSessionLastSent, key)
		}
	}
	return true
}

// recordCrossSessionSent 把这次跨会话投递记进事件流和运行日志。
//
// 它是机器人在一条会话里替另一条会话做的事：不记的话，那边那条消息凭空出现，
// 管理员既查不到是谁要求的，也查不到是从哪条会话发起的。
func (r *Runtime) recordCrossSessionSent(ctx context.Context, source, event MessageEvent, target crossSessionTarget, message string, err error) {
	if err == nil {
		r.record(EventRecord{
			At:        time.Now(),
			Kind:      event.Kind,
			Platform:  event.Platform,
			ProfileID: event.ProfileID,
			UserID:    event.UserID,
			GroupID:   event.GroupID,
			Text:      "[cross_session_message] " + sessionKey(source),
			Reply:     message,
			Handled:   true,
			Outcome:   "cross_session_message",
			Decision:  "replied",
			Reason:    "按要求把内容发到" + target.label,
		})
	}
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	entry := applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "cross_session_message_sent",
		Message: "已按要求把内容发到" + target.label,
		Actor:   oneBotEventActor(source),
		Target:  target.key,
		Metadata: map[string]any{
			"from_session": sessionKey(source),
			"target":       target.key,
			"temp_session": event.tempSessionGroupID != "",
			"runes":        len([]rune(message)),
			"preview":      truncateRunesFromStart(message, 200),
		},
		CreatedAt: time.Now(),
	}
	if err != nil {
		entry.Message = "跨会话消息没有发出"
		entry.Detail = err.Error()
		entry.Metadata["skipped"] = err == errCrossSessionRateLimited
		if err != errCrossSessionRateLimited {
			entry.Kind = applog.KindError
			entry.Level = applog.LevelError
		}
	}
	_ = writer.AppendLog(ctx, entry)
}
