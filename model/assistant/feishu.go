// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// FeishuConfig 是飞书自建应用的连接配置。
type FeishuConfig struct {
	// ProfileID 用于把回调路由到正确的通道实例。
	ProfileID string
	// AppID / AppSecret 来自开放平台的自建应用凭证。
	AppID     string
	AppSecret string
	// VerificationToken 是事件订阅里的 Verification Token，用于校验回调来源。
	VerificationToken string
	// EncryptKey 配置了加密推送时必填，留空表示明文推送。
	EncryptKey string
	// APIBaseURL 留空用飞书；Lark 国际版填 https://open.larksuite.com。
	APIBaseURL string
}

const (
	feishuDefaultAPIBase = "https://open.feishu.cn"
	feishuTokenPath      = "/open-apis/auth/v3/tenant_access_token/internal"
	feishuMessagePath    = "/open-apis/im/v1/messages"
	feishuCallbackLimit  = 4 << 20
)

// FeishuChannel 通过事件订阅回调接入飞书。
//
// 飞书另有一种 WebSocket 长连接模式，但它走的是未公开的私有协议、只有官方 SDK
// 实现；这里用公开的事件订阅回调，代价是需要一个公网可达的 HTTPS 地址填到
// 开放平台后台。
type FeishuChannel struct {
	mu      sync.RWMutex
	cfg     FeishuConfig
	handler EventHandler
	client  *http.Client
	cancel  context.CancelFunc

	statusMu sync.RWMutex
	status   ChannelStatus

	tokens *platformTokenCache

	// seenEvents 去重。飞书在没收到 200 时会重推同一个事件，重复投递会让机器人
	// 把同一条消息回答两遍。
	dedupe *eventDeduper
}

// NewFeishuChannel 创建飞书通道。
func NewFeishuChannel(cfg FeishuConfig) *FeishuChannel {
	channel := &FeishuChannel{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
		status: ChannelStatus{Endpoint: feishuEndpointLabel(cfg), UpdatedAt: time.Now()},
		dedupe: newEventDeduper(10 * time.Minute),
	}
	channel.tokens = &platformTokenCache{fetch: channel.fetchTenantAccessToken}
	return channel
}

func feishuEndpointLabel(cfg FeishuConfig) string {
	return feishuAPIBase(cfg) + " (event callback " + FeishuCallbackPath + ")"
}

func feishuAPIBase(cfg FeishuConfig) string {
	if base := strings.TrimSpace(cfg.APIBaseURL); base != "" {
		return strings.TrimRight(base, "/")
	}
	return feishuDefaultAPIBase
}

// SetConfig 更新配置并丢弃已缓存的 token。
func (c *FeishuChannel) SetConfig(cfg FeishuConfig) {
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
	c.tokens.Invalidate()
	c.statusMu.Lock()
	c.status.Endpoint = feishuEndpointLabel(cfg)
	c.statusMu.Unlock()
}

