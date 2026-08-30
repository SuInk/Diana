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

// DingTalkConfig 是钉钉机器人的连接配置。
type DingTalkConfig struct {
	// ClientID 是钉钉开放平台应用的 AppKey / Client ID。
	ClientID string
	// ClientSecret 是对应的 AppSecret / Client Secret。
	ClientSecret string
	// RobotCode 缺省等于 ClientID，仅在企业内部机器人单独分配时才需要填。
	RobotCode string
}

const (
	dingTalkOpenAPIBase = "https://api.dingtalk.com"
	dingTalkGatewayURL  = dingTalkOpenAPIBase + "/v1.0/gateway/connections/open"
	dingTalkTokenURL    = dingTalkOpenAPIBase + "/v1.0/oauth2/accessToken"

	// dingTalkTopicRobotMessage 是「机器人收到消息」这个回调在 Stream 里的主题。
	dingTalkTopicRobotMessage = "/v1.0/im/bot/messages/get"
)

// DingTalkChannel 通过钉钉 Stream 模式接入。
//
// 钉钉的回调有 HTTP webhook 和 Stream 两种。这里用 Stream：本机向钉钉建一条
// 出站 WebSocket，事件顺着这条连接推下来，不需要公网地址、备案或证书，内网
// 部署也能跑。
type DingTalkChannel struct {
	mu      sync.RWMutex
	cfg     DingTalkConfig
	handler EventHandler
	client  *http.Client
	cancel  context.CancelFunc

	statusMu sync.RWMutex
	status   ChannelStatus

	tokens *platformTokenCache

	connMu sync.Mutex
	conn   *websocket.Conn

	// sessionWebhooks 保存每个会话最近一次的 sessionWebhook。
	//
	// 钉钉的被动回复走这个一次性地址，不消耗主动推送额度，也不需要额外权限；
	// 它有效期约 1 小时，所以按会话缓存、发完即用最近一条。
	webhookMu       sync.RWMutex
	sessionWebhooks map[string]dingTalkSessionWebhook
}

type dingTalkSessionWebhook struct {
	URL       string
	ExpiresAt time.Time
}

// dingTalkSessionWebhookTTL 是 sessionWebhook 的保守有效期。
const dingTalkSessionWebhookTTL = 55 * time.Minute

// NewDingTalkChannel 创建钉钉通道。
func NewDingTalkChannel(cfg DingTalkConfig) *DingTalkChannel {
	channel := &DingTalkChannel{
		cfg:             cfg,
		client:          &http.Client{Timeout: 30 * time.Second},
		status:          ChannelStatus{Endpoint: dingTalkOpenAPIBase + " (stream)", UpdatedAt: time.Now()},
		sessionWebhooks: map[string]dingTalkSessionWebhook{},
	}
	channel.tokens = &platformTokenCache{fetch: channel.fetchAccessToken}
	return channel
}

// SetConfig 更新连接配置并丢弃已缓存的 token。
func (c *DingTalkChannel) SetConfig(cfg DingTalkConfig) {
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
	c.tokens.Invalidate()
	c.setStatus(false, c.Status().SelfID, "")
}

func (c *DingTalkChannel) fetchAccessToken(ctx context.Context) (string, time.Duration, error) {
	c.mu.RLock()
	id := strings.TrimSpace(c.cfg.ClientID)
	secret := strings.TrimSpace(c.cfg.ClientSecret)
	client := c.client
	c.mu.RUnlock()
	if id == "" || secret == "" {
		return "", 0, fmt.Errorf("dingtalk: ClientID 和 ClientSecret 都必须配置")
	}
	raw, err := platformJSONRequest(ctx, client, http.MethodPost, dingTalkTokenURL, nil, map[string]string{
		"appKey":    id,
		"appSecret": secret,
	})
	if err != nil {
		return "", 0, fmt.Errorf("dingtalk: 换取 access token 失败: %w", err)
	}
	var payload struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.AccessToken == "" {
		return "", 0, fmt.Errorf("dingtalk: token 响应无效: %s", truncateForError(string(raw)))
	}
	ttl := time.Duration(payload.ExpireIn) * time.Second
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	return payload.AccessToken, ttl, nil
}

