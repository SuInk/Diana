// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxPendingCommands = 8

// Conn 是一条控制连接的发送能力。
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

// CommandError 是被控制面自己拦下的指令错误。
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

// ErrorCode 取出错误里的错误码。
func ErrorCode(err error) string {
	var typed *CommandError
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

// ConnectionStatus 是一条连接的展示形态。
type ConnectionStatus struct {
	ID             string    `json:"id"`
	TokenID        string    `json:"token_id,omitempty"`
	TokenName      string    `json:"token_name,omitempty"`
	HelperID       string    `json:"helper_id"`
	HelperName     string    `json:"helper_name,omitempty"`
	Platform       string    `json:"platform,omitempty"`
	Label          string    `json:"label,omitempty"`
	ConnectedAt    time.Time `json:"connected_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	Takeover       bool      `json:"takeover"`
	TakeoverReason string    `json:"takeover_reason,omitempty"`
	AllowedWindows int       `json:"allowed_windows"`
	Commands       int       `json:"commands"`
}

// Hub 保管已握手的控制连接。
type Hub struct {
	registry *Registry
	jobs     *JobManager
	now      func() time.Time

	stopMu sync.Mutex
	mu     sync.RWMutex
	conns  map[string]*Connection
	seq    uint64
}

// NewHub 创建控制面。
func NewHub(registry *Registry) *Hub {
	return &Hub{registry: registry, now: time.Now, conns: map[string]*Connection{}}
}

// SetJobManager 挂上持久任务管理器；为 nil 时 Dispatch 不走任务门禁。
func (h *Hub) SetJobManager(jobs *JobManager) {
	if h == nil {
		return
	}
	h.jobs = jobs
}

// Jobs 返回任务管理器。
func (h *Hub) Jobs() *JobManager {
	if h == nil {
		return nil
	}
	return h.jobs
}

// Policy 返回当前策略。
func (h *Hub) Policy() Policy {
	if h == nil || h.registry == nil {
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
	windows    []WindowInfo
	takeover   bool
	takeReason string
	pending    map[string]chan Result
	closed     bool
	commands   int
	windowFrom time.Time
	windowUsed int
	seq        uint64
}

// Register 登记一条已通过鉴权的连接。
func (h *Hub) Register(conn Conn, hello Hello, token TokenInfo) (*Connection, Welcome, error) {
	if h == nil || conn == nil {
		return nil, Welcome{}, errors.New("desktop control hub 未初始化")
	}
	if hello.ProtocolVersion != ProtocolVersion {
		return nil, Welcome{}, commandError(CodeVersion,
			"执行器协议版本 %d 与控制面 %d 不一致，请更新执行器", hello.ProtocolVersion, ProtocolVersion)
	}
	if h.registry == nil {
		return nil, Welcome{}, commandError(CodeDisabled, "桌面控制未初始化")
	}
	policy := h.registry.Policy()
	if !policy.Enabled {
		return nil, Welcome{}, commandError(CodeDisabled, "桌面控制未启用")
	}
	h.stopMu.Lock()
	defer h.stopMu.Unlock()
	hello.Token = ""
	now := h.now()
	h.mu.Lock()
	h.seq++
	id := fmt.Sprintf("dc-%d-%d", now.UnixNano(), h.seq)
	c := &Connection{
		hub:      h,
		id:       id,
		conn:     conn,
		hello:    hello,
		token:    token,
		created:  now,
		lastSeen: now,
		pending:  map[string]chan Result{},
		takeover: h.registry.EmergencyStop(),
	}
	h.conns[id] = c
	stale := make([]*Connection, 0, 1)
	for _, other := range h.conns {
		if other != c && other.hello.HelperID != "" && other.hello.HelperID == hello.HelperID {
			stale = append(stale, other)
		}
	}
	h.mu.Unlock()
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

// Close 注销连接并让在飞指令失败。
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
		case ch <- Result{Code: CodeNotConnected, Error: "桌面控制连接已断开"}:
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

// HandleFrame 处理执行器发来的一帧。
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
	case FrameWindows:
		var payload WindowsPayload
		if len(frame.Data) == 0 || json.Unmarshal(frame.Data, &payload) != nil {
			return
		}
		c.setWindows(payload.Windows)
	case FrameTakeover:
		var payload TakeoverPayload
		if len(frame.Data) == 0 || json.Unmarshal(frame.Data, &payload) != nil {
			return
		}
		_ = c.hub.SetTakeover(context.Background(), payload.Active, payload.Reason)
	case FramePing:
		_ = c.conn.Send(Frame{Type: FramePong})
	case FramePong:
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
		return
	}
	result := Result{OK: frame.Error == "" && frame.Code == "", Code: frame.Code, Error: frame.Error, Data: frame.Data}
	if !result.OK && result.Code == "" {
		result.Code = CodeHelper
	}
	select {
	case ch <- result:
	default:
	}
}

func (c *Connection) setWindows(windows []WindowInfo) {
	clean := make([]WindowInfo, 0, len(windows))
	for _, win := range windows {
		win.ID = strings.TrimSpace(win.ID)
		win.AppName = strings.TrimSpace(win.AppName)
		win.BundleID = strings.TrimSpace(win.BundleID)
		win.Title = strings.TrimSpace(win.Title)
		if win.ID == "" {
			continue
		}
		clean = append(clean, win)
	}
	c.mu.Lock()
	c.windows = clean
	c.mu.Unlock()
}

func (c *Connection) setTakeover(active bool, reason string) {
	c.mu.Lock()
	c.takeover = active
	c.takeReason = strings.TrimSpace(reason)
	c.mu.Unlock()
}

// SetTakeover 从控制面一侧切换人工接管。
func (c *Connection) SetTakeover(active bool, reason string) {
	if c == nil {
		return
	}
	_ = c.hub.SetTakeover(context.Background(), active, reason)
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

// Windows 返回策略允许的窗口；未授权应用的窗口不可见。
func (c *Connection) Windows(policy Policy) []WindowInfo {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	windows := append([]WindowInfo(nil), c.windows...)
	c.mu.Unlock()
	allowed := make([]WindowInfo, 0, len(windows))
	for _, win := range windows {
		if policy.AppAllowed(win.BundleID, win.AppName) {
			allowed = append(allowed, win)
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
		HelperID:       c.hello.HelperID,
		HelperName:     c.hello.HelperName,
		Platform:       c.hello.Platform,
		Label:          c.hello.Label,
		ConnectedAt:    c.created,
		LastSeenAt:     lastSeen,
		Takeover:       takeover,
		TakeoverReason: reason,
		AllowedWindows: len(c.Windows(policy)),
		Commands:       commands,
	}
}

// Connections 返回全部连接状态。
func (h *Hub) Connections() []ConnectionStatus {
	if h == nil {
		return nil
	}
	policy := h.Policy()
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

// CloseAll 断开所有连接。
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

// Ready 表示现在能不能操作桌面。
func (h *Hub) Ready() bool {
	if h == nil || h.registry == nil || h.registry.EmergencyStop() || !h.registry.Policy().Enabled {
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

func (h *Hub) pickConnection(id string) (*Connection, error) {
	id = strings.TrimSpace(id)
	h.mu.RLock()
	defer h.mu.RUnlock()
	if id != "" {
		c, ok := h.conns[id]
		if !ok {
			return nil, commandError(CodeNotConnected, "桌面控制连接 %s 不存在", id)
		}
		return c, nil
	}
	switch len(h.conns) {
	case 0:
		return nil, commandError(CodeNotConnected, "没有已连接的桌面执行器")
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
		"有 %d 个桌面控制连接，请用 connection 参数点名其中一个：%s", len(ids), strings.Join(ids, ", "))
}

// SetTakeover persists a machine-wide stop independently of connection lifetime.
func (h *Hub) SetTakeover(ctx context.Context, active bool, reason string) error {
	h.stopMu.Lock()
	defer h.stopMu.Unlock()
	h.mu.RLock()
	conns := make([]*Connection, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	apply := func(stop bool) {
		for _, c := range conns {
			c.setTakeover(stop, reason)
			_ = c.conn.Send(Frame{Type: FrameTakeover, Data: rawJSON(TakeoverPayload{Active: stop, Reason: reason})})
		}
	}
	// Cancel in-flight input before waiting for persistence.
	if active {
		apply(true)
	}
	err := h.registry.setEmergencyStop(ctx, active)
	if !active {
		apply(h.registry.EmergencyStop())
	}
	return err
}
