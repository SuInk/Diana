// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxPendingCommands 是单条连接上同时在飞的指令数上限。浏览器那头是单线程执行，
// 攒一堆只会一起超时，不如早点拒绝。
const maxPendingCommands = 8

// Conn 是一条控制连接的发送能力，由传输层（WebSocket）实现。
// 抽出来是为了让 Hub 的策略与配对逻辑能在没有网络的测试里跑完。
type Conn interface {
	Send(frame Frame) error
	Close() error
}

// Result 是一条指令的执行结果。
type Result struct {
	OK    bool            `json:"ok"`
	Code  string          `json:"code,omitempty"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// CommandError 是被控制面自己拦下的指令错误，带错误码。
type CommandError struct {
	Code    string
	Message string
}

func (e *CommandError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func commandError(code, format string, args ...any) *CommandError {
	return &CommandError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrorCode 取出错误里的错误码，没有则返回空串。
func ErrorCode(err error) string {
	var typed *CommandError
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

// ConnectionStatus 是一条连接在管理接口里的展示形态。
type ConnectionStatus struct {
	ID             string    `json:"id"`
	TokenID        string    `json:"token_id"`
	TokenName      string    `json:"token_name,omitempty"`
	ExtensionID    string    `json:"extension_id"`
	ExtensionName  string    `json:"extension_name,omitempty"`
	Browser        string    `json:"browser,omitempty"`
	BrowserVersion string    `json:"browser_version,omitempty"`
	Label          string    `json:"label,omitempty"`
	ConnectedAt    time.Time `json:"connected_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	Takeover       bool      `json:"takeover"`
	TakeoverReason string    `json:"takeover_reason,omitempty"`
	// AllowedTabs 只统计策略允许操作的标签页，不暴露用户其余标签页的数量。
	AllowedTabs int `json:"allowed_tabs"`
	Commands    int `json:"commands"`
}

// Hub 保管已握手的控制连接，并在下发指令前逐条核对授权边界。
type Hub struct {
	registry *Registry
	now      func() time.Time

	mu    sync.RWMutex
	conns map[string]*Connection
	seq   uint64
}

// NewHub 创建控制面。registry 提供策略与令牌，为 nil 时 Hub 一律拒绝下发。
func NewHub(registry *Registry) *Hub {
	return &Hub{registry: registry, now: time.Now, conns: map[string]*Connection{}}
}

// Policy 返回当前策略。
func (h *Hub) Policy() Policy {
	if h == nil {
		return Policy{}.WithDefaults()
	}
	return h.registry.Policy()
}

// Connection 是一条已握手的控制连接。
type Connection struct {
	hub     *Hub
	id      string
	conn    Conn
	hello   Hello
	token   TokenInfo
	created time.Time

	mu         sync.Mutex
	lastSeen   time.Time
	tabs       []TabInfo
	takeover   bool
	takeReason string
	pending    map[string]chan Result
	closed     bool
	commands   int
	windowFrom time.Time
	windowUsed int
	seq        uint64
}

// Register 登记一条已通过鉴权的连接。调用方必须先校验令牌与来源。
func (h *Hub) Register(conn Conn, hello Hello, token TokenInfo) (*Connection, Welcome, error) {
	if h == nil || conn == nil {
		return nil, Welcome{}, errors.New("browser control hub 未初始化")
	}
	if hello.ProtocolVersion != ProtocolVersion {
		return nil, Welcome{}, commandError(CodeVersion,
			"扩展协议版本 %d 与控制面 %d 不一致，请更新扩展", hello.ProtocolVersion, ProtocolVersion)
	}
	policy := h.registry.Policy()
	if !policy.Enabled {
		return nil, Welcome{}, commandError(CodeDisabled, "浏览器控制未启用")
	}
	// 令牌已经在传输层校验过了，连接对象不留它：状态接口、日志和崩溃转储
	// 都会打印 Connection 里的握手信息，明文不该跟着一起出现。
	hello.Token = ""
	now := h.now()
	h.mu.Lock()
	h.seq++
	id := fmt.Sprintf("bc-%d-%d", now.UnixNano(), h.seq)
	c := &Connection{
		hub:      h,
		id:       id,
		conn:     conn,
		hello:    hello,
		token:    token,
		created:  now,
		lastSeen: now,
		pending:  map[string]chan Result{},
	}
	h.conns[id] = c
	// 同一个扩展重连时顶掉它上一条连接。扩展只会持有一条 socket，旧的那条
	// 要么已经死了、要么是半开——但控制面要等心跳超时才发现，这段时间里
	// pickConnection 会看到两条「同一个浏览器」，然后要求调用方点名，
	// 而两条在模型眼里长得一模一样，等于这段时间工具全不能用。
	stale := make([]*Connection, 0, 1)
	for _, other := range h.conns {
		if other != c && other.hello.ExtensionID != "" && other.hello.ExtensionID == hello.ExtensionID {
			stale = append(stale, other)
		}
	}
	h.mu.Unlock()
	// Close 自己要拿 h.mu，放到锁外面关。
	for _, other := range stale {
		other.Close()
	}
	return c, Welcome{
		ProtocolVersion:  ProtocolVersion,
		ConnectionID:     id,
		Policy:           policy.Digest(),
		HeartbeatSeconds: DefaultHeartbeatSeconds,
	}, nil
}

