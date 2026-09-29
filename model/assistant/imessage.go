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
	// WebhookToken 是 webhook 回调地址里 ?token= 的值，留空时拒收所有 webhook。
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
	// imessagePollFailureLimit 次连续轮询失败后状态改为未连接。
	imessagePollFailureLimit = 3
	// imessageMinWebhookTokenLength 挡住手填的短 token：回调地址是公开的，token 是唯一凭据。
	imessageMinWebhookTokenLength = 16
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
	// runCtx 是当前连接的生命周期，会话 worker 用它派生处理超时。
	runCtx context.Context

	// state 持久化轮询游标、已处理的消息和私聊会话，按配置档和服务器地址分开。
	state    *imessageState
	stateKey string

	queueMu sync.Mutex
	queues  map[string]*imessageChatQueue

	statusMu sync.RWMutex
	status   ChannelStatus

	// mediaClient 下载出站媒体的远程地址，只允许公网目标。
	mediaClient *http.Client
}

// NewIMessageChannel 创建 iMessage 通道。
func NewIMessageChannel(cfg IMessageConfig) *IMessageChannel {
	return &IMessageChannel{
		cfg:         cfg,
		client:      &http.Client{Timeout: 2 * time.Minute},
		status:      ChannelStatus{Endpoint: imessageEndpointLabel(cfg), UpdatedAt: time.Now()},
		queues:      map[string]*imessageChatQueue{},
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
	c.runCtx = runCtx
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
// 地址，所以地址里必须带独立的 ?token=。这里刻意不认服务器密码：写进 webhook 地址
// 的东西会落进反向代理的访问日志，而服务器密码能读写整个 iMessage 账号。
func (c *IMessageChannel) ServeCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()
	expected := strings.TrimSpace(cfg.WebhookToken)
	got := strings.TrimSpace(r.URL.Query().Get("token"))
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
	// 入队很快，附件下载和回答都在会话 worker 里做；这里回 200 不会让 BlueBubbles 干等。
	writeJSON(w, map[string]any{"status": http.StatusOK})
	if envelope.Type != "new-message" {
		return
	}
	var message imessageMessage
	if err := json.Unmarshal(envelope.Data, &message); err != nil {
		return
	}
	c.enqueue(message)
}

// enqueue 把一条消息交给它所在会话的 worker；webhook 和轮询共用。
//
// 同一会话串行处理：附件要先下载，每条各开一个 goroutine 的话，紧跟在图片后面的
// 文字会比图片先到上层。不同会话互不等待，一个大附件只卡住它自己那个会话。
func (c *IMessageChannel) enqueue(message imessageMessage) {
	event, ok := imessageEventFromMessage(message, c.Status().SelfID)
	if !ok {
		return
	}
	state := c.stateStore()
	if !state.accept(event.MessageID) {
		return
	}
	key := event.UserID
	if chat := message.primaryChat(); chat != nil && strings.TrimSpace(chat.GUID) != "" {
		key = chat.GUID
		if event.Kind == EventKindPrivate {
			state.rememberDirectChat(event.UserID, chat.GUID)
		}
	}
	item := imessageQueued{event: event, attachments: message.Attachments}
	c.queueMu.Lock()
	queue, running := c.queues[key]
	if !running {
		queue = &imessageChatQueue{}
		c.queues[key] = queue
	}
	queue.pending = append(queue.pending, item)
	c.queueMu.Unlock()
	if !running {
		go func() {
			defer recoverGoroutinePanic("imessage.go:queue")
			c.drainQueue(key, queue)
		}()
	}
}

type imessageQueued struct {
	event       MessageEvent
	attachments []imessageAttachment
}

type imessageChatQueue struct {
	pending []imessageQueued
}

func (c *IMessageChannel) drainQueue(key string, queue *imessageChatQueue) {
	// 处理中途 panic 时把队列摘掉，否则这个会话之后的消息会一直排在一个没人消费的队列里。
	finished := false
	defer func() {
		if finished {
			return
		}
		c.queueMu.Lock()
		if c.queues[key] == queue {
			delete(c.queues, key)
		}
		c.queueMu.Unlock()
	}()
	for {
		c.queueMu.Lock()
		if len(queue.pending) == 0 {
			delete(c.queues, key)
			c.queueMu.Unlock()
			finished = true
			return
		}
		item := queue.pending[0]
		queue.pending = queue.pending[1:]
		c.queueMu.Unlock()
		c.deliver(item)
	}
}

func (c *IMessageChannel) deliver(item imessageQueued) {
	c.mu.RLock()
	handler := c.handler
	base := c.runCtx
	c.mu.RUnlock()
	if handler == nil {
		return
	}
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, 5*time.Minute)
	defer cancel()
	event := c.resolveIncomingMedia(ctx, item.event, item.attachments)
	_ = handler(ctx, event)
}

