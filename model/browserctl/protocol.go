// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package browserctl 实现浏览器控制扩展的控制面：握手、鉴权、指令下发、
// 站点与读写权限隔离，以及人工接管。
//
// 它不自带浏览器。真正执行页面操作的是用户自己安装、自己授权的浏览器扩展，
// 扩展通过 WebSocket 反向连到 Diana，Diana 只下发有限的几条指令并等回执。
// 这样做的取舍很明确：
//
//   - 页面在用户日常浏览器里跑，登录态归用户，Diana 不接触 Cookie 和密码。
//   - 能做什么由本包的 Policy 说了算，默认只读、站点白名单为空，
//     也就是装上扩展之后仍然一个站点都碰不到，必须逐条授权。
//   - 任何时候用户都能在扩展里按下接管，控制面立刻停止下发指令。
//
// 本包不做、也不打算做规避站点风控的事：没有指纹伪装、没有验证码绕过、
// 没有隐藏自动化痕迹的开关。它的用途是让用户把自己有权访问的页面交给 Diana 读写。
package browserctl

import (
	"encoding/json"
	"errors"
	"strings"
)

// ProtocolVersion 是控制协议的主版本号。扩展握手时报自己的版本，
// 主版本不一致直接拒连：宁可让用户去更新扩展，也不要两边各按半套协议猜。
const ProtocolVersion = 1

// 帧类型。控制面与扩展之间只有这几种帧，多一种都要先改协议版本。
const (
	// FrameHello 是扩展连上来的第一帧，报协议版本、扩展 ID 和能力集。
	FrameHello = "hello"
	// FrameWelcome 是控制面的握手回执，回带生效中的策略，扩展据此本地也拦一道。
	FrameWelcome = "welcome"
	// FrameCommand 是控制面下发的一条指令。
	FrameCommand = "command"
	// FrameResult 是扩展对某条指令的回执，按 ID 配对。
	FrameResult = "result"
	// FrameTabs 是扩展主动上报的标签页清单；控制面靠它判断目标页属于哪个站点。
	FrameTabs = "tabs"
	// FrameTakeover 是人工接管状态变更，由扩展发起。
	FrameTakeover = "takeover"
	// FramePing/FramePong 是应用层心跳，用来发现半开连接。
	FramePing = "ping"
	FramePong = "pong"
	// FrameError 是控制面在握手阶段拒连时给出的原因帧。
	FrameError = "error"
)

// 指令名。只有这几条，且全部是「看得懂的单步动作」：
// 没有执行任意脚本，没有读写 Cookie，没有下载与文件系统访问。
//
// 这里没有截图。浏览器的 captureVisibleTab 要么要 <all_urls>、要么要 activeTab
// 这种「当前这一页随便读」的权限，比「只授权白名单站点」宽得多；为了一张图把扩展
// 的权限放大到全网不值得。要页面内容用 page.read；要截图用 Diana 内置浏览器（或
// 机器人配置的外部 CDP 浏览器）的 browser_screenshot，那条链路不碰用户的 Chrome。
const (
	OpTabsList  = "tabs.list"
	OpPageRead  = "page.read"
	OpPageOpen  = "page.open"
	OpPageClick = "page.click"
	OpPageType  = "page.type"
)

// 错误码。回给模型的文字会变，错误码不会，WebUI 和测试都按它判断。
const (
	CodeUnauthorized  = "unauthorized"
	CodeOriginDenied  = "origin_denied"
	CodeVersion       = "protocol_version"
	CodeNotConnected  = "not_connected"
	CodeDisabled      = "disabled"
	CodeTakeover      = "takeover"
	CodeHostDenied    = "host_denied"
	CodeWriteDisabled = "write_disabled"
	CodeRateLimited   = "rate_limited"
	CodeTimeout       = "timeout"
	CodeUnsupportedOp = "unsupported_op"
	CodeTabUnknown    = "tab_unknown"
	CodeExtension     = "extension_error"
	CodeBadRequest    = "bad_request"
)

// readOnlyOps 是不改变页面状态的指令。写操作单独一档开关，见 Policy.WriteEnabled。
var readOnlyOps = map[string]bool{
	OpTabsList: true,
	OpPageRead: true,
}

