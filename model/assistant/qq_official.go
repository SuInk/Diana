// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// QQOfficialConfig 是 QQ 开放平台机器人的连接配置。
type QQOfficialConfig struct {
	// AppID 是开放平台的机器人 AppID。
	AppID string
	// AppSecret 用于换取 access token。
	AppSecret string
	// Sandbox 走沙箱环境，用于机器人未上架前的联调。
	Sandbox bool
}

const (
	qqOfficialTokenURL      = "https://bots.qq.com/app/getAppAccessToken"
	qqOfficialAPIBase       = "https://api.sgroup.qq.com"
	qqOfficialSandboxAPI    = "https://sandbox.api.sgroup.qq.com"
	qqOfficialHeartbeatSlop = 5 * time.Second

	// qqIntentGroupAndC2C 订阅群聊 @ 消息和单聊消息，这是「QQ 机器人」这个形态
	// 的主场景；频道相关意图另算，没开通频道能力时订阅了会被网关拒绝。
	qqIntentGroupAndC2C = 1 << 25
	// qqIntentPublicGuildMessages 订阅频道内的 @ 消息。
	qqIntentPublicGuildMessages = 1 << 30
)

// QQOfficialChannel 通过 QQ 开放平台的 WebSocket 网关接入官方机器人。
//
// 开放平台同时提供 webhook 和 WebSocket 两种接收方式。这里选 WebSocket：它是
// 本机主动出站建连，和 Telegram 长轮询一样不需要公网地址和证书，家庭或内网
// 部署可以直接用。
type QQOfficialChannel struct {
	mu      sync.RWMutex
	cfg     QQOfficialConfig
	handler EventHandler
	client  *http.Client
	cancel  context.CancelFunc

	statusMu sync.RWMutex
	status   ChannelStatus

	tokens *platformTokenCache

	// sessionID 和 lastSeq 用于断线后 RESUME，避免重连丢事件。
	connMu    sync.Mutex
	conn      *websocket.Conn
	sessionID string
	lastSeq   int64
}

// NewQQOfficialChannel 创建 QQ 官方机器人通道。
func NewQQOfficialChannel(cfg QQOfficialConfig) *QQOfficialChannel {
	channel := &QQOfficialChannel{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
		status: ChannelStatus{Endpoint: qqOfficialEndpointLabel(cfg), UpdatedAt: time.Now()},
	}
	channel.tokens = &platformTokenCache{fetch: channel.fetchAccessToken}
	return channel
}

func qqOfficialEndpointLabel(cfg QQOfficialConfig) string {
	if cfg.Sandbox {
		return qqOfficialSandboxAPI + " (sandbox gateway)"
	}
	return qqOfficialAPIBase + " (gateway ws)"
}

// SetConfig 更新连接配置并丢弃已缓存的 token。
func (c *QQOfficialChannel) SetConfig(cfg QQOfficialConfig) {
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
	c.tokens.Invalidate()
	c.statusMu.Lock()
	c.status.Endpoint = qqOfficialEndpointLabel(cfg)
	c.statusMu.Unlock()
	c.setStatus(false, c.Status().SelfID, "")
}

func (c *QQOfficialChannel) apiBase() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cfg.Sandbox {
		return qqOfficialSandboxAPI
	}
	return qqOfficialAPIBase
}