// stateStore 返回当前配置对应的持久状态，配置换了服务器就换一份。
func (c *IMessageChannel) stateStore() *imessageState {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.TrimSpace(c.cfg.ProfileID) + "\x00" + imessageServerBase(c.cfg)
	if c.state == nil || c.stateKey != key {
		c.state = loadIMessageState(c.cfg.ProfileID, imessageServerBase(c.cfg))
		c.stateKey = key
	}
	return c.state
}

// pollLoop 定期拉取新消息，给 webhook 打不通的部署兜底；与 webhook 同时开也不会
// 重复回答，两条路进来的消息按 guid 去重。
func (c *IMessageChannel) pollLoop(ctx context.Context, cfg IMessageConfig) {
	interval := time.Duration(max(cfg.PollSeconds, imessageMinPollSeconds)) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		err := c.pollOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		failures = c.notePollResult(failures, err)
	}
}

// notePollResult 按连续失败次数更新状态。偶发一次失败只记原因；连续失败说明 Mac
// 那头已经不可用，只开了轮询的部署此时收不到任何消息，状态不能再显示已连接。
func (c *IMessageChannel) notePollResult(failures int, err error) int {
	selfID := c.Status().SelfID
	if err == nil {
		if failures > 0 {
			c.setStatus(true, selfID, "")
		}
		return 0
	}
	failures++
	if failures >= imessagePollFailureLimit {
		c.setStatus(false, selfID, fmt.Sprintf("轮询 BlueBubbles 连续失败 %d 次：%v", failures, err))
	} else {
		c.setStatus(c.Status().Connected, selfID, err.Error())
	}
	return failures
}

// pollOnce 拉一轮新消息。
//
// 游标用服务端的 dateCreated，不用本机时间：两台机器的时钟对不齐，用本机时间做起点，
// Mac 偏慢时刚到的消息就落在起点之前被跳过。游标持久化，停机期间的消息重启后还能
// 拉到；第一次启用时从服务端最新一条开始，不把历史记录当新消息回一遍。
func (c *IMessageChannel) pollOnce(ctx context.Context) error {
	state := c.stateStore()
	cursor := state.cursor()
	if cursor <= 0 {
		latest, err := c.queryMessages(ctx, 0, "DESC", 1)
		if err != nil {
			return err
		}
		start := int64(1)
		for _, message := range latest {
			state.accept(message.GUID)
			start = max(start, message.DateCreated)
		}
		state.advanceCursor(start)
		return nil
	}
	messages, err := c.queryMessages(ctx, cursor, "ASC", imessagePollBatch)
	if err != nil {
		return err
	}
	newest := cursor
	for _, message := range messages {
		newest = max(newest, message.DateCreated)
		c.enqueue(message)
	}
	state.advanceCursor(newest)
	return nil
}

func (c *IMessageChannel) queryMessages(ctx context.Context, after int64, sort string, limit int) ([]imessageMessage, error) {
	c.mu.RLock()
	cfg := c.cfg
	client := c.client
	c.mu.RUnlock()
	body := map[string]any{
		"with":  []string{"chat", "handle", "attachment"},
		"sort":  sort,
		"limit": limit,
	}
	if after > 0 {
		body["after"] = after
	}
	raw, err := imessageRequest(ctx, client, cfg, http.MethodPost, "/api/v1/message/query", body)
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
	chatGUID := c.chatGUIDFor(ctx, msg)
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
// 群聊的 GroupID 本身就是 chat guid。私聊只有 handle：先用持久化的入站会话；没见过
// 的对象去服务端按 iMessage、SMS 两种 guid 各查一次（chat/query 只能按 guid 过滤，
// 不能按参与者查）；都查不到才按 iMessage 私聊拼。只能收短信的联系人如果直接拼成
// iMessage 会话，消息会发不出去。
func (c *IMessageChannel) chatGUIDFor(ctx context.Context, msg OutgoingMessage) string {
	target, isGroup := platformChatTarget(msg)
	if target == "" || isGroup || strings.Contains(target, ";") {
		return target
	}
	state := c.stateStore()
	if guid := state.directChat(target); guid != "" {
		return guid
	}
	for _, service := range []string{"iMessage", "SMS"} {
		guid := service + ";-;" + target
		if c.chatExists(ctx, guid) {
			state.rememberDirectChat(target, guid)
			return guid
		}
	}
	return "iMessage;-;" + target
}

func (c *IMessageChannel) chatExists(ctx context.Context, guid string) bool {
	c.mu.RLock()
	cfg := c.cfg
	client := c.client
	c.mu.RUnlock()
	raw, err := imessageRequest(ctx, client, cfg, http.MethodPost, "/api/v1/chat/query", map[string]any{"guid": guid, "limit": 1})
	if err != nil {
		return false
	}
	var chats []imessageChat
	if err := json.Unmarshal(raw, &chats); err != nil {
		return false
	}
	for _, chat := range chats {
		if strings.EqualFold(strings.TrimSpace(chat.GUID), guid) {
			return true
		}
	}
	return false
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
	raw, readErr := netguard.ReadResponseBody(resp, platformHTTPResponseLimit)
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

// NewIMessageWebhookToken 生成 webhook 回调地址里的 token。
func NewIMessageWebhookToken() string {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
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
