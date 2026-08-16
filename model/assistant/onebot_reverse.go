// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const maxOneBotWebSocketFrameBytes = 8 << 20

type OneBotReverseServer struct {
	mu      sync.RWMutex
	cfg     OneBotConfig
	handler EventHandler
	ctx     context.Context

	connMu    sync.RWMutex
	writeMu   sync.Mutex
	conn      *websocket.Conn
	accepting bool
	// acceptGeneration invalidates an in-flight WebSocket upgrade when the
	// server closes before that upgrade has installed its connection.
	acceptGeneration uint64
	status           ChannelStatus
	pending          sync.Map
	upgrader         websocket.Upgrader
}

func (s *OneBotReverseServer) OutboundBackoffEnabled() bool { return true }

// NewOneBotReverseServer 创建反向 OneBot WebSocket server。
func NewOneBotReverseServer(cfg OneBotConfig) *OneBotReverseServer {
	return &OneBotReverseServer{
		cfg: cfg,
		status: ChannelStatus{
			Endpoint:  cfg.Endpoint,
			UpdatedAt: time.Now(),
		},
		upgrader: websocket.Upgrader{
			// NapCat does not send Origin. Browser clients must be same-origin so a
			// hostile page cannot reuse a token embedded in a WebSocket URL.
			CheckOrigin: sameOriginWebSocketRequest,
		},
	}
}

// SetConfig 更新反向 OneBot server 的连接配置。
func (s *OneBotReverseServer) SetConfig(cfg OneBotConfig) {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	s.connMu.Lock()
	s.status.Endpoint = cfg.Endpoint
	s.status.UpdatedAt = time.Now()
	s.connMu.Unlock()
}

// Connect 在反向模式下登记事件处理器并等待关闭。
func (s *OneBotReverseServer) Connect(ctx context.Context, handler EventHandler) error {
	s.mu.Lock()
	// 反向模式下 Connect 不主动拨号，只登记 handler 等待 NapCat 连进来。
	s.ctx = ctx
	s.handler = handler
	s.mu.Unlock()
	s.setStatus(false, s.Status().SelfID, "")
	<-ctx.Done()
	_ = s.Close()
	return ctx.Err()
}

// ServeHTTP 接受 NapCat 反向 WebSocket 连接。
func (s *OneBotReverseServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	clientFingerprint := oneBotClientFingerprint(r)
	selfID := strings.TrimSpace(r.Header.Get("X-Self-ID"))

	s.connMu.Lock()
	if s.conn != nil || s.accepting {
		now := time.Now()
		s.status.DuplicateConnections++
		s.status.LastRejectedClient = clientFingerprint
		s.status.LastConnectionEvent = "duplicate_client_conflict"
		s.status.LastConnectionEventTime = &now
		s.status.UpdatedAt = now
		s.connMu.Unlock()
		http.Error(w, "onebot reverse websocket already has an active client", http.StatusConflict)
		return
	}
	// Reserve the single connection slot across the HTTP upgrade so two
	// simultaneous clients cannot both pass the empty-slot check.
	s.accepting = true
	acceptGeneration := s.acceptGeneration
	s.connMu.Unlock()

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.connMu.Lock()
		if acceptGeneration == s.acceptGeneration {
			s.accepting = false
			s.status.Connected = false
			s.status.LastError = err.Error()
			s.status.UpdatedAt = time.Now()
		}
		s.connMu.Unlock()
		return
	}
	conn.SetReadLimit(maxOneBotWebSocketFrameBytes)

	s.connMu.Lock()
	if acceptGeneration != s.acceptGeneration {
		s.connMu.Unlock()
		_ = conn.Close()
		return
	}
	s.accepting = false
	s.conn = conn
	now := time.Now()
	s.status.Connected = true
	s.status.AccountStatusKnown = false
	s.status.AccountOnline = false
	s.status.AccountGood = false
	s.status.AccountStatusMessage = ""
	s.status.SelfID = selfID
	s.status.LastError = ""
	s.status.ConnectionEpoch++
	s.status.ConnectionOwner = clientFingerprint
	s.status.LastConnectionEvent = "connection_opened"
	s.status.LastConnectionEventTime = &now
	s.status.UpdatedAt = now
	s.connMu.Unlock()

	go s.readLoop(conn)
}

