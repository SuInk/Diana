// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package desktopctl 实现桌面控制的控制面：握手、鉴权、指令下发、应用白名单与人工接管。
//
// 它不自带桌面自动化。真正枚举窗口、截图的是用户机器上的本地执行器（例如 macOS
// helper），执行器通过本协议连到 Diana。Diana 只下发有限的只读指令并等回执。
//
// 与 model/browserctl 是两回事：浏览器控制管的是用户浏览器里的标签页；桌面控制
// 管的是操作系统窗口。两套白名单、两套工具、互不替代。
//
// 阶段 1 只开放只读能力（列窗口、截图）。点击、键盘、持久任务与控制台画面不在本包。
package desktopctl

import (
	"encoding/json"
	"errors"
	"strings"
)

// ProtocolVersion 是控制协议的主版本号。执行器握手时报自己的版本，不一致直接拒连。
const ProtocolVersion = 1

const (
	FrameHello    = "hello"
	FrameWelcome  = "welcome"
	FrameCommand  = "command"
	FrameResult   = "result"
	FrameWindows  = "windows"
	FrameTakeover = "takeover"
	FramePing     = "ping"
	FramePong     = "pong"
	FrameError    = "error"
)

// 指令名。阶段 1 只有只读两条；写操作名字预留，KnownOp 认但不由工具挂出。
const (
	OpWindowsList      = "windows.list"
	OpWindowScreenshot = "window.screenshot"
	// 以下为后续阶段预留，本阶段不下发。
	OpWindowClick = "window.click"
	OpWindowType  = "window.type"
)

const (
	CodeUnauthorized     = "unauthorized"
	CodeVersion          = "protocol_version"
	CodeNotConnected     = "not_connected"
	CodeDisabled         = "disabled"
	CodeTakeover         = "takeover"
	CodeAppDenied        = "app_denied"
	CodeWriteDisabled    = "write_disabled"
	CodeRateLimited      = "rate_limited"
	CodeTimeout          = "timeout"
	CodeUnsupportedOp    = "unsupported_op"
	CodeWindowUnknown    = "window_unknown"
	CodePermissionDenied = "permission_denied"
	CodeHelper           = "helper_error"
	CodeBadRequest       = "bad_request"
)

var readOnlyOps = map[string]bool{
	OpWindowsList:      true,
	OpWindowScreenshot: true,
}

var writeOps = map[string]bool{
	OpWindowClick: true,
	OpWindowType:  true,
}

// KnownOp 判断指令名是否在协议内。
func KnownOp(op string) bool {
	op = strings.TrimSpace(op)
	return readOnlyOps[op] || writeOps[op]
}

// IsWriteOp 判断一条指令是否属于写操作。
func IsWriteOp(op string) bool {
	return writeOps[strings.TrimSpace(op)]
}

// Frame 是控制面与执行器之间唯一的报文外壳。
type Frame struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Op   string `json:"op,omitempty"`
	// JobID / Observation 为后续持久任务与失效观察预留，阶段 1 可空。
	JobID       string          `json:"job_id,omitempty"`
	Observation int64           `json:"observation,omitempty"`
	Params      json.RawMessage `json:"params,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	Error       string          `json:"error,omitempty"`
	Code        string          `json:"code,omitempty"`
}

// Hello 是执行器的握手载荷。
type Hello struct {
	ProtocolVersion int    `json:"protocol_version"`
	Token           string `json:"token,omitempty"`
	// HelperID 唯一标识这台执行器；令牌首次使用时会钉在上面。
	HelperID     string   `json:"helper_id"`
	HelperName   string   `json:"helper_name,omitempty"`
	Platform     string   `json:"platform,omitempty"`
	Label        string   `json:"label,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Welcome 是控制面的握手回执。
type Welcome struct {
	ProtocolVersion  int          `json:"protocol_version"`
	ConnectionID     string       `json:"connection_id"`
	ServerVersion    string       `json:"server_version,omitempty"`
	Policy           PolicyDigest `json:"policy"`
	HeartbeatSeconds int          `json:"heartbeat_seconds"`
}

// WindowInfo 是一个桌面窗口。AppName 给人看；BundleID 与 ID 做策略与定位。
type WindowInfo struct {
	ID       string `json:"id"`
	AppName  string `json:"app_name,omitempty"`
	BundleID string `json:"bundle_id,omitempty"`
	Title    string `json:"title,omitempty"`
	Active   bool   `json:"active,omitempty"`
}

// WindowsPayload 是窗口清单帧的载荷。
type WindowsPayload struct {
	Windows []WindowInfo `json:"windows"`
}

// TakeoverPayload 是人工接管状态帧的载荷。
type TakeoverPayload struct {
	Active bool   `json:"active"`
	Reason string `json:"reason,omitempty"`
}

// ScreenshotPayload 是截图回执（PNG base64）。
type ScreenshotPayload struct {
	WindowID string `json:"window_id"`
	AppName  string `json:"app_name,omitempty"`
	BundleID string `json:"bundle_id,omitempty"`
	Title    string `json:"title,omitempty"`
	Mime     string `json:"mime,omitempty"`
	Data     string `json:"data"` // base64 PNG
	// Observation 预留：后续动作用它判断现场是否过期。
	Observation int64 `json:"observation,omitempty"`
}

// ErrProtocol 表示对端发来的帧不符合协议。
var ErrProtocol = errors.New("desktop control: 协议错误")

// EncodeFrame 把一帧序列化成待发送的字节。
func EncodeFrame(frame Frame) ([]byte, error) {
	return json.Marshal(frame)
}

// DecodeFrame 解析一帧，并校验帧类型非空。
func DecodeFrame(payload []byte) (Frame, error) {
	var frame Frame
	if err := json.Unmarshal(payload, &frame); err != nil {
		return Frame{}, err
	}
	if strings.TrimSpace(frame.Type) == "" {
		return Frame{}, ErrProtocol
	}
	return frame, nil
}

func rawJSON(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return body
}