// fetchAccessToken 用 AppID + AppSecret 换取 access token。
func (c *QQOfficialChannel) fetchAccessToken(ctx context.Context) (string, time.Duration, error) {
	c.mu.RLock()
	appID := strings.TrimSpace(c.cfg.AppID)
	secret := strings.TrimSpace(c.cfg.AppSecret)
	client := c.client
	c.mu.RUnlock()
	if appID == "" || secret == "" {
		return "", 0, fmt.Errorf("qq: AppID 和 AppSecret 都必须配置")
	}
	raw, err := platformJSONRequest(ctx, client, http.MethodPost, qqOfficialTokenURL, nil, map[string]string{
		"appId":        appID,
		"clientSecret": secret,
	})
	if err != nil {
		return "", 0, fmt.Errorf("qq: 换取 access token 失败: %w", err)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   any    `json:"expires_in"`
		Message     string `json:"message"`
		Code        int    `json:"code"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", 0, fmt.Errorf("qq: 解析 token 响应失败: %w", err)
	}
	if payload.AccessToken == "" {
		return "", 0, fmt.Errorf("qq: 换取 access token 被拒绝: %s (code %d)", payload.Message, payload.Code)
	}
	ttl := time.Duration(platformEventTime(payload.ExpiresIn)) * time.Second
	if ttl <= 0 {
		// 开放平台目前固定 7200 秒；字段缺失时按这个兜底，不至于每次都换。
		ttl = 2 * time.Hour
	}
	return payload.AccessToken, ttl, nil
}

// authHeader 组装开放平台要求的 Authorization 头。
func (c *QQOfficialChannel) authHeader(ctx context.Context) (string, error) {
	token, err := c.tokens.Get(ctx)
	if err != nil {
		return "", err
	}
	return "QQBot " + token, nil
}

// Connect 建立网关连接并持续接收事件，直到 ctx 取消。
func (c *QQOfficialChannel) Connect(ctx context.Context, handler EventHandler) error {
	c.mu.Lock()
	if strings.TrimSpace(c.cfg.AppID) == "" || strings.TrimSpace(c.cfg.AppSecret) == "" {
		c.mu.Unlock()
		c.setStatus(false, "", "未配置 QQ 机器人 AppID / AppSecret")
		return fmt.Errorf("qq: AppID 和 AppSecret 都必须配置")
	}
	c.handler = handler
	c.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	defer cancel()

	backoff := time.Second
	for runCtx.Err() == nil {
		err := c.runSession(runCtx)
		if runCtx.Err() != nil {
			break
		}
		if err != nil {
			c.setStatus(false, c.Status().SelfID, err.Error())
		}
		select {
		case <-runCtx.Done():
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return runCtx.Err()
}

// runSession 跑完一次网关连接的生命周期，返回时连接已关闭。
func (c *QQOfficialChannel) runSession(ctx context.Context) error {
	gateway, err := c.gatewayURL(ctx)
	if err != nil {
		return err
	}
	auth, err := c.authHeader(ctx)
	if err != nil {
		return err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, gateway, nil)
	if err != nil {
		return fmt.Errorf("qq: 连接网关失败: %w", err)
	}
	defer conn.Close()
	c.connMu.Lock()
	c.conn = conn
	session := c.sessionID
	seq := c.lastSeq
	c.connMu.Unlock()

	// OP 10 Hello 带来心跳间隔，必须先收到它才能开始鉴权。
	var hello qqGatewayPayload
	if err := conn.ReadJSON(&hello); err != nil {
		return fmt.Errorf("qq: 读取 Hello 失败: %w", err)
	}
	interval := 30 * time.Second
	if hello.Op == qqOpHello {
		var data struct {
			HeartbeatInterval int64 `json:"heartbeat_interval"`
		}
		if err := json.Unmarshal(hello.Data, &data); err == nil && data.HeartbeatInterval > 0 {
			interval = time.Duration(data.HeartbeatInterval) * time.Millisecond
		}
	}

	// 有 session 就 RESUME 续上，否则 IDENTIFY 开新会话。
	if session != "" {
		err = conn.WriteJSON(qqGatewayPayload{Op: qqOpResume, D: map[string]any{
			"token":      auth,
			"session_id": session,
			"seq":        seq,
		}})
	} else {
		err = conn.WriteJSON(qqGatewayPayload{Op: qqOpIdentify, D: map[string]any{
			"token":      auth,
			"intents":    qqIntentGroupAndC2C | qqIntentPublicGuildMessages,
			"shard":      []int{0, 1},
			"properties": map[string]string{},
		}})
	}
	if err != nil {
		return fmt.Errorf("qq: 鉴权失败: %w", err)
	}

	sessionCtx, stop := context.WithCancel(ctx)
	defer stop()
	go c.heartbeatLoop(sessionCtx, conn, interval)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var payload qqGatewayPayload
		if err := conn.ReadJSON(&payload); err != nil {
			return fmt.Errorf("qq: 网关连接中断: %w", err)
		}
		if payload.S > 0 {
			c.connMu.Lock()
			c.lastSeq = payload.S
			c.connMu.Unlock()
		}
		switch payload.Op {
		case qqOpDispatch:
			c.handleDispatch(ctx, payload)
		case qqOpInvalidSession:
			// 会话失效：清掉 session 让下一轮走全新 IDENTIFY，否则会一直被拒。
			c.connMu.Lock()
			c.sessionID = ""
			c.lastSeq = 0
			c.connMu.Unlock()
			return fmt.Errorf("qq: 会话失效，将重新鉴权")
		case qqOpReconnect:
			return fmt.Errorf("qq: 网关要求重连")
		}
	}
}

// heartbeatLoop 按网关给的间隔上报心跳，附带最后处理的 seq。
func (c *QQOfficialChannel) heartbeatLoop(ctx context.Context, conn *websocket.Conn, interval time.Duration) {
	if interval <= qqOfficialHeartbeatSlop {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.connMu.Lock()
			seq := c.lastSeq
			c.connMu.Unlock()
			var data any
			if seq > 0 {
				data = seq
			}
			encoded, err := json.Marshal(qqGatewayPayload{Op: qqOpHeartbeat, D: data})
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, encoded); err != nil {
				return
			}
		}
	}
}

// handleDispatch 处理 OP 0 的业务事件。
func (c *QQOfficialChannel) handleDispatch(ctx context.Context, payload qqGatewayPayload) {
	switch payload.T {
	case "READY":
		var ready struct {
			SessionID string `json:"session_id"`
			User      struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"user"`
		}
		if err := json.Unmarshal(payload.Data, &ready); err == nil {
			c.connMu.Lock()
			c.sessionID = ready.SessionID
			c.connMu.Unlock()
			c.setStatus(true, ready.User.ID, "")
		}
		return
	case "RESUMED":
		c.setStatus(true, c.Status().SelfID, "")
		return
	}

	event, ok := qqOfficialEventFromDispatch(payload.T, payload.Data, c.Status().SelfID)
	if !ok {
		return
	}
	c.mu.RLock()
	handler := c.handler
	c.mu.RUnlock()
	if handler == nil {
		return
	}
	_ = handler(ctx, event)
}