// Send 通过反向 OneBot 连接发送消息。
func (s *OneBotReverseServer) Send(ctx context.Context, msg OutgoingMessage) error {
	_, err := s.SendWithResult(ctx, msg)
	return err
}

// SendWithResult sends a message and preserves the OneBot response message_id.
func (s *OneBotReverseServer) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if strings.TrimSpace(msg.Text) == "" && len(msg.ImageURLs) == 0 && len(msg.VideoURLs) == 0 {
		return nil, nil
	}
	params := map[string]any{"message": buildOutgoingSegments(msg)}
	action := "send_private_msg"
	if msg.GroupID != "" {
		action = "send_group_msg"
		groupID, err := strconv.ParseInt(msg.GroupID, 10, 64)
		if err != nil {
			return nil, err
		}
		params["group_id"] = groupID
	} else {
		userID, err := strconv.ParseInt(msg.UserID, 10, 64)
		if err != nil {
			return nil, err
		}
		params["user_id"] = userID
	}
	return s.CallAPI(ctx, action, params)
}

// CallAPI 通过反向连接发送 OneBot action 并等待响应。
func (s *OneBotReverseServer) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	s.connMu.RLock()
	conn := s.conn
	s.connMu.RUnlock()
	if conn == nil {
		return nil, errors.New("qqbot: onebot reverse websocket is not connected")
	}

	echo := time.Now().Format("20060102150405.000000000")
	resultCh := make(chan callResult, 1)
	// 与正向模式一样，所有 API 调用通过 echo 等待读循环返回结果。
	s.pending.Store(echo, resultCh)
	defer s.pending.Delete(echo)

	req := map[string]any{
		"action": action,
		"params": params,
		"echo":   echo,
	}
	s.writeMu.Lock()
	err := conn.WriteJSON(req)
	s.writeMu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		return result.data, result.err
	}
}

// Status 返回反向 OneBot server 状态。
func (s *OneBotReverseServer) Status() ChannelStatus {
	s.connMu.RLock()
	defer s.connMu.RUnlock()
	return s.status
}

// Close 关闭当前反向 WebSocket 连接。
func (s *OneBotReverseServer) Close() error {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	s.acceptGeneration++
	s.accepting = false
	if s.conn == nil {
		s.status.Connected = false
		s.status.UpdatedAt = time.Now()
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	now := time.Now()
	s.status.Connected = false
	s.status.ConnectionOwner = ""
	s.status.LastConnectionEvent = "disconnected"
	s.status.LastConnectionEventTime = &now
	s.status.UpdatedAt = now
	return err
}

// readLoop 持续读取反向 WebSocket 事件帧。
func (s *OneBotReverseServer) readLoop(conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			s.disconnectIfCurrent(conn, err.Error())
			return
		}
		if err := s.handleFrame(data); err != nil {
			s.setStatus(s.Status().Connected, s.Status().SelfID, err.Error())
		}
	}
}

func (s *OneBotReverseServer) disconnectIfCurrent(conn *websocket.Conn, lastError string) {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.conn != conn {
		return
	}
	s.conn = nil
	now := time.Now()
	s.status.Connected = false
	s.status.LastError = lastError
	s.status.ConnectionOwner = ""
	s.status.LastConnectionEvent = "disconnected"
	s.status.LastConnectionEventTime = &now
	s.status.UpdatedAt = now
}

