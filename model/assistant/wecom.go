// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WeComConfig 是企业微信自建应用的连接配置。
type WeComConfig struct {
	// ProfileID 用于把回调路由到正确的通道实例。
	ProfileID string
	// CorpID 是企业 ID。
	CorpID string
	// AgentID 是自建应用的 AgentId，发消息时必填。
	AgentID string
	// Secret 是应用的 Secret，用于换取 access token。
	Secret string
	// Token / EncodingAESKey 来自应用的「接收消息」回调配置。
	Token          string
	EncodingAESKey string
}

const (
	weComAPIBase        = "https://qyapi.weixin.qq.com"
	weComCallbackLimit  = 4 << 20
	weComAESKeyRawBytes = 32
)

// WeComChannel 通过应用回调接入企业微信。
//
// 企业微信没有出站长连接模式：消息只能由它 POST 到本机，所以必须有一个公网可达
// 的 HTTPS 地址填到应用的「接收消息」配置里。报文按 WXBizMsgCrypt 规范加密，
// 密钥和签名都在这里校验。
type WeComChannel struct {
	mu      sync.RWMutex
	cfg     WeComConfig
	handler EventHandler
	client  *http.Client
	cancel  context.CancelFunc

	statusMu sync.RWMutex
	status   ChannelStatus

	tokens *platformTokenCache
	dedupe *eventDeduper
}

// NewWeComChannel 创建企业微信通道。
func NewWeComChannel(cfg WeComConfig) *WeComChannel {
	channel := &WeComChannel{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
		status: ChannelStatus{Endpoint: weComAPIBase + " (callback " + WeComCallbackPath + ")", UpdatedAt: time.Now()},
		dedupe: newEventDeduper(10 * time.Minute),
	}
	channel.tokens = &platformTokenCache{fetch: channel.fetchAccessToken}
	return channel
}

// SetConfig 更新配置并丢弃已缓存的 token。
func (c *WeComChannel) SetConfig(cfg WeComConfig) {
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
	c.tokens.Invalidate()
}

func (c *WeComChannel) fetchAccessToken(ctx context.Context) (string, time.Duration, error) {
	c.mu.RLock()
	corpID := strings.TrimSpace(c.cfg.CorpID)
	secret := strings.TrimSpace(c.cfg.Secret)
	client := c.client
	c.mu.RUnlock()
	if corpID == "" || secret == "" {
		return "", 0, fmt.Errorf("wecom: 企业 ID 和应用 Secret 都必须配置")
	}
	endpoint := weComAPIBase + "/cgi-bin/gettoken?corpid=" + url.QueryEscape(corpID) + "&corpsecret=" + url.QueryEscape(secret)
	raw, err := platformJSONRequest(ctx, client, http.MethodGet, endpoint, nil, nil)
	if err != nil {
		return "", 0, fmt.Errorf("wecom: 换取 access token 失败: %w", err)
	}
	var payload struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", 0, fmt.Errorf("wecom: 解析 token 响应失败: %w", err)
	}
	if payload.ErrCode != 0 || payload.AccessToken == "" {
		return "", 0, fmt.Errorf("wecom: 换取 token 被拒绝: %s (errcode %d)", payload.ErrMsg, payload.ErrCode)
	}
	ttl := time.Duration(payload.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	return payload.AccessToken, ttl, nil
}

// Connect 登记回调处理器并保持在线，直到 ctx 取消。
func (c *WeComChannel) Connect(ctx context.Context, handler EventHandler) error {
	c.mu.Lock()
	cfg := c.cfg
	if strings.TrimSpace(cfg.CorpID) == "" || strings.TrimSpace(cfg.Secret) == "" {
		c.mu.Unlock()
		c.setStatus(false, "", "未配置企业微信 企业ID / 应用 Secret")
		return fmt.Errorf("wecom: 企业 ID 和应用 Secret 都必须配置")
	}
	c.handler = handler
	c.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	defer cancel()

	RegisterCallbackHandler(PlatformWeCom, cfg.ProfileID, http.HandlerFunc(c.ServeCallback))
	defer UnregisterCallbackHandler(PlatformWeCom, cfg.ProfileID)

	if _, err := c.tokens.Get(runCtx); err != nil {
		c.setStatus(false, "", err.Error())
		return err
	}
	c.setStatus(true, strings.TrimSpace(cfg.AgentID), "")
	<-runCtx.Done()
	c.setStatus(false, c.Status().SelfID, "")
	return runCtx.Err()
}

