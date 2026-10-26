// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package voicecall

// 协议说明见 docs/voice-call.md。这里的类型就是线上的 JSON 文本帧，音频走二进制帧。

// 客户端发来的控制消息类型。
const (
	ClientStart     = "start"
	ClientCommit    = "commit"
	ClientInterrupt = "interrupt"
	ClientText      = "text"
	ClientHangup    = "hangup"
)

// 服务端下发的消息类型。
const (
	ServerReady       = "ready"
	ServerState       = "state"
	ServerSpeech      = "speech"
	ServerTranscript  = "transcript"
	ServerReply       = "reply"
	ServerAudioStart  = "audio_start"
	ServerAudioEnd    = "audio_end"
	ServerTurnEnd     = "turn_end"
	ServerInterrupted = "interrupted"
	ServerError       = "error"
)

// 上行音频格式。只有裸 PCM 能在服务端做静音分段；带封装的压缩格式由客户端自己
// 分句，说完一句发 commit。
const (
	FormatPCM16  = "pcm_s16le"
	FormatWebM   = "webm"
	FormatOgg    = "ogg"
	FormatWAV    = "wav"
	FormatMP4    = "mp4"
	SegmentByVAD = "server_vad"
	SegmentByCmd = "client"
)

// 通话状态。
const (
	StateListening = "listening"
	StateThinking  = "thinking"
	StateSpeaking  = "speaking"
)

// turn_end 的原因。
const (
	TurnDone        = "done"
	TurnInterrupted = "interrupted"
	TurnNoSpeech    = "no_speech"
	TurnNoReply     = "no_reply"
	TurnFailed      = "failed"
)

// ClientMessage 是客户端的文本帧。
type ClientMessage struct {
	Type string `json:"type"`
	// start
	Format       string `json:"format,omitempty"`
	SampleRate   int    `json:"sample_rate,omitempty"`
	Segmentation string `json:"segmentation,omitempty"`
	// BargeIn 为 nil 时开启：播报中检测到开口就打断。
	BargeIn *bool `json:"barge_in,omitempty"`
	// text
	Text string `json:"text,omitempty"`
}

// ServerMessage 是服务端的文本帧，按 Type 只填对应字段。
type ServerMessage struct {
	Type  string `json:"type"`
	Turn  int    `json:"turn,omitempty"`
	Seq   int    `json:"seq,omitempty"`
	Text  string `json:"text,omitempty"`
	State string `json:"state,omitempty"`
	// speech 消息里是 start / end。
	Event  string `json:"event,omitempty"`
	MIME   string `json:"mime,omitempty"`
	Reason string `json:"reason,omitempty"`
	Code   string `json:"code,omitempty"`
	Fatal  bool   `json:"fatal,omitempty"`
	Ready  *Ready `json:"ready,omitempty"`
}

// Ready 是 ready 消息的内容。
type Ready struct {
	CallID       string `json:"call_id"`
	BotID        string `json:"bot_id"`
	BotName      string `json:"bot_name,omitempty"`
	Format       string `json:"format"`
	SampleRate   int    `json:"sample_rate,omitempty"`
	Segmentation string `json:"segmentation"`
	BargeIn      bool   `json:"barge_in"`
}