// handleFrame 解析反向 OneBot 帧并分发事件。
func (s *OneBotReverseServer) handleFrame(data []byte) error {
	var envelope oneBotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.Echo != "" {
		// echo 响应只唤醒对应 CallAPI，不进入消息 handler。
		s.resolveCall(envelope)
		return nil
	}
	if envelope.PostType == "meta_event" {
		if selfID := stringifyID(envelope.SelfID); selfID != "" {
			s.setStatus(true, selfID, "")
		}
		s.updateAccountStatus(envelope.Status)
		return nil
	}
	if envelope.PostType != "message" && envelope.PostType != "notice" {
		// request/meta 等其它事件目前不触发机器人回复。
		return nil
	}

	event := messageEventFromEnvelope(envelope)
	if event.Kind == "" {
		return nil
	}
	if event.SelfID != "" {
		s.setStatus(true, event.SelfID, "")
	}

	s.mu.RLock()
	handler := s.handler
	ctx := s.ctx
	s.mu.RUnlock()
	if handler == nil {
		return nil
	}
	if ctx == nil {
		// 单元测试或异常初始化路径可能没有 Connect context，兜底避免 nil context。
		ctx = context.Background()
	}
	go func() {
		if err := handler(ctx, event); err != nil {
			s.setStatus(s.Status().Connected, s.Status().SelfID, err.Error())
		}
	}()
	return nil
}

func (s *OneBotReverseServer) updateAccountStatus(raw any) {
	status, ok := raw.(map[string]any)
	if !ok {
		return
	}
	online, hasOnline := status["online"].(bool)
	good, hasGood := status["good"].(bool)
	if !hasOnline && !hasGood {
		return
	}
	if !hasOnline {
		online = true
	}
	if !hasGood {
		good = online
	}
	message := ""
	if !online {
		message = "QQ 账号已离线，请在 NapCat 中检查 QQ 登录状态并重新登录"
	} else if !good {
		message = "NapCat 报告 QQ 账号状态异常，请检查账号风控、网络或登录状态"
	}
	s.connMu.Lock()
	s.status.AccountStatusKnown = true
	s.status.AccountOnline = online
	s.status.AccountGood = good
	s.status.AccountStatusMessage = message
	s.status.UpdatedAt = time.Now()
	s.connMu.Unlock()
}

// resolveCall 根据 echo 处理反向 API 调用结果。
func (s *OneBotReverseServer) resolveCall(envelope oneBotEnvelope) {
	value, ok := s.pending.Load(envelope.Echo)
	if !ok {
		return
	}
	resultCh, ok := value.(chan callResult)
	if !ok {
		return
	}
	if envelopeStatusOK(envelope) {
		resultCh <- callResult{data: oneBotDataMap(envelope.Data)}
		return
	}
	resultCh <- callResult{err: errors.New(oneBotErrorMessage(envelope))}
}

// setStatus 更新反向 OneBot server 状态。
func (s *OneBotReverseServer) setStatus(connected bool, selfID string, lastError string) {
	s.mu.RLock()
	endpoint := s.cfg.Endpoint
	s.mu.RUnlock()
	s.connMu.Lock()
	defer s.connMu.Unlock()
	s.status.Connected = connected
	s.status.Endpoint = endpoint
	if selfID != "" || s.status.SelfID == "" {
		s.status.SelfID = selfID
	}
	s.status.LastError = lastError
	s.status.UpdatedAt = time.Now()
}

func oneBotClientFingerprint(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	identity := strings.Join([]string{
		strings.TrimSpace(r.RemoteAddr),
		strings.TrimSpace(r.Header.Get("X-Self-ID")),
		strings.TrimSpace(r.Header.Get("User-Agent")),
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("client-%x", sum[:8])
}

// authorized 校验反向 WebSocket 请求鉴权。
func (s *OneBotReverseServer) authorized(r *http.Request) bool {
	s.mu.RLock()
	token := strings.TrimSpace(s.cfg.AccessToken)
	s.mu.RUnlock()
	if token == "" {
		return false
	}
	// 兼容 Authorization Bearer 和 access_token 查询参数两种 NapCat 常见鉴权方式。
	if got := bearerToken(r.Header.Get("Authorization")); secureOneBotTokenEqual(got, token) {
		return true
	}
	return secureOneBotTokenEqual(r.URL.Query().Get("access_token"), token)
}

func bearerToken(value string) string {
	scheme, token, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func secureOneBotTokenEqual(left, right string) bool {
	if left == "" || right == "" || len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func sameOriginWebSocketRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}
