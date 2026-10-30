// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package qqcall 是 QQ 语音通话桥的线上协议：Diana 的插件和 SnowLuma 容器里的
// qqcall-agent（github.com/SuInk/snowluma-voice，那边有一份相同的 qqcall 包）之间
// 的约定，改动要两边一起改。设计见 docs/qq-voice-call.md。
//
// 一条 WebSocket：文本帧是 JSON 控制消息，二进制帧是 16kHz 单声道 s16le 裸 PCM。
// 上行（agent → Diana）是对方的声音，下行是机器人的声音；只在接通期间有音频。
package qqcall

// 音频格式固定，两边都不协商。
const (
	SampleRate = 16000
	// FrameBytes 是上行每帧的大小：20ms。
	FrameBytes = SampleRate * 2 / 50
)

// agent 发给 Diana 的消息类型。
const (
	// Hello 是连上后的第一条，带 agent 版本和当前状态。
	TypeHello = "hello"
	// Ringing 有人打进来，Name 是来电窗口上识别出的昵称。
	TypeRinging = "ringing"
	// Connected 已接通，PeerUIN 是 SnowLuma 日志里读到的对方 QQ 号，读不到时为空。
	TypeConnected = "connected"
	// Ended 通话窗口关了：对方挂断、拒接、超时或我们挂断都是它。
	TypeEnded = "ended"
	TypeError = "error"
)

// Diana 发给 agent 的消息类型。
const (
	TypeAnswer = "answer"
	TypeHangup = "hangup"
	// Flush 丢掉还没播完的下行音频，打断时用。
	TypeFlush = "flush"
)

// Message 是一条控制消息，按 Type 只填对应字段。
type Message struct {
	Type string `json:"type"`
	// Call 是 agent 给每通电话起的 ID，从响铃到挂断不变。
	Call    string `json:"call,omitempty"`
	Name    string `json:"name,omitempty"`
	PeerUIN string `json:"peer_uin,omitempty"`
	// State 只在 hello 里：idle / ringing / connected。
	State   string `json:"state,omitempty"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// 通话状态。
const (
	StateIdle      = "idle"
	StateRinging   = "ringing"
	StateConnected = "connected"
)
