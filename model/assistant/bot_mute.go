// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"
)

// 机器人在群里被禁言时暂停回复。
//
// 禁言期间发什么都会被平台挡回来。以前这里不认识禁言：群消息照样过回复判断、
// 生成回复，发送失败后在群退避闸门里卡满失败窗口再丢弃——钱花了，一个字也发
// 不出去。现在知道被禁言了就只把消息记进上下文（历史、记忆、表达学习照常），
// 跳过回复判断和生成；解禁后从新消息开始正常回复，禁言期间的消息不补发。
//
// 禁言状态从三处来：
//   - 平台通知：OneBot 的 group_ban（机器人自己被禁言或全员禁言），Telegram 的
//     my_chat_member（机器人被限制发言）。实时，但重启或断线期间会错过。
//   - 发送失败后查一次：OneBot 用 get_group_member_info 查机器人自己的
//     shut_up_timestamp。补上错过通知的情况，代价是那一条回复已经生成过。
//   - 发送成功：说明没被禁言，直接清掉。
//
// 全员禁言挡不住管理员和群主，所以只有查到机器人是普通成员时才算数；查不到
// 时宁可不暂停，也不要让一个本来能说话的管理员机器人莫名沉默。
//
// 开关是 muted_reply_pause_enabled，机器人级默认开，分群可以覆盖。关掉后按原来的
// 方式处理：照常生成、照常重试。

var errBotMuted = errors.New("diana: bot is muted in this group")

const (
	botMuteNoticeSubType = "group_ban"
	// botMuteRecheckInterval 是没有到期时间的禁言（全员禁言、Telegram 无限期限制）
	// 多久重新核实一次。只在这个群有新消息时顺手查，不单独轮询。
	botMuteRecheckInterval = 10 * time.Minute
)

// botMuteState 是机器人在一个群里的禁言状态。个人禁言和全员禁言分开记：解除全员
// 禁言不代表个人禁言也到期了。
type botMuteState struct {
	PersonalUntil      time.Time
	PersonalIndefinite bool
	WholeGroup         bool
	CheckedAt          time.Time
}

func (s botMuteState) active(now time.Time) bool {
	return s.WholeGroup || s.PersonalIndefinite || now.Before(s.PersonalUntil)
}

// open 表示这个禁言没有可信的到期时间，需要定期重新核实。
func (s botMuteState) open() bool {
	return s.WholeGroup || s.PersonalIndefinite
}

func (s botMuteState) describe(now time.Time) string {
	switch {
	case s.PersonalIndefinite:
		return "机器人在本群被禁言，未给出解除时间"
	case now.Before(s.PersonalUntil):
		return "机器人在本群被禁言至 " + s.PersonalUntil.Local().Format("01-02 15:04")
	case s.WholeGroup:
		return "群处于全员禁言，机器人不是管理员"
	default:
		return ""
	}
}

func (cfg BotConfig) mutedReplyPauseEnabled() bool {
	return boolValue(cfg.MutedReplyPauseEnabled, true)
}

// 暂停回复期间三个要花钱的环节各自可开关：
//   - 语音转文字、图片识别默认照常：历史里的语音和图片有文字，解禁后上下文才完整；
//     关掉能省下禁言期间这部分费用。
//   - 回复判断默认不做：判断了也发不出去。打开后照常判断，结果记在事件页上
//     （「判断该回，但禁言中未发送」），用来看禁言期间错过了哪些该回的消息。
func (cfg BotConfig) mutedVoiceTranscriptionEnabled() bool {
	return boolValue(cfg.MutedVoiceTranscriptionEnabled, true)
}

func (cfg BotConfig) mutedImageDescriptionEnabled() bool {
	return boolValue(cfg.MutedImageDescriptionEnabled, true)
}

func (cfg BotConfig) mutedReplyJudgmentEnabled() bool {
	return boolValue(cfg.MutedReplyJudgmentEnabled, false)
}

// skipVoiceTranscriptionWhileMuted 在语音转写之前判断：被禁言、暂停开着、转写关着。
func (r *Runtime) skipVoiceTranscriptionWhileMuted(event MessageEvent) bool {
	if event.Kind != EventKindGroup {
		return false
	}
	if _, muted := r.botMuteFor(event, time.Now()); !muted {
		return false
	}
	return !r.effectiveConfigForEvent(event).mutedVoiceTranscriptionEnabled()
}

