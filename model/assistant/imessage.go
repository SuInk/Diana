// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

// IMessageConfig 是 BlueBubbles Server 的连接配置。
type IMessageConfig struct {
	// ProfileID 用于把 webhook 路由到正确的通道实例。
	ProfileID string
	// ServerURL 是 BlueBubbles Server 的地址，例如 http://192.168.1.10:1234。
	ServerURL string
	// Password 是 BlueBubbles Server 的密码，REST 接口以 ?password= 鉴权。
	Password string
	// WebhookToken 是 webhook 回调地址里 ?token= 的值，留空时回退为 Password。
	WebhookToken string
	// PollSeconds 大于 0 时额外轮询新消息，给 webhook 打不进来的部署兜底。
	PollSeconds int
}

const (
	// IMessageCallbackPath 是 BlueBubbles webhook 的回调路径。
	IMessageCallbackPath = "/api/channels/imessage/callback"

	imessageCallbackLimit  = 4 << 20
	imessageMaxMediaBytes  = 100 << 20
	imessageMinPollSeconds = 5
	imessagePollBatch      = 100
)

// IMessageChannel 通过 BlueBubbles Server 接入 iMessage。
//
// BlueBubbles 跑在一台登录了 Apple ID 的 Mac 上，把 Messages.app 包成 REST 接口。
// 收消息靠它把 new-message 事件 POST 到 Diana；发消息默认走 AppleScript，
// 只有「回复某条消息」这类能力需要 Mac 上开了 Private API。
type IMessageChannel struct {
	mu      sync.RWMutex
	cfg     IMessageConfig
	handler EventHandler
	client  *http.Client
	cancel  context.CancelFunc
	// privateAPI 记录服务端 Private API 是否可用，决定回复时能不能带 selectedMessageGuid。
	privateAPI bool
	// directChats 记住私聊对象最近一次出现的会话 guid：同一个号码可能走 iMessage
	// 也可能走 SMS，按入站时的真实会话回发，比拼一个 iMessage;-; 前缀可靠。
	directChats map[string]string

	statusMu sync.RWMutex
	status   ChannelStatus

	dedupe *eventDeduper
	// mediaClient 下载出站媒体的远程地址，只允许公网目标。
	mediaClient *http.Client
}

// NewIMessageChannel 创建 iMessage 通道。
func NewIMessageChannel(cfg IMessageConfig) *IMessageChannel {
	return &IMessageChannel{
		cfg:         cfg,
		client:      &http.Client{Timeout: 2 * time.Minute},
		status:      ChannelStatus{Endpoint: imessageEndpointLabel(cfg), UpdatedAt: time.Now()},
		dedupe:      newEventDeduper(30 * time.Minute),
		directChats: map[string]string{},
		mediaClient: netguard.NewPublicHTTPClient(2 * time.Minute),
	}
}

func imessageEndpointLabel(cfg IMessageConfig) string {
	return imessageServerBase(cfg) + " (webhook " + IMessageCallbackPath + ")"
}

func imessageServerBase(cfg IMessageConfig) string {
	return strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
}

// SetConfig 更新配置。
func (c *IMessageChannel) SetConfig(cfg IMessageConfig) {
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
	c.statusMu.Lock()
	c.status.Endpoint = imessageEndpointLabel(cfg)
	c.statusMu.Unlock()
}

// IMessageServerInfo 是 GET /api/v1/server/info 里 Diana 关心的字段。
type IMessageServerInfo struct {
	ServerVersion    string `json:"server_version"`
	OSVersion        string `json:"os_version"`
	PrivateAPI       bool   `json:"private_api"`
	HelperConnected  bool   `json:"helper_connected"`
	DetectedIMessage string `json:"detected_imessage"`
	DetectedICloud   string `json:"detected_icloud"`
}