func (c *FeishuChannel) fetchTenantAccessToken(ctx context.Context) (string, time.Duration, error) {
	c.mu.RLock()
	cfg := c.cfg
	client := c.client
	c.mu.RUnlock()
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" {
		return "", 0, fmt.Errorf("feishu: App ID 和 App Secret 都必须配置")
	}
	raw, err := platformJSONRequest(ctx, client, http.MethodPost, feishuAPIBase(cfg)+feishuTokenPath, nil, map[string]string{
		"app_id":     strings.TrimSpace(cfg.AppID),
		"app_secret": strings.TrimSpace(cfg.AppSecret),
	})
	if err != nil {
		return "", 0, fmt.Errorf("feishu: 换取 tenant access token 失败: %w", err)
	}
	var payload struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int64  `json:"expire"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", 0, fmt.Errorf("feishu: 解析 token 响应失败: %w", err)
	}
	if payload.Code != 0 || payload.TenantAccessToken == "" {
		return "", 0, fmt.Errorf("feishu: 换取 token 被拒绝: %s (code %d)", payload.Msg, payload.Code)
	}
	ttl := time.Duration(payload.Expire) * time.Second
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	return payload.TenantAccessToken, ttl, nil
}

// Connect 登记回调处理器并保持在线，直到 ctx 取消。
//
// 回调是被动接收，这里没有需要维持的连接；启动时用一次 token 请求确认凭据可用，
// 让「配置错了」立刻在状态里暴露出来，而不是等第一条消息进来才失败。
func (c *FeishuChannel) Connect(ctx context.Context, handler EventHandler) error {
	c.mu.Lock()
	cfg := c.cfg
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" {
		c.mu.Unlock()
		c.setStatus(false, "", "未配置飞书 App ID / App Secret")
		return fmt.Errorf("feishu: App ID 和 App Secret 都必须配置")
	}
	c.handler = handler
	c.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	defer cancel()

	RegisterCallbackHandler(PlatformFeishu, cfg.ProfileID, http.HandlerFunc(c.ServeCallback))
	defer UnregisterCallbackHandler(PlatformFeishu, cfg.ProfileID)

	if _, err := c.tokens.Get(runCtx); err != nil {
		c.setStatus(false, "", err.Error())
		return err
	}
	c.setStatus(true, strings.TrimSpace(cfg.AppID), "")
	<-runCtx.Done()
	c.setStatus(false, c.Status().SelfID, "")
	return runCtx.Err()
}

// ServeCallback 处理飞书推来的事件回调。
func (c *FeishuChannel) ServeCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, feishuCallbackLimit))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	c.mu.RLock()
	cfg := c.cfg
	handler := c.handler
	c.mu.RUnlock()

	// 加密推送时飞书会带签名头。校验它能挡住「拿到回调地址就能伪造事件」这类
	// 情况——地址本身是公开的，不能当凭据用。
	if key := strings.TrimSpace(cfg.EncryptKey); key != "" {
		if signature := strings.TrimSpace(r.Header.Get("X-Lark-Signature")); signature != "" {
			expected := feishuSignature(
				r.Header.Get("X-Lark-Request-Timestamp"),
				r.Header.Get("X-Lark-Request-Nonce"),
				key,
				string(body),
			)
			if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
				http.Error(w, "signature mismatch", http.StatusUnauthorized)
				return
			}
		}
	}

	plaintext, err := feishuDecodeCallback(body, cfg.EncryptKey)
	if err != nil {
		c.setStatus(c.Status().Connected, c.Status().SelfID, err.Error())
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	// URL 校验：配置回调地址时飞书会先发一次 challenge，必须原样回显。
	var probe struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Token     string `json:"token"`
		Header    struct {
			Token     string `json:"token"`
			EventType string `json:"event_type"`
			EventID   string `json:"event_id"`
			CreateAt  any    `json:"create_time"`
		} `json:"header"`
	}
	if err := json.Unmarshal(plaintext, &probe); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	// Verification Token 是飞书唯一的来源证明（明文推送时尤其重要），配置了就必须核对。
	if expected := strings.TrimSpace(cfg.VerificationToken); expected != "" {
		got := firstNonEmpty(strings.TrimSpace(probe.Header.Token), strings.TrimSpace(probe.Token))
		if subtle.ConstantTimeCompare([]byte(expected), []byte(got)) != 1 {
			http.Error(w, "verification token mismatch", http.StatusUnauthorized)
			return
		}
	}

	if probe.Type == "url_verification" {
		writeJSON(w, map[string]string{"challenge": probe.Challenge})
		return
	}

	// 先回 200 再处理：飞书要求 3 秒内响应，超时会重推。
	writeJSON(w, map[string]any{"code": 0})

	if probe.Header.EventType != "im.message.receive_v1" {
		return
	}
	if id := strings.TrimSpace(probe.Header.EventID); id != "" && !c.dedupe.Accept(id) {
		return
	}
	event, ok := feishuEventFromCallback(plaintext, strings.TrimSpace(cfg.AppID))
	if !ok || handler == nil {
		return
	}
	// 回调协程不能持有请求的 ctx——响应已经写回去了，ctx 随即取消。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_ = handler(ctx, event)
	}()
}

// Send 发送消息。
func (c *FeishuChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	text := platformOutboundText(msg)
	if text == "" {
		return nil
	}
	target, isGroup := platformChatTarget(msg)
	if target == "" {
		return fmt.Errorf("feishu: 缺少会话标识")
	}
	token, err := c.tokens.Get(ctx)
	if err != nil {
		return err
	}
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	c.mu.RLock()
	cfg := c.cfg
	client := c.client
	c.mu.RUnlock()

	base := feishuAPIBase(cfg)
	endpoint := base + feishuMessagePath + "?receive_id_type=" + feishuReceiveIDType(target, isGroup)
	body := map[string]any{
		"receive_id": target,
		"msg_type":   "text",
		"content":    string(content),
	}
	// 有被回复的消息时走 reply 接口，回复会挂在原消息下面形成话题。
	if replyID := strings.TrimSpace(msg.ReplyMessageID); replyID != "" {
		endpoint = base + feishuMessagePath + "/" + replyID + "/reply"
		body = map[string]any{"msg_type": "text", "content": string(content)}
	}

	raw, err := platformJSONRequest(ctx, client, http.MethodPost, endpoint, map[string]string{
		"Authorization": "Bearer " + token,
	}, body)
	if err != nil {
		if strings.Contains(err.Error(), "http 401") {
			c.tokens.Invalidate()
		}
		return fmt.Errorf("feishu: 发送失败: %w", err)
	}
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Code != 0 {
		return fmt.Errorf("feishu: 发送被拒绝: %s (code %d)", envelope.Msg, envelope.Code)
	}
	return nil
}

// feishuReceiveIDType 按标识前缀判断该用哪种 receive_id_type。
//
// 飞书的 ID 自带前缀：oc_ 是会话、ou_ 是用户 open_id、on_ 是 union_id。传错类型
// 会直接被拒，所以这里按前缀选，而不是按「群聊还是私聊」猜。
func feishuReceiveIDType(id string, isGroup bool) string {
	switch {
	case strings.HasPrefix(id, "oc_"):
		return "chat_id"
	case strings.HasPrefix(id, "ou_"):
		return "open_id"
	case strings.HasPrefix(id, "on_"):
		return "union_id"
	case strings.Contains(id, "@"):
		return "email"
	case isGroup:
		return "chat_id"
	default:
		return "user_id"
	}
}

// CallAPI 透传飞书开放接口，action 形如 "GET /open-apis/im/v1/chats"。
func (c *FeishuChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	method, path := http.MethodGet, strings.TrimSpace(action)
	if fields := strings.SplitN(path, " ", 2); len(fields) == 2 {
		method, path = strings.ToUpper(fields[0]), strings.TrimSpace(fields[1])
	}
	if path == "" {
		return nil, fmt.Errorf("feishu: 缺少接口路径")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	token, err := c.tokens.Get(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.RLock()
	cfg := c.cfg
	client := c.client
	c.mu.RUnlock()
	var payload any
	if len(params) > 0 && method != http.MethodGet {
		payload = params
	}
	raw, err := platformJSONRequest(ctx, client, method, feishuAPIBase(cfg)+path, map[string]string{
		"Authorization": "Bearer " + token,
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
func (c *FeishuChannel) Status() ChannelStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.status
}

func (c *FeishuChannel) setStatus(connected bool, selfID, lastErr string) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	c.status.Connected = connected
	if selfID != "" {
		c.status.SelfID = selfID
	}
	c.status.LastError = lastErr
	c.status.UpdatedAt = time.Now()
}

// Close 注销回调并停止。
func (c *FeishuChannel) Close() error {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	profileID := c.cfg.ProfileID
	c.mu.Unlock()
	UnregisterCallbackHandler(PlatformFeishu, profileID)
	if cancel != nil {
		cancel()
	}
	c.setStatus(false, c.Status().SelfID, "")
	return nil
}

// —— 回调解码 ——

// feishuDecodeCallback 还原回调正文，必要时解密。
func feishuDecodeCallback(body []byte, encryptKey string) ([]byte, error) {
	var envelope struct {
		Encrypt string `json:"encrypt"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("feishu: 回调不是合法 JSON")
	}
	if envelope.Encrypt == "" {
		if strings.TrimSpace(encryptKey) != "" {
			// 配置了 Encrypt Key 却收到明文，说明来源可疑或后台没同步开启加密。
			return nil, fmt.Errorf("feishu: 已配置 Encrypt Key 但收到明文回调")
		}
		return body, nil
	}
	if strings.TrimSpace(encryptKey) == "" {
		return nil, fmt.Errorf("feishu: 收到加密回调但未配置 Encrypt Key")
	}
	return feishuDecrypt(envelope.Encrypt, encryptKey)
}