// ID 返回连接 ID。
func (c *Connection) ID() string {
	if c == nil {
		return ""
	}
	return c.id
}

// Close 注销连接，并让所有在飞指令立即失败，不用等各自超时。
func (c *Connection) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = map[string]chan Result{}
	c.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- Result{Code: CodeNotConnected, Error: "浏览器控制连接已断开"}:
		default:
		}
	}
	if c.hub != nil {
		c.hub.mu.Lock()
		delete(c.hub.conns, c.id)
		c.hub.mu.Unlock()
	}
	_ = c.conn.Close()
}

// HandleFrame 处理扩展发来的一帧。只认协议内的帧类型，其余忽略：
// 对端多发了什么不该让控制面跟着走。
func (c *Connection) HandleFrame(frame Frame) {
	if c == nil {
		return
	}
	now := c.hub.nowOrDefault()
	c.mu.Lock()
	c.lastSeen = now
	c.mu.Unlock()
	switch frame.Type {
	case FrameResult:
		c.deliver(frame)
	case FrameTabs:
		var payload TabsPayload
		if len(frame.Data) == 0 || json.Unmarshal(frame.Data, &payload) != nil {
			return
		}
		c.setTabs(payload.Tabs)
	case FrameTakeover:
		var payload TakeoverPayload
		if len(frame.Data) == 0 || json.Unmarshal(frame.Data, &payload) != nil {
			return
		}
		c.setTakeover(payload.Active, payload.Reason)
	case FramePing:
		_ = c.conn.Send(Frame{Type: FramePong})
	case FramePong:
		// 心跳回执，lastSeen 已经更新过了。
	}
}