// gatewayURL 取网关地址。
func (c *QQOfficialChannel) gatewayURL(ctx context.Context) (string, error) {
	auth, err := c.authHeader(ctx)
	if err != nil {
		return "", err
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	raw, err := platformJSONRequest(ctx, client, http.MethodGet, c.apiBase()+"/gateway", map[string]string{
		"Authorization": auth,
	}, nil)
	if err != nil {
		return "", fmt.Errorf("qq: 获取网关地址失败: %w", err)
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || strings.TrimSpace(payload.URL) == "" {
		return "", fmt.Errorf("qq: 网关地址无效: %s", truncateForError(string(raw)))
	}
	return payload.URL, nil
}

// Send 把出站消息投递到群或单聊。
//
// 开放平台的主动推送有严格额度，被动回复（带上收到那条消息的 msg_id）不占额度，
// 所以这里只要拿得到 ReplyMessageID 就一定带上。
func (c *QQOfficialChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	_, err := c.SendWithResult(ctx, msg)
	return err
}

// SendWithResult 发送消息并把开放平台返回的消息 id 交回上层。
//
// 上层靠它把 Diana 自己这条发言连同平台 ID 记进历史；别人引用这条消息时，入站事件
// 的 message_reference.message_id 与之同属一个空间，回查才对得上。不实现的话出站
// 消息以空 ID 入库，引用 Diana 必然还原不出内容。
func (c *QQOfficialChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	target, isGroup := platformChatTarget(msg)
	if target == "" {
		return nil, fmt.Errorf("qq: 缺少会话标识")
	}
	text := platformOutboundText(msg)
	if text == "" && len(msg.ImageURLs) == 0 {
		return nil, nil
	}
	endpoint := c.apiBase() + "/v2/users/" + target + "/messages"
	if isGroup {
		endpoint = c.apiBase() + "/v2/groups/" + target + "/messages"
	}
	auth, err := c.authHeader(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"content": text,
		// msg_type 0 是纯文本。富媒体要先上传拿 file_info，另走一条链路。
		"msg_type": 0,
	}
	if replyID := strings.TrimSpace(msg.ReplyMessageID); replyID != "" {
		body["msg_id"] = replyID
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	raw, err := platformJSONRequest(ctx, client, http.MethodPost, endpoint, map[string]string{
		"Authorization": auth,
	}, body)
	if err != nil {
		// token 过期时开放平台返回 401；丢掉缓存让下一次重新换。
		if strings.Contains(err.Error(), "http 401") {
			c.tokens.Invalidate()
		}
		return nil, fmt.Errorf("qq: 发送失败: %w", err)
	}
	var envelope struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		ID      string `json:"id"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Code != 0 {
		return nil, fmt.Errorf("qq: 发送被拒绝: %s (code %d)", envelope.Message, envelope.Code)
	}
	if id := strings.TrimSpace(envelope.ID); id != "" {
		return map[string]any{"message_id": id}, nil
	}
	return nil, nil
}

// CallAPI 透传开放平台的 REST 接口，action 形如 "GET /users/@me/guilds"。
func (c *QQOfficialChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	method, path := http.MethodGet, strings.TrimSpace(action)
	if fields := strings.SplitN(path, " ", 2); len(fields) == 2 {
		method, path = strings.ToUpper(fields[0]), strings.TrimSpace(fields[1])
	}
	if path == "" {
		return nil, fmt.Errorf("qq: 缺少接口路径")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	auth, err := c.authHeader(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	var payload any
	if len(params) > 0 && method != http.MethodGet {
		payload = params
	}
	raw, err := platformJSONRequest(ctx, client, method, c.apiBase()+path, map[string]string{
		"Authorization": auth,
	}, payload)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{"result": json.RawMessage(raw)}, nil
	}
	return out, nil
}

// Status 返回通道状态。
func (c *QQOfficialChannel) Status() ChannelStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.status
}

func (c *QQOfficialChannel) setStatus(connected bool, selfID, lastErr string) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	c.status.Connected = connected
	if selfID != "" {
		c.status.SelfID = selfID
	}
	c.status.LastError = lastErr
	c.status.UpdatedAt = time.Now()
}

// Close 断开网关连接。
func (c *QQOfficialChannel) Close() error {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.connMu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()
	c.setStatus(false, c.Status().SelfID, "")
	return nil
}

// —— 网关协议 ——

const (
	qqOpDispatch       = 0
	qqOpHeartbeat      = 1
	qqOpIdentify       = 2
	qqOpResume         = 6
	qqOpReconnect      = 7
	qqOpInvalidSession = 9
	qqOpHello          = 10
)

type qqGatewayPayload struct {
	Op   int             `json:"op"`
	S    int64           `json:"s,omitempty"`
	T    string          `json:"t,omitempty"`
	Data json.RawMessage `json:"d,omitempty"`
	// D 只在发送时用；接收时统一走 Data。
	D any `json:"-"`
}

// MarshalJSON 让发送方向可以直接塞任意 d，接收方向仍保留 RawMessage。
func (p qqGatewayPayload) MarshalJSON() ([]byte, error) {
	out := map[string]any{"op": p.Op}
	if p.D != nil {
		out["d"] = p.D
	}
	if p.T != "" {
		out["t"] = p.T
	}
	if p.S > 0 {
		out["s"] = p.S
	}
	return json.Marshal(out)
}

// qqOfficialMessage 是群聊 / 单聊 / 频道消息的公共字段。
type qqOfficialMessage struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	// GroupOpenID 只在群消息里出现，是这个群对本机器人的稳定标识。
	GroupOpenID string `json:"group_openid"`
	// ChannelID / GuildID 出现在频道消息里。
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	Timestamp any    `json:"timestamp"`
	Author    struct {
		ID string `json:"id"`
		// UserOpenID 是单聊里的用户标识；MemberOpenID 是群里的。
		UserOpenID   string `json:"user_openid"`
		MemberOpenID string `json:"member_openid"`
		Username     string `json:"username"`
	} `json:"author"`
	Member struct {
		Nick  string   `json:"nick"`
		Roles []string `json:"roles"`
	} `json:"member"`
	MessageReference *struct {
		MessageID string `json:"message_id"`
	} `json:"message_reference"`
}

// qqOfficialEventFromDispatch 把网关事件映射成统一事件。
//
// 语义对照：
//   - GROUP_AT_MESSAGE_CREATE 群里 @ 机器人 -> 群聊，group_openid 当群号
//   - C2C_MESSAGE_CREATE 单聊 -> 私聊
//   - AT_MESSAGE_CREATE 频道里 @ 机器人 -> 群聊，channel_id 当群号
//
// 开放平台只把「@ 了机器人」的群消息推过来（这是平台侧的硬限制，拿不到全部群
// 消息），所以群消息一律 ToMe=true——收到即意味着被点名。
func qqOfficialEventFromDispatch(eventType string, data json.RawMessage, selfID string) (MessageEvent, bool) {
	var msg qqOfficialMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return MessageEvent{}, false
	}
	text := strings.TrimSpace(msg.Content)
	quoted := ""
	if msg.MessageReference != nil {
		quoted = msg.MessageReference.MessageID
	}

	event := MessageEvent{
		Time:       platformEventTime(msg.Timestamp),
		SelfID:     selfID,
		MessageID:  msg.ID,
		RawMessage: text,
		Segments:   platformTextSegments(text, quoted),
		SenderName: firstNonEmpty(msg.Member.Nick, msg.Author.Username),
		ToMe:       true,
	}
	if quoted != "" {
		event.Quoted = &QuotedMessage{MessageID: quoted}
	}

	switch eventType {
	case "GROUP_AT_MESSAGE_CREATE":
		event.Kind = EventKindGroup
		event.MessageType = "group"
		event.GroupID = msg.GroupOpenID
		event.UserID = firstNonEmpty(msg.Author.MemberOpenID, msg.Author.ID)
	case "C2C_MESSAGE_CREATE":
		event.Kind = EventKindPrivate
		event.MessageType = "private"
		event.UserID = firstNonEmpty(msg.Author.UserOpenID, msg.Author.ID)
	case "AT_MESSAGE_CREATE":
		event.Kind = EventKindGroup
		event.MessageType = "group"
		event.GroupID = msg.ChannelID
		event.UserID = msg.Author.ID
	case "DIRECT_MESSAGE_CREATE":
		event.Kind = EventKindPrivate
		event.MessageType = "private"
		event.UserID = msg.Author.ID
	default:
		return MessageEvent{}, false
	}
	if event.UserID == "" {
		return MessageEvent{}, false
	}
	return event, true
}
