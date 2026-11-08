// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strconv"
	"strings"
)

// 聊天页要把通话里说的话和普通私聊分开：通话轮次归到挂断时那条通话记录下面，通话
// 还没挂断时页面显示「正在通话中」。
//
// 归组靠消息 ID 里带的通话 ID，不按时间段圈：通话期间主人在 QQ 上打的字、机器人主动
// 发的消息也落在那段时间里，按时间圈会被错收进通话。
//   - 主人说的一句：call-<通话ID>-<序号>（VoiceCall.Reply）
//   - 机器人回的一句：call-<通话ID>-out-<序号>（deliverToWebChat）
//   - 通话记录：local-call-<通话ID>（rememberVoiceCall）

// callHistoryIDPrefix 是通话记录的消息 ID 前缀。
const callHistoryIDPrefix = "local-call-"

// voiceCallReplyID 是通话里机器人回复的消息 ID。
func voiceCallReplyID(callID string, seq int64) string {
	return voiceCallMessagePrefix + callID + "-out-" + strconv.FormatInt(seq, 10)
}

// voiceCallIDFromTurn 从通话轮次的消息 ID 里取出通话 ID；不是通话轮次返回空。
func voiceCallIDFromTurn(messageID string) string {
	rest, ok := strings.CutPrefix(messageID, voiceCallMessagePrefix)
	if !ok {
		return ""
	}
	cut := strings.LastIndexByte(rest, '-')
	if cut <= 0 {
		return ""
	}
	if _, err := strconv.ParseInt(rest[cut+1:], 10, 64); err != nil {
		return ""
	}
	rest = strings.TrimSuffix(rest[:cut], "-out")
	return rest
}

// voiceCallIDFromRecord 从通话记录的消息 ID 里取出通话 ID。PR #1465 那批记录的 ID 是
// local-call-<uuid>，取出来的 uuid 对不上任何轮次，页面照样只显示一条记录。
func voiceCallIDFromRecord(messageID string) string {
	return strings.TrimPrefix(messageID, callHistoryIDPrefix)
}

// WebChatCall 是一通还没挂断的电话。
type WebChatCall struct {
	ID string `json:"id"`
	// StartedAt 是 Unix 秒，页面据此走实时通话时长。
	StartedAt int64 `json:"started_at"`
}

func (r *Runtime) trackVoiceCall(call *VoiceCall) {
	r.activeVoiceCalls.Store(call, struct{}{})
}

func (r *Runtime) untrackVoiceCall(call *VoiceCall) {
	r.activeVoiceCalls.Delete(call)
}

// activeVoiceCall 返回这台机器人最近接通、还没挂断的那通电话。断线等重连的通话
// （webui parkedVoiceCalls）也算：它还没记通话记录，轮次也还没归组。
func (r *Runtime) activeVoiceCall(profileID string) *WebChatCall {
	var latest *VoiceCall
	r.activeVoiceCalls.Range(func(key, _ any) bool {
		call := key.(*VoiceCall)
		if call.base.ProfileID == profileID && (latest == nil || call.started.After(latest.started)) {
			latest = call
		}
		return true
	})
	if latest == nil {
		return nil
	}
	return &WebChatCall{ID: latest.callID, StartedAt: latest.started.Unix()}
}