// feishuDecrypt 解开飞书的 AES-256-CBC 密文。
//
// 密钥是 Encrypt Key 的 SHA-256，密文 base64 解码后前 16 字节是 IV。
func feishuDecrypt(encrypted, encryptKey string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, fmt.Errorf("feishu: 密文不是合法 base64")
	}
	if len(raw) <= aes.BlockSize || len(raw)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("feishu: 密文长度非法")
	}
	sum := sha256.Sum256([]byte(encryptKey))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	iv, payload := raw[:aes.BlockSize], raw[aes.BlockSize:]
	plaintext := make([]byte, len(payload))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, payload)
	return stripPKCS7(plaintext)
}

// feishuSignature 计算事件订阅的签名，用于校验加密回调。
func feishuSignature(timestamp, nonce, encryptKey, body string) string {
	sum := sha256.Sum256([]byte(timestamp + nonce + encryptKey + body))
	return hex.EncodeToString(sum[:])
}

// —— 事件映射 ——

// feishuEventFromCallback 把 im.message.receive_v1 映射成统一事件。
//
// 语义对照：
//   - chat_type "p2p" -> 私聊；"group" -> 群聊，chat_id 当群号
//   - sender.sender_id.open_id -> 用户 ID；飞书的 open_id 在同一应用内稳定
//   - ToMe 由 mentions 里有没有本应用判断；私聊天然为真
func feishuEventFromCallback(payload []byte, appID string) (MessageEvent, bool) {
	var callback struct {
		Header struct {
			EventID    string `json:"event_id"`
			CreateTime any    `json:"create_time"`
		} `json:"header"`
		Event struct {
			Sender struct {
				SenderID struct {
					OpenID  string `json:"open_id"`
					UserID  string `json:"user_id"`
					UnionID string `json:"union_id"`
				} `json:"sender_id"`
				SenderType string `json:"sender_type"`
			} `json:"sender"`
			Message struct {
				MessageID   string          `json:"message_id"`
				RootID      string          `json:"root_id"`
				ParentID    string          `json:"parent_id"`
				CreateTime  any             `json:"create_time"`
				ChatID      string          `json:"chat_id"`
				ChatType    string          `json:"chat_type"`
				MessageType string          `json:"message_type"`
				Content     string          `json:"content"`
				Mentions    []feishuMention `json:"mentions"`
			} `json:"message"`
		} `json:"event"`
	}
	if err := json.Unmarshal(payload, &callback); err != nil {
		return MessageEvent{}, false
	}
	message := callback.Event.Message
	// 只处理文本；富媒体飞书给的是 image_key/file_key，要另换取下载地址。
	if message.MessageType != "text" {
		return MessageEvent{}, false
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(message.Content), &content); err != nil {
		return MessageEvent{}, false
	}
	// 群里 @ 机器人会在正文留下 @_user_1 这样的占位符，按 mentions 还原成人名，
	// 不然模型看到的是一串没有意义的记号。
	text := strings.TrimSpace(feishuResolveMentionPlaceholders(content.Text, callback.Event.Message.Mentions))
	if text == "" {
		return MessageEvent{}, false
	}
	userID := firstNonEmpty(
		strings.TrimSpace(callback.Event.Sender.SenderID.OpenID),
		strings.TrimSpace(callback.Event.Sender.SenderID.UserID),
		strings.TrimSpace(callback.Event.Sender.SenderID.UnionID),
	)
	if userID == "" {
		return MessageEvent{}, false
	}

	quoted := strings.TrimSpace(message.ParentID)
	event := MessageEvent{
		Time:       platformEventTime(firstParsableEventTime(message.CreateTime, callback.Header.CreateTime)),
		SelfID:     appID,
		MessageID:  message.MessageID,
		RawMessage: text,
		Segments:   platformTextSegments(text, quoted),
		UserID:     userID,
	}
	if quoted != "" {
		event.Quoted = &QuotedMessage{MessageID: quoted}
	}

	if message.ChatType == "p2p" {
		event.Kind = EventKindPrivate
		event.MessageType = "private"
		event.ToMe = true
	} else {
		event.Kind = EventKindGroup
		event.MessageType = "group"
		event.GroupID = message.ChatID
		// 群里只有被 @ 才算点名。飞书把机器人自己的提及也放在 mentions 里，
		// 但它给的是 open_id，和 app_id 不是一回事——凡是有提及占位符且不属于
		// 其他人的，交给上层的回复闸门再判断一次。
		for _, mention := range message.Mentions {
			if strings.TrimSpace(mention.ID.OpenID) != "" && mention.ID.OpenID != userID {
				event.ToMe = true
				break
			}
		}
	}
	return event, true
}

