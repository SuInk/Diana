// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/voicecall"
)

// 群聊通话：主人给本地群打电话，说的话转写后当成主人在群里说的一句（和打字一样
// 交给每个成员），成员的每条发言用这台机器人自己的语音合成插槽念出来，字幕和音频
// 都带着是谁说的。成员之间接话、刹车、停止讨论都和打字时一样。
//
// 语音识别用第一个配了语音识别的成员；没配语音合成的成员借第一个配了的成员的声音，
// 文字照常显示。
const (
	localRoomCallKind     = "call"
	localRoomCallSender   = "call"
	promptLocalRoomCall   = "你正在群语音通话里，主人和几台机器人都在线，你说的每个字都会被念出来：像群聊语音一样口语化，一次一两句，不用 Markdown、列表、表情符号、链接和括号里的动作描写；别人刚说过的不用重复。主人的话来自语音识别，可能有错别字或断句不准，按最合理的意思理解。"
	localRoomCallIDPrefix = "room-call-"
)

var promptLocalRoomCallSpec = tailSpec("local_room_call", "群语音通话", "主人在 WebUI 里给本地群打电话时注入：每个成员的回复都会被合成语音，要写成能直接念的口语。",
	promptLocalRoomCall)

// LocalRoomCall 是一通群聊电话在机器人这一侧的对接，实现 voicecall.SpeakerBackend。
type LocalRoomCall struct {
	runtime *Runtime
	room    LocalRoom
	// listener 是转写用的成员，voice 是没配语音合成的成员借用的声音。
	listener *VoiceCall
	voice    *VoiceCall
	started  time.Time
	ended    bool
}

// LocalRoomCallInfo 检查这个群能不能打电话：至少一个成员配了语音识别、一个配了语音合成。
func (r *Runtime) LocalRoomCallInfo(ctx context.Context, roomID string) VoiceCallInfo {
	room, listener, voice, err := r.localRoomCallMembers(ctx, roomID)
	info := VoiceCallInfo{BotID: room.ID, BotName: room.Name, Platform: PlatformLocal, STT: listener != "", TTS: voice != ""}
	if err != nil {
		info.Problem = err.Error()
	}
	return info
}

func (r *Runtime) localRoomCallMembers(ctx context.Context, roomID string) (LocalRoom, string, string, error) {
	hub := r.localRoomHub()
	hub.mu.Lock()
	r.ensureLocalRoomsLoadedLocked(ctx, hub)
	room, index := findLocalRoom(hub.rooms, strings.TrimSpace(roomID))
	hub.mu.Unlock()
	if index < 0 {
		return room, "", "", errors.New("群不存在")
	}
	listener, voice := "", ""
	for _, member := range room.Members {
		_, event, err := r.ownerChatEvent(member)
		if err != nil {
			continue
		}
		memberCtx := withModelConfigEvent(ctx, event)
		if listener == "" && r.mediaSlotConfigured(memberCtx, mediaSlotSTT) {
			listener = member
		}
		if voice == "" && r.mediaSlotConfigured(memberCtx, mediaSlotTTS) {
			voice = member
		}
	}
	switch {
	case listener == "" && voice == "":
		return room, "", "", errors.New("群里还没有哪台机器人配了语音识别和语音合成")
	case listener == "":
		return room, "", voice, errors.New("群里还没有哪台机器人配了语音识别")
	case voice == "":
		return room, listener, "", errors.New("群里还没有哪台机器人配了语音合成")
	}
	return room, listener, voice, nil
}

// NewLocalRoomCall 为一通群聊电话准备好机器人这一侧，调用方必须是已登录的 WebUI 管理员。
func (r *Runtime) NewLocalRoomCall(ctx context.Context, roomID string) (*LocalRoomCall, error) {
	room, listener, voice, err := r.localRoomCallMembers(ctx, roomID)
	if err != nil {
		return nil, err
	}
	call := &LocalRoomCall{runtime: r, room: room, started: time.Now()}
	if call.listener, err = r.localRoomCallVoice(listener); err != nil {
		return nil, err
	}
	if call.voice, err = r.localRoomCallVoice(voice); err != nil {
		return nil, err
	}
	r.setLocalRoomCalling(room.ID, 1)
	return call, nil
}