// writeOps 会点、会打字、会导航，也就是会在用户账号下留下痕迹。
var writeOps = map[string]bool{
	OpPageOpen:  true,
	OpPageClick: true,
	OpPageType:  true,
}

// KnownOp 判断指令名是否在协议内。协议外的指令一律不下发，
// 免得扩展版本不同时各自发挥。
func KnownOp(op string) bool {
	op = strings.TrimSpace(op)
	return readOnlyOps[op] || writeOps[op]
}

// IsWriteOp 判断一条指令是否属于写操作。
func IsWriteOp(op string) bool {
	return writeOps[strings.TrimSpace(op)]
}

// Frame 是控制面与扩展之间唯一的报文外壳。载荷都用 json.RawMessage 装着，
// 解析推迟到确认帧类型之后，避免一个字段类型不对整帧都读不出来。
type Frame struct {
	Type string `json:"type"`
	// ID 只在 command/result 上出现，用来配对；其余帧留空。
	ID     string          `json:"id,omitempty"`
	Op     string          `json:"op,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
	// Error 是给人看的原因，Code 是给程序判断的错误码。
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
}

// Hello 是扩展的握手载荷。
type Hello struct {
	ProtocolVersion int `json:"protocol_version"`
	// Token 是令牌明文。它放在握手帧里而不是 URL 或查询参数里：连接地址会进
	// 访问日志和浏览器历史，帧不会。控制面校验完就丢，任何日志都不记它。
	Token string `json:"token,omitempty"`
	// ExtensionID 是浏览器给扩展分配的 ID。令牌首次使用时会钉在这个 ID 上，
	// 之后换一个扩展拿同一把令牌连过来会被拒：令牌泄露不等于随便谁都能接进来。
	ExtensionID    string   `json:"extension_id"`
	ExtensionName  string   `json:"extension_name,omitempty"`
	Browser        string   `json:"browser,omitempty"`
	BrowserVersion string   `json:"browser_version,omitempty"`
	Label          string   `json:"label,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
}

// Welcome 是控制面的握手回执。策略一并回带，扩展在本地也按它拦一道：
// 控制面已经拦了，扩展再拦一次是为了让用户在扩展里能看到边界，
// 也为了控制面被绕过时页面侧仍然有限制。
type Welcome struct {
	ProtocolVersion int          `json:"protocol_version"`
	ConnectionID    string       `json:"connection_id"`
	ServerVersion   string       `json:"server_version,omitempty"`
	Policy          PolicyDigest `json:"policy"`
	// HeartbeatSeconds 是控制面的心跳间隔，扩展按它决定多久没收到 ping 就重连。
	HeartbeatSeconds int `json:"heartbeat_seconds"`
}

// TabInfo 是扩展上报的一个标签页。控制面只需要定位和站点判断用得上的字段，
// 不收集页面正文，也不收集 favicon 之外的任何资源。
type TabInfo struct {
	ID     int    `json:"id"`
	URL    string `json:"url,omitempty"`
	Title  string `json:"title,omitempty"`
	Active bool   `json:"active,omitempty"`
	Window int    `json:"window,omitempty"`
}

// TabsPayload 是标签页清单帧的载荷。
type TabsPayload struct {
	Tabs []TabInfo `json:"tabs"`
}

// TakeoverPayload 是人工接管状态帧的载荷。Active 为真时控制面停止下发指令。
type TakeoverPayload struct {
	Active bool   `json:"active"`
	Reason string `json:"reason,omitempty"`
}

// ErrProtocol 表示对端发来的帧不符合协议。
var ErrProtocol = errors.New("browser control: 协议错误")

// EncodeFrame 把一帧序列化成待发送的字节。
func EncodeFrame(frame Frame) ([]byte, error) {
	return json.Marshal(frame)
}

// DecodeFrame 解析一帧，并顺手校验帧类型非空。
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

// rawJSON 把任意值编码成 json.RawMessage；编码失败时返回 nil，
// 调用方据此判断这一帧不该发出去。
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