// botMuteFor 返回这个群当前有效的禁言状态。开关关掉或不是群时一律当没禁言。
func (r *Runtime) botMuteFor(event MessageEvent, now time.Time) (botMuteState, bool) {
	if strings.TrimSpace(event.GroupID) == "" || !r.effectiveConfigForEvent(event).mutedReplyPauseEnabled() {
		return botMuteState{}, false
	}
	key := r.outboundGroupKey(event)
	r.botMuteMu.RLock()
	state, ok := r.botMutes[key]
	r.botMuteMu.RUnlock()
	if !ok {
		return botMuteState{}, false
	}
	if !state.active(now) {
		r.botMuteMu.Lock()
		if current, still := r.botMutes[key]; still && !current.active(now) {
			delete(r.botMutes, key)
		}
		r.botMuteMu.Unlock()
		return botMuteState{}, false
	}
	return state, true
}

// setBotMute 用一次完整核实的结果替换现有状态。
func (r *Runtime) setBotMute(event MessageEvent, state botMuteState) {
	r.updateBotMute(event, func(current *botMuteState) { *current = state })
}

func (r *Runtime) updateBotMute(event MessageEvent, update func(*botMuteState)) {
	key := r.outboundGroupKey(event)
	now := time.Now()
	r.botMuteMu.Lock()
	defer r.botMuteMu.Unlock()
	state := r.botMutes[key]
	update(&state)
	state.CheckedAt = now
	if !state.active(now) {
		delete(r.botMutes, key)
		return
	}
	if r.botMutes == nil {
		r.botMutes = make(map[string]botMuteState)
	}
	r.botMutes[key] = state
}

// clearBotMute 在群里发送成功后调用：发得出去就说明没被禁言。
func (r *Runtime) clearBotMute(event MessageEvent) {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return
	}
	key := r.outboundGroupKey(event)
	r.botMuteMu.Lock()
	delete(r.botMutes, key)
	r.botMuteMu.Unlock()
}

// botMutedSendError 在发送前拦下已知被禁言的群：不再白发一次，也不进退避。
func (r *Runtime) botMutedSendError(event MessageEvent) error {
	if event.Kind != EventKindGroup {
		return nil
	}
	now := time.Now()
	state, muted := r.botMuteFor(event, now)
	if !muted {
		return nil
	}
	return &outboundSendError{
		GroupID:  strings.TrimSpace(event.GroupID),
		Cause:    errors.New("diana: " + state.describe(now)),
		BotMuted: true,
	}
}

// botMutedForReply 是回复判断前的检查：被禁言就跳过这一轮。没有到期时间的禁言
// 顺手在后台重新核实一次，免得错过解除通知后一直沉默。
func (r *Runtime) botMutedForReply(event MessageEvent) (string, bool) {
	if event.Kind != EventKindGroup {
		return "", false
	}
	now := time.Now()
	state, muted := r.botMuteFor(event, now)
	if !muted {
		return "", false
	}
	if state.open() && now.Sub(state.CheckedAt) >= botMuteRecheckInterval {
		// 先刷新 CheckedAt 占住这一轮，并发进来的消息不会各查一遍。
		r.updateBotMute(event, func(*botMuteState) {})
		go func() {
			defer recoverGoroutinePanic("bot_mute.go:recheck")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if probed, ok := r.probeBotMute(ctx, event); ok {
				r.setBotMute(event, probed)
			}
		}()
	}
	return state.describe(now), true
}

// checkBotMuteAfterSendFailure 在群发送失败后查一次是不是被禁言了。
func (r *Runtime) checkBotMuteAfterSendFailure(ctx context.Context, event MessageEvent) bool {
	if event.Kind != EventKindGroup || !r.effectiveConfigForEvent(event).mutedReplyPauseEnabled() {
		return false
	}
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	state, ok := r.probeBotMute(probeCtx, event)
	if !ok {
		return false
	}
	r.setBotMute(event, state)
	if state.active(time.Now()) {
		log.Printf("diana bot muted (detected after send failure): group=%s %s", event.GroupID, state.describe(time.Now()))
		return true
	}
	return false
}