// ProbeIMessageServer 请求一次 server/info，WebUI 的「测试连接」和 Connect 共用。
func ProbeIMessageServer(ctx context.Context, client *http.Client, serverURL, password string) (IMessageServerInfo, error) {
	cfg := IMessageConfig{ServerURL: serverURL, Password: password}
	if imessageServerBase(cfg) == "" || strings.TrimSpace(password) == "" {
		return IMessageServerInfo{}, fmt.Errorf("imessage: 服务器地址和密码都必须配置")
	}
	raw, err := imessageRequest(ctx, client, cfg, http.MethodGet, "/api/v1/server/info", nil)
	if err != nil {
		return IMessageServerInfo{}, err
	}
	var info IMessageServerInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return IMessageServerInfo{}, fmt.Errorf("imessage: 解析 server/info 失败: %w", err)
	}
	return info, nil
}

// imessageRequest 调一个 BlueBubbles 接口并剥掉 {status, message, data} 信封，返回 data。
func imessageRequest(ctx context.Context, client *http.Client, cfg IMessageConfig, method, path string, payload any) (json.RawMessage, error) {
	endpoint, err := imessageURL(cfg, path)
	if err != nil {
		return nil, err
	}
	raw, err := platformJSONRequest(ctx, client, method, endpoint, nil, payload)
	return imessageUnwrap(raw, err)
}

