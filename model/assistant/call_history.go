// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 通话记录进私聊历史。通话里说的每句话本来就按主人私聊记着，但挂断后分不清哪段是
// 电话里说的、打了多久；主人问「刚才那通电话聊了多久」，模型也只能猜。
//
// 挂断时记一条带 callHistorySubType 标记的私聊事件，正文是中性的「[语音通话] 通话
// 3 分 12 秒」。它和戳一戳一样是旁白，不是任何一方的发言：不算 assistant、不拿去
// 引用或撤回（见 isNarrationHistoryEvent）；给模型看时由 callHistoryPromptText
// 渲染成旁白；聊天页显示成居中的提示（WebChatMessage.Kind == "call"）。
const callHistorySubType = "voice_call"

// callHistoryMinDuration 以下的不记：连一句话都没来得及说就挂了。
const callHistoryMinDuration = time.Second

func isCallHistoryEvent(event MessageEvent) bool {
	return event.SubType == callHistorySubType && event.Kind == EventKindPrivate
}

// isNarrationHistoryEvent 是历史里没有正文的旁白：戳一戳、通话记录、回复的赞踩反馈。它们的消息 ID 是
// 本地编的，平台上没有这条消息，也不是谁说的话。
func isNarrationHistoryEvent(event MessageEvent) bool {
	return isPokeHistoryEvent(event) || isCallHistoryEvent(event) || isQQFeedbackHistoryEvent(event)
}

// callHistoryText 是通话记录的正文。
func callHistoryText(duration time.Duration) string {
	return "[语音通话] 通话 " + formatCallDuration(duration)
}

func formatCallDuration(duration time.Duration) string {
	seconds := int(duration.Round(time.Second) / time.Second)
	switch {
	case seconds < 60:
		return fmt.Sprintf("%d 秒", max(seconds, 1))
	case seconds < 3600:
		return fmt.Sprintf("%d 分 %d 秒", seconds/60, seconds%60)
	default:
		return fmt.Sprintf("%d 小时 %d 分", seconds/3600, seconds%3600/60)
	}
}

// callHistoryPromptText 给模型看的那一行：标明是通话记录，不是谁说的话。
func callHistoryPromptText(event MessageEvent) string {
	text := strings.TrimSpace(strings.TrimPrefix(historyPlainText(event), "[语音通话]"))
	return historyLinePrefix(event) + "[语音通话记录，不是谁说的话] 你和主人" + text + "，期间说的话就是这条之前的那几轮"
}

// rememberVoiceCall 在主人私聊里记一条通话记录。base 是通话用的主人私聊事件。
// callID 写进记录的消息 ID，聊天页靠它把通话里的轮次归到这条记录下（见 web_chat_call.go）。
func (r *Runtime) rememberVoiceCall(base MessageEvent, callID string, started, ended time.Time) {
	duration := ended.Sub(started)
	if duration < callHistoryMinDuration {
		return
	}
	text := callHistoryText(duration)
	r.remember(r.bindInboundEventIdentity(MessageEvent{
		Platform:         base.Platform,
		ProfileID:        base.ProfileID,
		ContextNamespace: base.ContextNamespace,
		Kind:             EventKindPrivate,
		MessageType:      string(EventKindPrivate),
		SubType:          callHistorySubType,
		Time:             ended.Unix(),
		SelfID:           base.SelfID,
		UserID:           base.UserID,
		SenderName:       base.SenderName,
		MessageID:        callHistoryIDPrefix + firstNonEmpty(strings.TrimSpace(callID), uuid.NewString()),
		RawMessage:       text,
		Segments:         []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}))
}