// localRoomCallVoice 借成员的主人私聊骨架去用它的语音插槽。
func (r *Runtime) localRoomCallVoice(member string) (*VoiceCall, error) {
	_, event, err := r.ownerChatEvent(member)
	if err != nil {
		return nil, err
	}
	return &VoiceCall{runtime: r, base: event}, nil
}

func (r *Runtime) setLocalRoomCalling(roomID string, delta int) {
	hub := r.localRoomHub()
	hub.mu.Lock()
	live := hub.liveLocked(roomID)
	live.calls += delta
	if live.calls < 0 {
		live.calls = 0
	}
	hub.mu.Unlock()
}

// localRoomCalling 报告这个群此刻有没有在通话：成员的回复要按通话口吻写。
func (r *Runtime) localRoomCalling(roomID string) bool {
	hub := r.localRoomHub()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	live := hub.live[roomID]
	return live != nil && live.calls > 0
}

// Finish 在挂断时调用一次：群里记一条通话记录。
func (c *LocalRoomCall) Finish() {
	if c == nil || c.ended {
		return
	}
	c.ended = true
	c.runtime.setLocalRoomCalling(c.room.ID, -1)
	duration := time.Since(c.started)
	if duration < callHistoryMinDuration {
		return
	}
	c.runtime.appendLocalRoomMessage(c.room, LocalRoomMessage{
		ID:         localRoomCallIDPrefix + fmt.Sprint(time.Now().UnixNano()),
		SenderID:   localRoomCallSender,
		SenderName: "群通话",
		Kind:       localRoomCallKind,
		Text:       callHistoryText(duration),
		At:         time.Now().Unix(),
	}, false)
}

// Transcribe 用转写成员的语音识别插槽。
func (c *LocalRoomCall) Transcribe(ctx context.Context, audio []byte, format string) (string, error) {
	return c.listener.Transcribe(ctx, audio, format)
}

// Reply 是单人通话的接口，群聊通话走 ReplyBySpeaker；留着满足 voicecall.Backend。
func (c *LocalRoomCall) Reply(ctx context.Context, text string, emit func(string)) error {
	return c.ReplyBySpeaker(ctx, text, func(_ voicecall.Speaker, message string) { emit(message) })
}

// Synthesize 同样只为满足 voicecall.Backend，用借来的声音念。
func (c *LocalRoomCall) Synthesize(ctx context.Context, text string, chunk func(mime string, data []byte) error) error {
	return c.voice.Synthesize(ctx, text, chunk)
}

// ReplyBySpeaker 把这句话当成主人在群里说的，讨论停下（没有成员在想）时返回。被主人
// 的下一句顶替或挂断时停止讨论：正在想的回复作废，不会再念到下一轮里。
func (c *LocalRoomCall) ReplyBySpeaker(ctx context.Context, text string, emit func(voicecall.Speaker, string)) error {
	_, err := c.runtime.LocalRoomSend(ctx, c.room.ID, text, func(msg LocalRoomMessage) {
		if msg.SenderID == localRoomOwnerSender || msg.Kind != "" || strings.TrimSpace(msg.Text) == "" {
			return
		}
		emit(voicecall.Speaker{ID: msg.SenderID, Name: msg.SenderName}, msg.Text)
	})
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		_, _ = c.runtime.StopLocalRoom(context.Background(), c.room.ID)
	}
	return nil
}

// SynthesizeAs 用发言成员自己的语音合成插槽念；它没配就借别人的声音。
func (c *LocalRoomCall) SynthesizeAs(ctx context.Context, speaker voicecall.Speaker, text string, chunk func(mime string, data []byte) error) error {
	voice := c.voice
	if speaker.ID != "" {
		if own, err := c.runtime.localRoomCallVoice(speaker.ID); err == nil && c.runtime.mediaSlotConfigured(withModelConfigEvent(ctx, own.base), mediaSlotTTS) {
			voice = own
		}
	}
	return voice.Synthesize(ctx, text, chunk)
}