func imessageURL(cfg IMessageConfig, path string) (string, error) {
	base := imessageServerBase(cfg)
	if base == "" {
		return "", fmt.Errorf("imessage: 未配置服务器地址")
	}
	u, err := url.Parse(base + path)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("imessage: 服务器地址必须是 http:// 或 https:// URL")
	}
	q := u.Query()
	q.Set("password", strings.TrimSpace(cfg.Password))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func imessageUnwrap(raw []byte, err error) (json.RawMessage, error) {
	var envelope struct {
		Status  int             `json:"status"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
		Error   *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeErr := json.Unmarshal(raw, &envelope)
	if err != nil {
		// 4xx/5xx 的正文仍是 BlueBubbles 的错误信封，里面的 error.message 比 http 状态码有用。
		if decodeErr == nil && envelope.Error != nil && strings.TrimSpace(envelope.Error.Message) != "" {
			return nil, fmt.Errorf("imessage: %s (%s)", envelope.Error.Message, firstNonEmpty(envelope.Message, envelope.Error.Type))
		}
		return nil, fmt.Errorf("imessage: 请求失败: %w", err)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("imessage: 响应不是合法 JSON")
	}
	if envelope.Status != 0 && envelope.Status != http.StatusOK {
		return nil, fmt.Errorf("imessage: %s (status %d)", firstNonEmpty(envelope.Message, "请求被拒绝"), envelope.Status)
	}
	return envelope.Data, nil
}

// Connect 登记 webhook 处理器并保持在线，直到 ctx 取消。
//
// webhook 是被动接收；启动时请求一次 server/info，让地址或密码填错立刻在状态里
// 暴露出来，顺便知道 Private API 开没开。
func (c *IMessageChannel) Connect(ctx context.Context, handler EventHandler) error {
	c.mu.Lock()
	cfg := c.cfg
	client := c.client
	if imessageServerBase(cfg) == "" || strings.TrimSpace(cfg.Password) == "" {
		c.mu.Unlock()
		c.setStatus(false, "", "未配置 BlueBubbles 服务器地址 / 密码")
		return fmt.Errorf("imessage: 服务器地址和密码都必须配置")
	}
	c.handler = handler
	c.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	defer cancel()

	RegisterCallbackHandler(PlatformIMessage, cfg.ProfileID, http.HandlerFunc(c.ServeCallback))
	defer UnregisterCallbackHandler(PlatformIMessage, cfg.ProfileID)

	info, err := ProbeIMessageServer(runCtx, client, cfg.ServerURL, cfg.Password)
	if err != nil {
		c.setStatus(false, "", err.Error())
		return err
	}
	c.mu.Lock()
	c.privateAPI = info.PrivateAPI && info.HelperConnected
	c.mu.Unlock()
	c.setStatus(true, strings.TrimSpace(info.DetectedIMessage), "")

	if cfg.PollSeconds > 0 {
		go func() {
			defer recoverGoroutinePanic("imessage.go:poll")
			c.pollLoop(runCtx, cfg)
		}()
	}
	<-runCtx.Done()
	c.setStatus(false, c.Status().SelfID, "")
	return runCtx.Err()
}

// ServeCallback 处理 BlueBubbles 推来的 webhook。
//
// BlueBubbles 的 webhook 不签名、也不带任何凭据，只是把 {type, data} POST 到登记的
// 地址。回调地址是公网可达的，所以要求地址里带 ?token=，拿它当来源证明。
func (c *IMessageChannel) ServeCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()
	expected := strings.TrimSpace(firstNonEmpty(cfg.WebhookToken, cfg.Password))
	got := strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("token"), r.URL.Query().Get("password")))
	if expected == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(got)) != 1 {
		http.Error(w, "token mismatch", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, imessageCallbackLimit))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	var envelope struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	// 先回 200：BlueBubbles 不重试，但它的 axios 请求会一直挂着等响应。
	writeJSON(w, map[string]any{"status": http.StatusOK})
	if envelope.Type != "new-message" {
		return
	}
	var message imessageMessage
	if err := json.Unmarshal(envelope.Data, &message); err != nil {
		return
	}
	go func() {
		defer recoverGoroutinePanic("imessage.go:callback")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		c.dispatch(ctx, message)
	}()
}

// dispatch 把一条 BlueBubbles 消息转成统一事件交给上层；webhook 和轮询共用。
func (c *IMessageChannel) dispatch(ctx context.Context, message imessageMessage) {
	c.mu.RLock()
	handler := c.handler
	c.mu.RUnlock()
	if handler == nil {
		return
	}
	event, ok := imessageEventFromMessage(message, c.Status().SelfID)
	if !ok {
		return
	}
	if !c.dedupe.Accept(event.MessageID) {
		return
	}
	if event.Kind == EventKindPrivate {
		if chat := message.primaryChat(); chat != nil {
			c.mu.Lock()
			c.directChats[event.UserID] = chat.GUID
			c.mu.Unlock()
		}
	}
	event = c.resolveIncomingMedia(ctx, event, message.Attachments)
	_ = handler(ctx, event)
}

// pollLoop 定期拉取新消息，给 webhook 打不通的部署兜底；与 webhook 同时开也不会
// 重复回答，两条路进来的消息按 guid 去重。
func (c *IMessageChannel) pollLoop(ctx context.Context, cfg IMessageConfig) {
	interval := time.Duration(max(cfg.PollSeconds, imessageMinPollSeconds)) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// 只看连上之后的消息，不把 Mac 上的历史记录当成新消息回一遍。
	after := time.Now().UnixMilli()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		messages, err := c.queryMessagesAfter(ctx, after)
		if err != nil {
			c.setStatus(true, c.Status().SelfID, err.Error())
			continue
		}
		for _, message := range messages {
			if message.DateCreated > after {
				after = message.DateCreated
			}
			c.dispatch(ctx, message)
		}
	}
}

func (c *IMessageChannel) queryMessagesAfter(ctx context.Context, after int64) ([]imessageMessage, error) {
	c.mu.RLock()
	cfg := c.cfg
	client := c.client
	c.mu.RUnlock()
	raw, err := imessageRequest(ctx, client, cfg, http.MethodPost, "/api/v1/message/query", map[string]any{
		"with":  []string{"chat", "handle", "attachment"},
		"after": after,
		"sort":  "ASC",
		"limit": imessagePollBatch,
	})
	if err != nil {
		return nil, err
	}
	var messages []imessageMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, fmt.Errorf("imessage: 解析消息列表失败: %w", err)
	}
	return messages, nil
}

// resolveIncomingMedia 把附件下载到本地媒体缓存。下载地址带着服务器密码，
// 不能写进事件交给模型或浏览器，所以这里只落本地路径。
func (c *IMessageChannel) resolveIncomingMedia(ctx context.Context, event MessageEvent, attachments []imessageAttachment) MessageEvent {
	if len(attachments) == 0 {
		return event
	}
	dir, err := historyMediaDir()
	if err != nil {
		return event
	}
	c.mu.RLock()
	cfg := c.cfg
	client := c.client
	c.mu.RUnlock()
	for _, attachment := range attachments {
		guid := strings.TrimSpace(attachment.GUID)
		if guid == "" {
			continue
		}
		key := "imessage:" + imessageServerBase(cfg) + ":" + guid
		path, err := fetchMediaContent(ctx, dir, key, imessageMaxMediaBytes, func(ctx context.Context) ([]byte, string, string, error) {
			body, err := imessageDownloadAttachment(ctx, client, cfg, guid)
			return body, attachment.MimeType, filepath.Base(firstNonEmpty(attachment.TransferName, "attachment")), err
		})
		if err != nil {
			log.Printf("imessage: download attachment failed: message_id=%s err=%v", event.MessageID, err)
			continue
		}
		data := map[string]string{
			"file":                path,
			"cached_file":         path,
			"name":                strings.TrimSpace(attachment.TransferName),
			"mime":                strings.TrimSpace(attachment.MimeType),
			imageContentSHA256Key: filepath.Base(filepath.Dir(path)),
		}
		event.Segments = append(event.Segments, MessageSegment{Type: imessageSegmentType(attachment), Data: data})
	}
	return event
}

func imessageDownloadAttachment(ctx context.Context, client *http.Client, cfg IMessageConfig, guid string) ([]byte, error) {
	endpoint, err := imessageURL(cfg, "/api/v1/attachment/"+url.PathEscape(guid)+"/download")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("imessage: invalid attachment request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, safePlatformError(err, endpoint, nil)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("imessage: 下载附件失败: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, imessageMaxMediaBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > imessageMaxMediaBytes {
		return nil, fmt.Errorf("imessage: 附件超过 %dMB", imessageMaxMediaBytes>>20)
	}
	return body, nil
}

func imessageSegmentType(attachment imessageAttachment) string {
	mime := strings.ToLower(strings.TrimSpace(attachment.MimeType))
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/"):
		return "record"
	}
	return "file"
}

// Send 发送消息。
func (c *IMessageChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	_, err := c.SendWithResult(ctx, msg)
	return err
}

// SendWithResult 发送文本和附件，把 BlueBubbles 返回的消息 guid 交回上层。
//
// 入站事件里被回复消息的 threadOriginatorGuid 与这里的 guid 同属一个空间，
// 别人回复 Diana 的消息时才能回查到原文。
func (c *IMessageChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	chatGUID := c.chatGUIDFor(msg)
	if chatGUID == "" {
		return nil, fmt.Errorf("imessage: 缺少会话标识")
	}
	c.mu.RLock()
	cfg := c.cfg
	client := c.client
	privateAPI := c.privateAPI
	c.mu.RUnlock()

	// 回复（selectedMessageGuid）只有 Private API 才做得到；没开时带上它 BlueBubbles
	// 会改走 Private API 然后失败，整条消息都发不出去。不如退回普通发送。
	replyID := ""
	if privateAPI {
		replyID = strings.TrimSpace(msg.ReplyMessageID)
	}

	var first map[string]any
	keep := func(guid string) {
		if first == nil && guid != "" {
			first = map[string]any{"message_id": guid}
		}
	}
	if text := platformOutboundText(msg); text != "" {
		body := map[string]any{
			"chatGuid": chatGUID,
			"message":  text,
			"tempGuid": imessageTempGUID(),
			"method":   "apple-script",
		}
		if replyID != "" {
			body["method"] = "private-api"
			body["selectedMessageGuid"] = replyID
		}
		raw, err := imessageRequest(ctx, client, cfg, http.MethodPost, "/api/v1/message/text", body)
		if err != nil {
			return nil, fmt.Errorf("imessage: 发送失败: %w", err)
		}
		keep(imessageSentGUID(raw))
	}

	sources := make([]string, 0, len(msg.ImageURLs)+len(msg.VideoURLs)+len(msg.AudioURLs))
	sources = append(sources, msg.ImageURLs...)
	sources = append(sources, msg.VideoURLs...)
	sources = append(sources, msg.AudioURLs...)
	for _, segment := range msg.Segments {
		if segment.Type == "file" {
			sources = append(sources, segment.Data["file"])
		}
	}
	for _, source := range sources {
		if strings.TrimSpace(source) == "" {
			continue
		}
		guid, err := c.sendAttachment(ctx, client, cfg, chatGUID, source)
		if err != nil {
			return first, err
		}
		keep(guid)
	}
	return first, nil
}

// chatGUIDFor 算出出站消息的会话 guid。
//
// 群聊的 GroupID 本身就是 chat guid。私聊只有 handle，优先用入站时记下的真实会话；
// 没见过的对象按 iMessage 私聊拼，BlueBubbles 的 guid 规则是 服务;-;handle。
func (c *IMessageChannel) chatGUIDFor(msg OutgoingMessage) string {
	target, isGroup := platformChatTarget(msg)
	if target == "" || isGroup || strings.Contains(target, ";") {
		return target
	}
	c.mu.RLock()
	guid := c.directChats[target]
	c.mu.RUnlock()
	return firstNonEmpty(guid, "iMessage;-;"+target)
}

func (c *IMessageChannel) sendAttachment(ctx context.Context, client *http.Client, cfg IMessageConfig, chatGUID, source string) (string, error) {
	content, name, err := c.loadOutgoingMedia(ctx, source)
	if err != nil {
		return "", fmt.Errorf("imessage: 读取附件失败: %w", err)
	}
	endpoint, err := imessageURL(cfg, "/api/v1/message/attachment")
	if err != nil {
		return "", err
	}
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for key, value := range map[string]string{
		"chatGuid": chatGUID,
		"tempGuid": imessageTempGUID(),
		"name":     name,
		"method":   "apple-script",
	} {
		if err := writer.WriteField(key, value); err != nil {
			return "", err
		}
	}
	part, err := writer.CreateFormFile("attachment", name)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(content); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return "", fmt.Errorf("imessage: invalid attachment request")
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("imessage: 发送附件失败: %w", safePlatformError(err, endpoint, nil))
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, platformHTTPResponseLimit))
	if readErr != nil {
		return "", readErr
	}
	var statusErr error
	if resp.StatusCode >= 300 {
		statusErr = fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(truncateForError(string(raw))))
	}
	data, err := imessageUnwrap(raw, statusErr)
	if err != nil {
		return "", fmt.Errorf("imessage: 发送附件失败: %w", err)
	}
	return imessageSentGUID(data), nil
}

// loadOutgoingMedia 把出站媒体引用读成字节：本地文件、内联 base64，或公网 URL。
func (c *IMessageChannel) loadOutgoingMedia(ctx context.Context, source string) ([]byte, string, error) {
	source = strings.TrimSpace(source)
	if inlineImageSource(source) {
		body, contentType, err := decodeInlineHistoryImage(source)
		if err != nil {
			return nil, "", err
		}
		return body, "image" + imessageExtensionFor(contentType), nil
	}
	if path := telegramLocalPath(source); path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return nil, "", err
		}
		if info.Size() > imessageMaxMediaBytes {
			return nil, "", fmt.Errorf("文件超过 %dMB", imessageMaxMediaBytes>>20)
		}
		body, err := os.ReadFile(path)
		return body, filepath.Base(path), err
	}
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		return nil, "", fmt.Errorf("不支持的媒体地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, "", err
	}
	c.mu.RLock()
	client := c.mediaClient
	c.mu.RUnlock()
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, imessageMaxMediaBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > imessageMaxMediaBytes {
		return nil, "", fmt.Errorf("文件超过 %dMB", imessageMaxMediaBytes>>20)
	}
	name := filepath.Base(resp.Request.URL.Path)
	if name == "" || name == "." || name == "/" || !strings.Contains(name, ".") {
		name = "attachment" + imessageExtensionFor(resp.Header.Get("Content-Type"))
	}
	return body, name, nil
}

// imessageExtensionFor 给没有文件名的媒体补扩展名：Messages.app 按扩展名决定
// 显示成图片还是文件图标。
func imessageExtensionFor(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])) {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "video/mp4":
		return ".mp4"
	case "audio/mpeg":
		return ".mp3"
	}
	return ""
}

func imessageSentGUID(raw json.RawMessage) string {
	var sent struct {
		GUID string `json:"guid"`
	}
	if err := json.Unmarshal(raw, &sent); err != nil {
		return ""
	}
	return strings.TrimSpace(sent.GUID)
}

// imessageTempGUID 生成 tempGuid。AppleScript 发送必须带它，BlueBubbles 靠它把
// 发出去的消息和数据库里新出现的那条对上。
func imessageTempGUID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return "diana-" + hex.EncodeToString(buf)
}

// CallAPI 透传 BlueBubbles 接口，action 形如 "POST /api/v1/message/react"。
// tapback、标记已读这类可选能力都从这里走，需要 Mac 上开了 Private API。
func (c *IMessageChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	method, path := http.MethodGet, strings.TrimSpace(action)
	if fields := strings.SplitN(path, " ", 2); len(fields) == 2 {
		method, path = strings.ToUpper(fields[0]), strings.TrimSpace(fields[1])
	}
	if path == "" {
		return nil, fmt.Errorf("imessage: 缺少接口路径")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	c.mu.RLock()
	cfg := c.cfg
	client := c.client
	c.mu.RUnlock()
	var payload any
	if len(params) > 0 && method != http.MethodGet {
		payload = params
	}
	raw, err := imessageRequest(ctx, client, cfg, method, path, payload)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{"result": raw}, nil
	}
	return out, nil
}

// Status 返回通道状态。
func (c *IMessageChannel) Status() ChannelStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.status
}

func (c *IMessageChannel) setStatus(connected bool, selfID, lastErr string) {
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
func (c *IMessageChannel) Close() error {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	profileID := c.cfg.ProfileID
	c.mu.Unlock()
	UnregisterCallbackHandler(PlatformIMessage, profileID)
	if cancel != nil {
		cancel()
	}
	c.setStatus(false, c.Status().SelfID, "")
	return nil
}

// —— 事件映射 ——

// imessageMessage 是 BlueBubbles MessageSerializer 输出里 Diana 用到的字段。
type imessageMessage struct {
	GUID                  string               `json:"guid"`
	Text                  string               `json:"text"`
	Subject               string               `json:"subject"`
	IsFromMe              bool                 `json:"isFromMe"`
	DateCreated           int64                `json:"dateCreated"`
	ItemType              int                  `json:"itemType"`
	AssociatedMessageGUID string               `json:"associatedMessageGuid"`
	AssociatedMessageType any                  `json:"associatedMessageType"`
	ThreadOriginatorGUID  string               `json:"threadOriginatorGuid"`
	Handle                *imessageHandle      `json:"handle"`
	Chats                 []imessageChat       `json:"chats"`
	Attachments           []imessageAttachment `json:"attachments"`
}

type imessageHandle struct {
	Address string `json:"address"`
}

type imessageChat struct {
	GUID           string `json:"guid"`
	ChatIdentifier string `json:"chatIdentifier"`
	DisplayName    string `json:"displayName"`
}

type imessageAttachment struct {
	GUID         string `json:"guid"`
	MimeType     string `json:"mimeType"`
	TransferName string `json:"transferName"`
	TotalBytes   int64  `json:"totalBytes"`
}

func (m imessageMessage) primaryChat() *imessageChat {
	if len(m.Chats) == 0 {
		return nil
	}
	return &m.Chats[0]
}

// imessageIsGroupChat 按 chat guid 判断群聊。guid 形如 iMessage;+;chat123（群）
// 或 iMessage;-;+8613800000000（私聊），中间那一位就是 BlueBubbles 的约定。
func imessageIsGroupChat(guid string) bool {
	parts := strings.SplitN(guid, ";", 3)
	return len(parts) == 3 && parts[1] == "+"
}

// imessageEventFromMessage 把 new-message 映射成统一事件。
//
// 语义对照：
//   - handle.address（手机号或邮箱）是发送者账号
//   - chat guid 的第二段是 + 时为群聊，群号就是完整 guid；否则是私聊
//   - threadOriginatorGuid 是被回复的那条消息
//   - 自己发出的、tapback（associatedMessageType 非空）和改群名之类的系统项都不当对话
func imessageEventFromMessage(message imessageMessage, selfID string) (MessageEvent, bool) {
	if message.IsFromMe || strings.TrimSpace(message.GUID) == "" {
		return MessageEvent{}, false
	}
	if message.ItemType != 0 || strings.TrimSpace(message.AssociatedMessageGUID) != "" || imessageHasAssociatedType(message.AssociatedMessageType) {
		return MessageEvent{}, false
	}
	userID := ""
	if message.Handle != nil {
		userID = strings.TrimSpace(message.Handle.Address)
	}
	chat := message.primaryChat()
	if userID == "" && chat != nil && !imessageIsGroupChat(chat.GUID) {
		userID = strings.TrimSpace(chat.ChatIdentifier)
	}
	if userID == "" {
		return MessageEvent{}, false
	}
	// U+FFFC 是 Messages 给附件在正文里留的占位符，附件另走消息段。
	text := strings.TrimSpace(strings.ReplaceAll(message.Text, "￼", ""))
	if subject := strings.TrimSpace(message.Subject); subject != "" {
		text = strings.TrimSpace(subject + "\n" + text)
	}
	if text == "" && len(message.Attachments) == 0 {
		return MessageEvent{}, false
	}
	quoted := strings.TrimSpace(message.ThreadOriginatorGUID)
	event := MessageEvent{
		Platform:   PlatformIMessage,
		Time:       platformEventTime(message.DateCreated),
		SelfID:     selfID,
		MessageID:  strings.TrimSpace(message.GUID),
		RawMessage: text,
		Segments:   platformTextSegments(text, quoted),
		UserID:     userID,
		SenderName: userID,
	}
	if quoted != "" {
		event.Quoted = &QuotedMessage{MessageID: quoted}
	}
	if chat != nil && imessageIsGroupChat(chat.GUID) {
		event.Kind = EventKindGroup
		event.MessageType = "group"
		event.GroupID = strings.TrimSpace(chat.GUID)
		event.GroupName = strings.TrimSpace(chat.DisplayName)
		// iMessage 没有 @机器人 的结构化提及：Diana 用的是一个普通 Apple ID，
		// 群里是否该接话交给群触发词和主动回复的判断。
	} else {
		event.Kind = EventKindPrivate
		event.MessageType = "private"
		event.ToMe = true
	}
	return event, true
}

func imessageHasAssociatedType(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case float64:
		return v != 0
	case string:
		return strings.TrimSpace(v) != "" && strings.TrimSpace(v) != "0"
	}
	return true
}