// feishuMention 是消息里的一处提及。Key 是正文中的占位符（形如 @_user_1）。
type feishuMention struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	TenantKey string `json:"tenant_key"`
	ID        struct {
		OpenID string `json:"open_id"`
		UserID string `json:"user_id"`
	} `json:"id"`
}

// feishuResolveMentionPlaceholders 把 @_user_N 占位符换成对应的显示名。
func feishuResolveMentionPlaceholders(text string, mentions []feishuMention) string {
	for _, mention := range mentions {
		key := strings.TrimSpace(mention.Key)
		if key == "" {
			continue
		}
		name := strings.TrimSpace(mention.Name)
		if name == "" {
			name = "某人"
		}
		text = strings.ReplaceAll(text, key, "@"+name)
	}
	return text
}

// firstParsableEventTime 返回第一个能解析出非零秒数的时间值。
func firstParsableEventTime(values ...any) any {
	for _, value := range values {
		if platformEventTime(value) > 0 {
			return value
		}
	}
	return nil
}

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// stripPKCS7 去掉 PKCS#7 填充。
func stripPKCS7(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("assistant: 空的明文")
	}
	padding := int(data[len(data)-1])
	if padding <= 0 || padding > aes.BlockSize || padding > len(data) {
		return nil, fmt.Errorf("assistant: 填充长度非法")
	}
	// 逐字节核对填充，防止把正常数据当填充截掉。
	for _, b := range data[len(data)-padding:] {
		if int(b) != padding {
			return nil, fmt.Errorf("assistant: 填充内容非法")
		}
	}
	return data[:len(data)-padding], nil
}

// —— 事件去重 ——

// eventDeduper 记住最近处理过的事件 ID。
type eventDeduper struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
}

func newEventDeduper(ttl time.Duration) *eventDeduper {
	return &eventDeduper{ttl: ttl, seen: map[string]time.Time{}}
}

// Accept 返回这个事件是否是第一次见到。
func (d *eventDeduper) Accept(id string) bool {
	if d == nil || strings.TrimSpace(id) == "" {
		return true
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if seenAt, ok := d.seen[id]; ok && now.Sub(seenAt) < d.ttl {
		return false
	}
	// 顺手清掉过期条目，避免长期运行后无限增长。
	for key, seenAt := range d.seen {
		if now.Sub(seenAt) >= d.ttl {
			delete(d.seen, key)
		}
	}
	d.seen[id] = now
	return true
}