// ServeCallback 处理企业微信推来的回调。
//
// GET 是配置回调地址时的一次性 URL 校验，POST 是真正的消息推送，两者都要验签。
func (c *WeComChannel) ServeCallback(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	cfg := c.cfg
	handler := c.handler
	c.mu.RUnlock()

	query := r.URL.Query()
	signature := query.Get("msg_signature")
	timestamp := query.Get("timestamp")
	nonce := query.Get("nonce")

	if r.Method == http.MethodGet {
		echo := query.Get("echostr")
		if !weComSignatureValid(cfg.Token, timestamp, nonce, echo, signature) {
			http.Error(w, "signature mismatch", http.StatusUnauthorized)
			return
		}
		plaintext, _, err := weComDecrypt(echo, cfg.EncodingAESKey)
		if err != nil {
			http.Error(w, "decrypt failed", http.StatusBadRequest)
			return
		}
		// URL 校验要求原样回显解密后的明文，不能加任何包装。
		_, _ = w.Write(plaintext)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, weComCallbackLimit))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	var envelope struct {
		ToUserName string `xml:"ToUserName"`
		AgentID    string `xml:"AgentID"`
		Encrypt    string `xml:"Encrypt"`
	}
	if err := xml.Unmarshal(body, &envelope); err != nil || envelope.Encrypt == "" {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if !weComSignatureValid(cfg.Token, timestamp, nonce, envelope.Encrypt, signature) {
		http.Error(w, "signature mismatch", http.StatusUnauthorized)
		return
	}
	plaintext, receiveID, err := weComDecrypt(envelope.Encrypt, cfg.EncodingAESKey)
	if err != nil {
		c.setStatus(c.Status().Connected, c.Status().SelfID, err.Error())
		http.Error(w, "decrypt failed", http.StatusBadRequest)
		return
	}
	// 明文尾部的 receiveid 必须是本企业，否则是发错地方或伪造的报文。
	if corpID := strings.TrimSpace(cfg.CorpID); corpID != "" && receiveID != "" && receiveID != corpID {
		http.Error(w, "corp id mismatch", http.StatusUnauthorized)
		return
	}

	// 空响应就是「已收到，无被动回复」，企业微信认这个。回复走主动发送接口，
	// 因为生成回答要花的时间远超被动回复那 5 秒窗口。
	w.WriteHeader(http.StatusOK)

	event, ok := weComEventFromCallback(plaintext, strings.TrimSpace(cfg.AgentID))
	if !ok || handler == nil {
		return
	}
	if id := strings.TrimSpace(event.MessageID); id != "" && !c.dedupe.Accept(id) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_ = handler(ctx, event)
	}()
}

// Send 通过应用消息接口发送。
func (c *WeComChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	text := platformOutboundText(msg)
	if text == "" {
		return nil
	}
	token, err := c.tokens.Get(ctx)
	if err != nil {
		return err
	}
	c.mu.RLock()
	cfg := c.cfg
	client := c.client
	c.mu.RUnlock()

	agentID, err := strconv.Atoi(strings.TrimSpace(cfg.AgentID))
	if err != nil {
		return fmt.Errorf("wecom: AgentId 必须是数字")
	}
	body := map[string]any{
		"msgtype": "text",
		"agentid": agentID,
		"text":    map[string]string{"content": text},
	}
	if group := strings.TrimSpace(msg.GroupID); group != "" {
		// 企业微信的应用消息发给群时用 chatid，走的是另一个接口。
		body["chatid"] = group
		return c.postSend(ctx, client, weComAPIBase+"/cgi-bin/appchat/send?access_token="+url.QueryEscape(token), body)
	}
	user := strings.TrimSpace(msg.UserID)
	if user == "" {
		return fmt.Errorf("wecom: 缺少接收人")
	}
	body["touser"] = user
	return c.postSend(ctx, client, weComAPIBase+"/cgi-bin/message/send?access_token="+url.QueryEscape(token), body)
}

func (c *WeComChannel) postSend(ctx context.Context, client *http.Client, endpoint string, body map[string]any) error {
	raw, err := platformJSONRequest(ctx, client, http.MethodPost, endpoint, nil, body)
	if err != nil {
		return fmt.Errorf("wecom: 发送失败: %w", err)
	}
	var envelope struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.ErrCode != 0 {
		// 42001 是 token 过期，丢缓存让下一次重新换。
		if envelope.ErrCode == 42001 || envelope.ErrCode == 40014 {
			c.tokens.Invalidate()
		}
		return fmt.Errorf("wecom: 发送被拒绝: %s (errcode %d)", envelope.ErrMsg, envelope.ErrCode)
	}
	return nil
}