// Connect 建立 Stream 连接并持续接收事件，直到 ctx 取消。
func (c *DingTalkChannel) Connect(ctx context.Context, handler EventHandler) error {
	c.mu.Lock()
	if strings.TrimSpace(c.cfg.ClientID) == "" || strings.TrimSpace(c.cfg.ClientSecret) == "" {
		c.mu.Unlock()
		c.setStatus(false, "", "未配置钉钉 ClientID / ClientSecret")
		return fmt.Errorf("dingtalk: ClientID 和 ClientSecret 都必须配置")
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

// runSession 申请一个 Stream 端点并跑完这条连接。
func (c *DingTalkChannel) runSession(ctx context.Context) error {
	endpoint, err := c.openConnection(ctx)
	if err != nil {
		return err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("dingtalk: 连接 Stream 失败: %w", err)
	}
	defer conn.Close()
	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()
	c.setStatus(true, c.Status().SelfID, "")

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var frame dingTalkFrame
		if err := conn.ReadJSON(&frame); err != nil {
			return fmt.Errorf("dingtalk: Stream 连接中断: %w", err)
		}
		c.handleFrame(ctx, conn, frame)
	}
}

// openConnection 换取一个一次性的 Stream 接入地址。
func (c *DingTalkChannel) openConnection(ctx context.Context) (string, error) {
	c.mu.RLock()
	id := strings.TrimSpace(c.cfg.ClientID)
	secret := strings.TrimSpace(c.cfg.ClientSecret)
	client := c.client
	c.mu.RUnlock()
	raw, err := platformJSONRequest(ctx, client, http.MethodPost, dingTalkGatewayURL, nil, map[string]any{
		"clientId":     id,
		"clientSecret": secret,
		// 只订阅回调主题；不声明的话钉钉不会把机器人消息推过来。
		"subscriptions": []map[string]string{
			{"type": "CALLBACK", "topic": dingTalkTopicRobotMessage},
		},
		"ua": "diana-assistant",
	})
	if err != nil {
		return "", fmt.Errorf("dingtalk: 申请 Stream 端点失败: %w", err)
	}
	var payload struct {
		Endpoint string `json:"endpoint"`
		Ticket   string `json:"ticket"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Endpoint == "" {
		return "", fmt.Errorf("dingtalk: Stream 端点无效: %s", truncateForError(string(raw)))
	}
	return payload.Endpoint + "?ticket=" + payload.Ticket, nil
}

// handleFrame 分发一帧 Stream 报文。
//
// 钉钉要求每帧都回一个同 messageId 的 ACK，否则会重推同一条消息。
func (c *DingTalkChannel) handleFrame(ctx context.Context, conn *websocket.Conn, frame dingTalkFrame) {
	messageID := frame.Headers["messageId"]
	ack := func(body any) {
		if messageID == "" {
			return
		}
		response := map[string]any{
			"code": 200,
			"headers": map[string]string{
				"contentType": "application/json",
				"messageId":   messageID,
			},
		}
		if body == nil {
			body = map[string]any{}
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return
		}
		response["data"] = string(encoded)
		c.connMu.Lock()
		_ = conn.WriteJSON(response)
		c.connMu.Unlock()
	}

	switch frame.Type {
	case "SYSTEM":
		// 心跳和连接管理帧，回 ACK 即可。
		ack(nil)
		return
	case "CALLBACK":
		ack(nil)
	default:
		ack(nil)
		return
	}

	if frame.Headers["topic"] != dingTalkTopicRobotMessage {
		return
	}
	event, webhook, ok := dingTalkEventFromCallback([]byte(frame.Data), c.Status().SelfID)
	if !ok {
		return
	}
	if webhook != "" {
		c.rememberSessionWebhook(event, webhook)
	}
	c.mu.RLock()
	handler := c.handler
	c.mu.RUnlock()
	if handler == nil {
		return
	}
	_ = handler(ctx, event)
}

// rememberSessionWebhook 记下这个会话最近的被动回复地址。
func (c *DingTalkChannel) rememberSessionWebhook(event MessageEvent, webhook string) {
	key := dingTalkSessionKey(event.GroupID, event.UserID)
	if key == "" {
		return
	}
	c.webhookMu.Lock()
	c.sessionWebhooks[key] = dingTalkSessionWebhook{URL: webhook, ExpiresAt: time.Now().Add(dingTalkSessionWebhookTTL)}
	c.webhookMu.Unlock()
}

func (c *DingTalkChannel) lookupSessionWebhook(groupID, userID string) string {
	key := dingTalkSessionKey(groupID, userID)
	if key == "" {
		return ""
	}
	c.webhookMu.RLock()
	entry, ok := c.sessionWebhooks[key]
	c.webhookMu.RUnlock()
	if !ok || time.Now().After(entry.ExpiresAt) {
		return ""
	}
	return entry.URL
}

// dingTalkSessionKey 群聊按会话号、单聊按用户号索引。
func dingTalkSessionKey(groupID, userID string) string {
	if group := strings.TrimSpace(groupID); group != "" {
		return "g:" + group
	}
	if user := strings.TrimSpace(userID); user != "" {
		return "u:" + user
	}
	return ""
}

// Send 回复消息。
//
// 优先用会话自带的 sessionWebhook：它是被动回复，不占主动推送额度，也不需要
// 「机器人主动发消息」那项权限。地址过期或缺失时回落到 OpenAPI 主动推送。
func (c *DingTalkChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	text := platformOutboundText(msg)
	if text == "" {
		return nil
	}
	if webhook := c.lookupSessionWebhook(msg.GroupID, msg.UserID); webhook != "" {
		c.mu.RLock()
		client := c.client
		c.mu.RUnlock()
		_, err := platformJSONRequest(ctx, client, http.MethodPost, webhook, nil, map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": text},
		})
		if err == nil {
			return nil
		}
		// 会话地址失效时不直接失败，掉头走主动推送。
		c.webhookMu.Lock()
		delete(c.sessionWebhooks, dingTalkSessionKey(msg.GroupID, msg.UserID))
		c.webhookMu.Unlock()
	}
	return c.sendViaOpenAPI(ctx, msg, text)
}

// sendViaOpenAPI 走主动推送接口。
func (c *DingTalkChannel) sendViaOpenAPI(ctx context.Context, msg OutgoingMessage, text string) error {
	token, err := c.tokens.Get(ctx)
	if err != nil {
		return err
	}
	c.mu.RLock()
	client := c.client
	robotCode := firstNonEmpty(strings.TrimSpace(c.cfg.RobotCode), strings.TrimSpace(c.cfg.ClientID))
	c.mu.RUnlock()

	content, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return err
	}
	endpoint := dingTalkOpenAPIBase + "/v1.0/robot/oToMessages/batchSend"
	body := map[string]any{
		"robotCode": robotCode,
		"userIds":   []string{strings.TrimSpace(msg.UserID)},
		"msgKey":    "sampleText",
		"msgParam":  string(content),
	}
	if group := strings.TrimSpace(msg.GroupID); group != "" {
		endpoint = dingTalkOpenAPIBase + "/v1.0/robot/groupMessages/send"
		body = map[string]any{
			"robotCode":          robotCode,
			"openConversationId": group,
			"msgKey":             "sampleText",
			"msgParam":           string(content),
		}
	}
	raw, err := platformJSONRequest(ctx, client, http.MethodPost, endpoint, map[string]string{
		"x-acs-dingtalk-access-token": token,
	}, body)
	if err != nil {
		if strings.Contains(err.Error(), "http 401") {
			c.tokens.Invalidate()
		}
		return fmt.Errorf("dingtalk: 发送失败: %w", err)
	}
	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Code != "" {
		return fmt.Errorf("dingtalk: 发送被拒绝: %s (%s)", envelope.Message, envelope.Code)
	}
	return nil
}

// CallAPI 透传钉钉 OpenAPI，action 形如 "POST /v1.0/im/sessionWebhooks"。
func (c *DingTalkChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	method, path := http.MethodGet, strings.TrimSpace(action)
	if fields := strings.SplitN(path, " ", 2); len(fields) == 2 {
		method, path = strings.ToUpper(fields[0]), strings.TrimSpace(fields[1])
	}
	if path == "" {
		return nil, fmt.Errorf("dingtalk: 缺少接口路径")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	token, err := c.tokens.Get(ctx)
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
	raw, err := platformJSONRequest(ctx, client, method, dingTalkOpenAPIBase+path, map[string]string{
		"x-acs-dingtalk-access-token": token,
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
func (c *DingTalkChannel) Status() ChannelStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.status
}

func (c *DingTalkChannel) setStatus(connected bool, selfID, lastErr string) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	c.status.Connected = connected
	if selfID != "" {
		c.status.SelfID = selfID
	}
	c.status.LastError = lastErr
	c.status.UpdatedAt = time.Now()
}

// Close 断开 Stream 连接。
func (c *DingTalkChannel) Close() error {
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

// —— Stream 协议 ——

type dingTalkFrame struct {
	SpecVersion string            `json:"specVersion"`
	Type        string            `json:"type"`
	Headers     map[string]string `json:"headers"`
	Data        string            `json:"data"`
}

// dingTalkCallback 是机器人消息回调的报文。
type dingTalkCallback struct {
	MsgID                     string `json:"msgId"`
	MsgType                   string `json:"msgtype"`
	CreateAt                  any    `json:"createAt"`
	ConversationType          string `json:"conversationType"`
	ConversationID            string `json:"conversationId"`
	ConversationTitle         string `json:"conversationTitle"`
	SenderID                  string `json:"senderId"`
	SenderNick                string `json:"senderNick"`
	SenderStaffID             string `json:"senderStaffId"`
	IsAdmin                   bool   `json:"isAdmin"`
	SessionWebhook            string `json:"sessionWebhook"`
	SessionWebhookExpiredTime int64  `json:"sessionWebhookExpiredTime"`
	Text                      struct {
		Content string `json:"content"`
	} `json:"text"`
}

// dingTalkEventFromCallback 把回调报文映射成统一事件。
//
// 语义对照：
//   - conversationType "1" 单聊 -> 私聊；"2" 群聊 -> 群聊，conversationId 当群号
//   - senderStaffId 是企业内唯一的员工号，优先用它当用户 ID；外部联系人只有
//     senderId，这时回落到 senderId
//   - 钉钉只把 @ 了机器人的群消息推过来，所以收到即 ToMe
func dingTalkEventFromCallback(data []byte, selfID string) (MessageEvent, string, bool) {
	var callback dingTalkCallback
	if err := json.Unmarshal(data, &callback); err != nil {
		return MessageEvent{}, "", false
	}
	// 目前只处理文本消息；图片、语音等富媒体钉钉给的是下载码，需要另换取。
	if callback.MsgType != "" && callback.MsgType != "text" {
		return MessageEvent{}, "", false
	}
	text := strings.TrimSpace(callback.Text.Content)
	if text == "" {
		return MessageEvent{}, "", false
	}
	userID := firstNonEmpty(strings.TrimSpace(callback.SenderStaffID), strings.TrimSpace(callback.SenderID))
	if userID == "" {
		return MessageEvent{}, "", false
	}

	event := MessageEvent{
		Time:       platformEventTime(callback.CreateAt),
		SelfID:     selfID,
		MessageID:  callback.MsgID,
		RawMessage: text,
		Segments:   platformTextSegments(text, ""),
		UserID:     userID,
		SenderName: callback.SenderNick,
		ToMe:       true,
	}
	if callback.IsAdmin {
		event.SenderRole = "admin"
	}
	if callback.ConversationType == "2" {
		event.Kind = EventKindGroup
		event.MessageType = "group"
		event.GroupID = callback.ConversationID
	} else {
		event.Kind = EventKindPrivate
		event.MessageType = "private"
	}
	return event, strings.TrimSpace(callback.SessionWebhook), true
}