// observeBotMuteNotice 处理禁言通知。返回 true 表示这是机器人自己的禁言变化，
// 调用方不必再往下走（这类通知的 user_id 就是机器人自己，会被当成自己发的消息丢掉）。
func (r *Runtime) observeBotMuteNotice(ctx context.Context, event MessageEvent) bool {
	if event.Kind != EventKindNotice || event.SubType != botMuteNoticeSubType || strings.TrimSpace(event.GroupID) == "" {
		return false
	}
	// 通知的 Kind 是 notice，状态按群消息的键存，发送和回复判断才查得到。
	target := event
	target.Kind = EventKindGroup
	data := noticeSegmentData(event)
	lifted := data["sub_type"] == "lift_ban"
	userID := strings.TrimSpace(event.UserID)
	if userID == "" || userID == "0" {
		// user_id 为 0 按 OneBot 惯例是全员禁言，但 SnowLuma 把被禁言者的 uid 解析
		// 不出 QQ 号时也会填 0——那可能只是某个普通成员被禁言。所以不信通知本身，
		// 开关都以实际查到的状态为准（群的 group_all_shut 加机器人自己的身份和
		// shut_up_timestamp）。查不到就保持原状。
		previous, _ := r.botMuteSnapshot(target)
		if probed, ok := r.probeBotMute(ctx, target); ok {
			r.setBotMute(target, probed)
			switch {
			case probed.WholeGroup && !previous.WholeGroup:
				log.Printf("diana bot muted: group=%s whole group", event.GroupID)
				r.recordBotMuteNotice(event, "群开启了全员禁言，机器人不是管理员，发不了言")
			case !probed.WholeGroup && previous.WholeGroup:
				r.recordBotMuteNotice(event, "群关闭了全员禁言")
			}
		}
		return false
	}
	if !r.isSelfMessage(event) {
		return false
	}
	now := time.Now()
	until, indefinite := botMuteUntil(data, event.Time, now)
	if lifted || (!indefinite && !now.Before(until)) {
		// duration 为 0 的 ban 在一些实现里就是解除。
		r.updateBotMute(target, func(s *botMuteState) { s.PersonalUntil, s.PersonalIndefinite = time.Time{}, false })
		log.Printf("diana bot unmuted: group=%s", event.GroupID)
		r.recordBotMuteNotice(event, "机器人被解除禁言"+botMuteOperatorSuffix(event))
		return true
	}
	r.updateBotMute(target, func(s *botMuteState) { s.PersonalUntil, s.PersonalIndefinite = until, indefinite })
	log.Printf("diana bot muted: group=%s until=%s indefinite=%v", event.GroupID, until.Format(time.RFC3339), indefinite)
	text := "机器人被禁言，未给出解除时间"
	if !indefinite {
		text = "机器人被禁言 " + formatMuteDuration(until.Sub(now)) + "，至 " + until.Local().Format("01-02 15:04")
	}
	r.recordBotMuteNotice(event, text+botMuteOperatorSuffix(event))
	return true
}

// recordBotMuteNotice 把禁言变化写进事件页的「通知」一栏。只看到一串「被禁言，
// 暂停回复」说不清是谁、什么时候禁的，这一条补上来龙去脉。
func (r *Runtime) recordBotMuteNotice(event MessageEvent, text string) {
	event.RawMessage = "[通知] " + text
	if event.Time <= 0 {
		event.Time = time.Now().Unix()
	}
	r.recordNoticeEvent(event)
}

func botMuteOperatorSuffix(event MessageEvent) string {
	operator := strings.TrimSpace(firstNonEmpty(event.OperatorID, noticeSegmentData(event)["operator_id"]))
	if operator == "" || operator == "0" {
		return ""
	}
	return "（操作人 " + operator + "）"
}

func formatMuteDuration(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		return "不到 1 分钟"
	}
	days, hours, mins := minutes/(24*60), minutes/60%24, minutes%60
	var parts []string
	if days > 0 {
		parts = append(parts, strconv.Itoa(days)+" 天")
	}
	if hours > 0 {
		parts = append(parts, strconv.Itoa(hours)+" 小时")
	}
	if mins > 0 && days == 0 {
		parts = append(parts, strconv.Itoa(mins)+" 分钟")
	}
	return strings.Join(parts, " ")
}

func (r *Runtime) botMuteSnapshot(event MessageEvent) (botMuteState, bool) {
	r.botMuteMu.RLock()
	defer r.botMuteMu.RUnlock()
	state, ok := r.botMutes[r.outboundGroupKey(event)]
	return state, ok
}

// botMuteUntil 从通知里算到期时间。OneBot 给 duration（秒），Telegram 给绝对的
// until（Unix 秒，0 表示永久）。
func botMuteUntil(data map[string]string, eventTime int64, now time.Time) (time.Time, bool) {
	if raw, ok := data["until"]; ok {
		until, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if until <= 0 {
			return time.Time{}, true
		}
		return time.Unix(until, 0), false
	}
	duration, _ := strconv.ParseInt(strings.TrimSpace(data["duration"]), 10, 64)
	if duration < 0 {
		return time.Time{}, true
	}
	start := now
	if eventTime > 0 && eventTime <= now.Unix() {
		start = time.Unix(eventTime, 0)
	}
	return start.Add(time.Duration(duration) * time.Second), false
}