func (c *Connection) deliver(frame Frame) {
	id := strings.TrimSpace(frame.ID)
	if id == "" {
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if !ok {
		// 迟到的回执：对应指令已经超时或连接已重置，丢掉就好。
		return
	}
	result := Result{OK: frame.Error == "" && frame.Code == "", Code: frame.Code, Error: frame.Error, Data: frame.Data}
	if !result.OK && result.Code == "" {
		result.Code = CodeExtension
	}
	select {
	case ch <- result:
	default:
	}
}

func (c *Connection) setTabs(tabs []TabInfo) {
	clean := make([]TabInfo, 0, len(tabs))
	for _, tab := range tabs {
		tab.URL = strings.TrimSpace(tab.URL)
		tab.Title = strings.TrimSpace(tab.Title)
		clean = append(clean, tab)
	}
	c.mu.Lock()
	c.tabs = clean
	c.mu.Unlock()
}

func (c *Connection) setTakeover(active bool, reason string) {
	c.mu.Lock()
	c.takeover = active
	c.takeReason = strings.TrimSpace(reason)
	c.mu.Unlock()
}

// SetTakeover 从控制面一侧切换人工接管，供 WebUI 使用。
func (c *Connection) SetTakeover(active bool, reason string) {
	if c == nil {
		return
	}
	c.setTakeover(active, reason)
	// 扩展要知道当前状态，好在页面上显示是谁在开车。
	_ = c.conn.Send(Frame{Type: FrameTakeover, Data: rawJSON(TakeoverPayload{Active: active, Reason: reason})})
}

// Takeover 返回当前接管状态。
func (c *Connection) Takeover() (bool, string) {
	if c == nil {
		return false, ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.takeover, c.takeReason
}

// Tabs 返回策略允许操作的标签页。不在白名单内的标签页不出现在这里，
// 模型也就看不到用户其余页面的地址和标题。
func (c *Connection) Tabs(policy Policy) []TabInfo {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	tabs := append([]TabInfo(nil), c.tabs...)
	c.mu.Unlock()
	allowed := make([]TabInfo, 0, len(tabs))
	for _, tab := range tabs {
		if policy.HostAllowed(tab.URL) {
			allowed = append(allowed, tab)
		}
	}
	return allowed
}

func (c *Connection) status(policy Policy) ConnectionStatus {
	c.mu.Lock()
	takeover, reason := c.takeover, c.takeReason
	lastSeen := c.lastSeen
	commands := c.commands
	c.mu.Unlock()
	return ConnectionStatus{
		ID:             c.id,
		TokenID:        c.token.ID,
		TokenName:      c.token.Name,
		ExtensionID:    c.hello.ExtensionID,
		ExtensionName:  c.hello.ExtensionName,
		Browser:        c.hello.Browser,
		BrowserVersion: c.hello.BrowserVersion,
		Label:          c.hello.Label,
		ConnectedAt:    c.created,
		LastSeenAt:     lastSeen,
		Takeover:       takeover,
		TakeoverReason: reason,
		AllowedTabs:    len(c.Tabs(policy)),
		Commands:       commands,
	}
}

// Connections 返回全部连接状态，按连接时间倒序。
func (h *Hub) Connections() []ConnectionStatus {
	if h == nil {
		return nil
	}
	policy := h.registry.Policy()
	h.mu.RLock()
	items := make([]ConnectionStatus, 0, len(h.conns))
	for _, c := range h.conns {
		items = append(items, c.status(policy))
	}
	h.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if !items[i].ConnectedAt.Equal(items[j].ConnectedAt) {
			return items[i].ConnectedAt.After(items[j].ConnectedAt)
		}
		return items[i].ID < items[j].ID
	})
	return items
}

// Connection 按 ID 取连接。
func (h *Hub) Connection(id string) (*Connection, bool) {
	if h == nil {
		return nil, false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.conns[strings.TrimSpace(id)]
	return c, ok
}

// CloseAll 断开所有连接，用于关闭总开关或进程退出。
func (h *Hub) CloseAll() {
	if h == nil {
		return
	}
	h.mu.RLock()
	conns := make([]*Connection, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	for _, c := range conns {
		c.Close()
	}
}

// Ready 表示现在能不能真的操作浏览器：总开关开着、有连接、且没人在接管。
func (h *Hub) Ready() bool {
	if h == nil || !h.registry.Policy().Enabled {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.conns {
		if takeover, _ := c.Takeover(); !takeover {
			return true
		}
	}
	return false
}

func (h *Hub) nowOrDefault() time.Time {
	if h == nil || h.now == nil {
		return time.Now()
	}
	return h.now()
}

// pickConnection 选出本次指令要用的连接。只有一条时直接用；多条时必须由
// 调用方点名，不猜：猜错等于在用户另一个浏览器里点东西。
func (h *Hub) pickConnection(id string) (*Connection, error) {
	id = strings.TrimSpace(id)
	h.mu.RLock()
	defer h.mu.RUnlock()
	if id != "" {
		c, ok := h.conns[id]
		if !ok {
			return nil, commandError(CodeNotConnected, "浏览器控制连接 %s 不存在", id)
		}
		return c, nil
	}
	switch len(h.conns) {
	case 0:
		return nil, commandError(CodeNotConnected, "没有已连接的浏览器控制扩展")
	case 1:
		for _, c := range h.conns {
			return c, nil
		}
	}
	ids := make([]string, 0, len(h.conns))
	for key := range h.conns {
		ids = append(ids, key)
	}
	sort.Strings(ids)
	return nil, commandError(CodeBadRequest,
		"有 %d 个浏览器控制连接，请用 connection 参数点名其中一个：%s", len(ids), strings.Join(ids, ", "))
}
