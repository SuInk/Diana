// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package qqcall 是 QQ 语音通话桥的线上协议：Diana 的插件和 SnowLuma 容器里的
// qqcall-agent（另一个仓库里有一份相同的 qqcall 包）之间
// 的约定，改动要两边一起改。设计见 docs/qq-voice-call.md。
//
// 一条 WebSocket：文本帧是 JSON 控制消息，二进制帧是 16kHz 单声道 s16le 裸 PCM。
// 上行（agent → Diana）是对方的声音，下行是机器人的声音；只在接通期间有音频。
package qqcall

import "encoding/json"

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
	// Ringing 有人打进来，Name 是来电窗口上识别出的昵称，Text 是那块区域 OCR 的原文
	// （私聊是「XX 邀请你语音通话」，群通话是「XX 邀请您加入"群名"的语音通话」）。
	// 是不是来电、私聊还是群通话由 Diana 按 Text 判断；旧版 agent 没有 Text，只报私聊来电。
	TypeRinging = "ringing"
	// Connected 已接通，PeerUIN 是 SnowLuma 日志里读到的对方 QQ 号，读不到时为空。
	TypeConnected = "connected"
	// Outgoing 是自己拨出去的电话（窗口上没有「邀请你/您」）：多半是模型用桌面控制拨的。
	// agent 照样跟踪它，接通后报 connected 并收发声音，接不接管由 Diana 决定。
	TypeOutgoing = "outgoing"
	// Ended 通话窗口关了：对方挂断、拒接、超时或我们挂断都是它。
	TypeEnded = "ended"
	TypeError = "error"
)

// 桌面控制：Diana 把 SnowLuma 容器的屏幕接进「桌面控制」，让模型像操作电脑一样操作
// 容器里的 QQ（主动拨号等）。Diana 发 desktop（ID、Op、Params），agent 回
// desktop_result（同一个 ID，Data 或 Error）。Op 和 Params、Data 的格式就是 desktopctl
// 的 window.* 指令，agent 只管执行，授权和限流都在 Diana 这边。
const (
	TypeDesktop       = "desktop"
	TypeDesktopResult = "desktop_result"
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
	Call string `json:"call,omitempty"`
	Name string `json:"name,omitempty"`
	Text string `json:"text,omitempty"`
	// Image 只在 ringing 里：整个来电窗口的 PNG 截图（base64），给识图模型看。
	Image   string `json:"image,omitempty"`
	PeerUIN string `json:"peer_uin,omitempty"`
	// State 只在 hello 里：idle / ringing / connected。
	State   string `json:"state,omitempty"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
	// 下面几项只在 desktop / desktop_result 里。
	ID     string          `json:"id,omitempty"`
	Op     string          `json:"op,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// 通话状态。
const (
	StateIdle      = "idle"
	StateRinging   = "ringing"
	StateConnected = "connected"
)