func noticeSegmentData(event MessageEvent) map[string]string {
	for _, segment := range event.Segments {
		if segment.Type == "notice" && segment.Data != nil {
			return segment.Data
		}
	}
	return map[string]string{}
}

// botSelfAccount 是机器人在这个平台上的账号。
func (r *Runtime) botSelfAccount(event MessageEvent) string {
	if selfID := strings.TrimSpace(event.SelfID); selfID != "" {
		return selfID
	}
	if channel, _, err := r.outboundChannelForEvent(event); err == nil {
		if selfID := strings.TrimSpace(channel.Status().SelfID); selfID != "" {
			return selfID
		}
	}
	return strings.TrimSpace(r.profileConfig(event.ProfileID).BotAccount)
}

func (r *Runtime) botGroupMemberData(ctx context.Context, event MessageEvent) (map[string]any, bool) {
	_, platform, err := r.outboundChannelForEvent(event)
	if err != nil || !IsOneBotPlatform(platform) {
		return nil, false
	}
	selfID := r.botSelfAccount(event)
	if selfID == "" {
		return nil, false
	}
	callCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	data, err := r.callOneBotAPIForEvent(callCtx, event, "get_group_member_info", map[string]any{
		"group_id": oneBotIDParam(event.GroupID),
		"user_id":  oneBotIDParam(selfID),
		"no_cache": true,
	})
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

// probeBotMute 向 OneBot 查机器人在这个群里的禁言状态。第二个返回值为 false 表示
// 查不到（非 OneBot、接口失败），调用方应保持原状。
//
// 这两个字段都不在 OneBot 11 标准里，是各实现的扩展：
//   - 个人禁言看 get_group_member_info 的 shut_up_timestamp（禁言结束的 Unix 秒，
//     未禁言为 0）。常见实现都有。
//   - 全员禁言看 get_group_info 的 group_all_shut（常见实现：开启为 -1，
//     关闭为 0）。没有这个字段的实现当作没开，全员禁言就退回原来的处理方式。
func (r *Runtime) probeBotMute(ctx context.Context, event MessageEvent) (botMuteState, bool) {
	data, ok := r.botGroupMemberData(ctx, event)
	if !ok {
		return botMuteState{}, false
	}
	now := time.Now()
	state := botMuteState{CheckedAt: now}
	if until := int64(intFromAny(data["shut_up_timestamp"])); until > now.Unix() {
		state.PersonalUntil = time.Unix(until, 0)
	}
	role := NormalizeGroupRole(stringFromAny(data["role"]))
	if role != GroupRoleOwner && role != GroupRoleAdmin && role != "" {
		callCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		info, err := r.callOneBotAPIForEvent(callCtx, event, "get_group_info", map[string]any{
			"group_id": oneBotIDParam(event.GroupID),
			"no_cache": true,
		})
		cancel()
		if err == nil && intFromAny(info["group_all_shut"]) != 0 {
			state.WholeGroup = true
		}
	}
	return state, true
}

// telegramBotMuteEvent 把 my_chat_member 里机器人发言权限的变化翻成统一的禁言通知。
// 没有变化（或不是群）时返回空事件。
func telegramBotMuteEvent(update *telegramMemberUpdate, selfID string) MessageEvent {
	if update == nil || update.Chat.Type == "private" || update.Chat.Type == "channel" {
		return MessageEvent{}
	}
	before, after := update.Old.sendMuted(), update.New.sendMuted()
	if before == after {
		return MessageEvent{}
	}
	data := map[string]string{"notice_type": botMuteNoticeSubType, "sub_type": "lift_ban"}
	if after {
		data["sub_type"] = "ban"
		data["until"] = strconv.FormatInt(update.New.UntilDate, 10)
	}
	userID := strconv.FormatInt(update.New.User.ID, 10)
	return MessageEvent{
		Kind:        EventKindNotice,
		SubType:     botMuteNoticeSubType,
		Time:        time.Now().Unix(),
		SelfID:      firstNonEmpty(strings.TrimSpace(selfID), userID),
		UserID:      userID,
		GroupID:     strconv.FormatInt(update.Chat.ID, 10),
		GroupName:   strings.TrimSpace(update.Chat.Title),
		MessageType: "notice",
		Segments:    []MessageSegment{{Type: "notice", Data: data}},
	}
}

// sendMuted 表示这个成员在群里不能发消息（被限制但仍在群里）。
func (m telegramChatMember) sendMuted() bool {
	return m.Status == "restricted" && m.CanSendMessages != nil && !*m.CanSendMessages
}