// CallAPI 透传企业微信接口，action 形如 "GET /cgi-bin/user/get"。
func (c *WeComChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	method, path := http.MethodGet, strings.TrimSpace(action)
	if fields := strings.SplitN(path, " ", 2); len(fields) == 2 {
		method, path = strings.ToUpper(fields[0]), strings.TrimSpace(fields[1])
	}
	if path == "" {
		return nil, fmt.Errorf("wecom: 缺少接口路径")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	token, err := c.tokens.Get(ctx)
	if err != nil {
		return nil, err
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	var payload any
	if len(params) > 0 && method != http.MethodGet {
		payload = params
	}
	raw, err := platformJSONRequest(ctx, client, method, weComAPIBase+path+separator+"access_token="+url.QueryEscape(token), nil, payload)
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
func (c *WeComChannel) Status() ChannelStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.status
}

func (c *WeComChannel) setStatus(connected bool, selfID, lastErr string) {
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
func (c *WeComChannel) Close() error {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	profileID := c.cfg.ProfileID
	c.mu.Unlock()
	UnregisterCallbackHandler(PlatformWeCom, profileID)
	if cancel != nil {
		cancel()
	}
	c.setStatus(false, c.Status().SelfID, "")
	return nil
}

// —— WXBizMsgCrypt ——

// weComSignatureValid 校验 msg_signature。
//
// 规则是把 token、timestamp、nonce、密文四项字典序排序后拼接取 SHA-1。
func weComSignatureValid(token, timestamp, nonce, encrypted, signature string) bool {
	if strings.TrimSpace(token) == "" {
		// 没配 Token 就没有验签依据，直接拒绝——这条链路是公网可达的，
		// 不能在缺少凭据时放行。
		return false
	}
	expected := weComSignature(token, timestamp, nonce, encrypted)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(strings.TrimSpace(signature))) == 1
}

// weComSignature 计算 WXBizMsgCrypt 签名。
func weComSignature(token, timestamp, nonce, encrypted string) string {
	parts := []string{token, timestamp, nonce, encrypted}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(sum[:])
}

// weComAESKey 把 EncodingAESKey 还原成 32 字节密钥。
func weComAESKey(encodingAESKey string) ([]byte, error) {
	encodingAESKey = strings.TrimSpace(encodingAESKey)
	if encodingAESKey == "" {
		return nil, fmt.Errorf("wecom: 未配置 EncodingAESKey")
	}
	// EncodingAESKey 固定 43 位，补一个 '=' 才是合法 base64。
	key, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil {
		return nil, fmt.Errorf("wecom: EncodingAESKey 不是合法 base64")
	}
	if len(key) != weComAESKeyRawBytes {
		return nil, fmt.Errorf("wecom: EncodingAESKey 长度非法")
	}
	return key, nil
}

// weComDecrypt 解开企业微信的密文，返回明文和报文尾部的 receiveid。
//
// 明文结构是 16 字节随机数 + 4 字节网络序长度 + 正文 + receiveid。
func weComDecrypt(encrypted, encodingAESKey string) ([]byte, string, error) {
	key, err := weComAESKey(encodingAESKey)
	if err != nil {
		return nil, "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encrypted))
	if err != nil {
		return nil, "", fmt.Errorf("wecom: 密文不是合法 base64")
	}
	if len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		return nil, "", fmt.Errorf("wecom: 密文长度非法")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, "", err
	}
	plaintext := make([]byte, len(raw))
	// 企业微信用 AES-256-CBC，IV 取密钥前 16 字节。
	cipher.NewCBCDecrypter(block, key[:aes.BlockSize]).CryptBlocks(plaintext, raw)
	plaintext, err = stripPKCS7(plaintext)
	if err != nil {
		return nil, "", err
	}
	if len(plaintext) < 20 {
		return nil, "", fmt.Errorf("wecom: 明文过短")
	}
	length := int(binary.BigEndian.Uint32(plaintext[16:20]))
	if length < 0 || 20+length > len(plaintext) {
		return nil, "", fmt.Errorf("wecom: 明文长度字段非法")
	}
	return plaintext[20 : 20+length], string(plaintext[20+length:]), nil
}

// —— 事件映射 ——

// weComMessage 是回调解密后的消息报文。
type weComMessage struct {
	ToUserName   string `xml:"ToUserName"`
	FromUserName string `xml:"FromUserName"`
	CreateTime   int64  `xml:"CreateTime"`
	MsgType      string `xml:"MsgType"`
	Content      string `xml:"Content"`
	MsgID        string `xml:"MsgId"`
	AgentID      string `xml:"AgentID"`
	ChatID       string `xml:"ChatId"`
	Event        string `xml:"Event"`
}

// weComEventFromCallback 把解密后的报文映射成统一事件。
//
// 语义对照：
//   - FromUserName 是成员的 UserID，企业内唯一
//   - ChatId 存在时是群聊（应用群聊），否则是应用单聊
//   - 应用消息都是直接发给机器人的，所以一律 ToMe
func weComEventFromCallback(payload []byte, agentID string) (MessageEvent, bool) {
	var message weComMessage
	if err := xml.Unmarshal(payload, &message); err != nil {
		return MessageEvent{}, false
	}
	// 只处理文本消息；event 类推送（进入应用、通讯录变更等）不当作对话。
	if message.MsgType != "text" {
		return MessageEvent{}, false
	}
	text := strings.TrimSpace(message.Content)
	userID := strings.TrimSpace(message.FromUserName)
	if text == "" || userID == "" {
		return MessageEvent{}, false
	}

	event := MessageEvent{
		Time:       platformEventTime(message.CreateTime),
		SelfID:     firstNonEmpty(strings.TrimSpace(message.AgentID), agentID),
		MessageID:  strings.TrimSpace(message.MsgID),
		RawMessage: text,
		Segments:   platformTextSegments(text, ""),
		UserID:     userID,
		SenderName: userID,
		ToMe:       true,
	}
	if chat := strings.TrimSpace(message.ChatID); chat != "" {
		event.Kind = EventKindGroup
		event.MessageType = "group"
		event.GroupID = chat
	} else {
		event.Kind = EventKindPrivate
		event.MessageType = "private"
	}
	return event, true
}
