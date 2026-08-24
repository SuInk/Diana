// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// TelegramConfig 是 Telegram Bot API 通道的连接配置。
type TelegramConfig struct {
	// BotToken 来自 BotFather。
	BotToken string
	// APIBaseURL 留空用官方 api.telegram.org；可指向自建 Bot API server。
	APIBaseURL string
	// ProxyURL 国内网络通常必须配置，支持 http/https/socks5。
	ProxyURL string
}

const (
	telegramDefaultAPIBase = "https://api.telegram.org"
	// 长轮询超时。Telegram 建议 30~50s，超时期间连接挂起不消耗配额。
	telegramPollTimeoutSeconds = 30
	// 单次上传体积上限，Bot API 对普通 bot 的限制是 50MB。
	telegramMaxUploadBytes = 50 << 20
)

// TelegramChannel 通过官方 Bot API 长轮询接入 Telegram。
//
// 选长轮询而不是 webhook：webhook 需要一个带证书的公网地址，家庭或内网
// 部署基本用不了；长轮询只要能出站访问 api.telegram.org 就行。
type TelegramChannel struct {
	mu      sync.RWMutex
	cfg     TelegramConfig
	handler EventHandler

	statusMu sync.RWMutex
	status   ChannelStatus

	client *http.Client
	cancel context.CancelFunc
	// botUsername 来自 getMe，用于精确判断 @提及。
	botUsername string
	// offset 是下一次 getUpdates 的起点，保证已处理的更新不会重复投递。
	offset int64
}

// NewTelegramChannel 创建 Telegram 通道。
func NewTelegramChannel(cfg TelegramConfig) *TelegramChannel {
	return &TelegramChannel{
		cfg:    cfg,
		client: telegramHTTPClient(cfg.ProxyURL),
		status: ChannelStatus{
			Endpoint:  telegramEndpointLabel(cfg),
			UpdatedAt: time.Now(),
		},
	}
}

// SetConfig 更新连接配置，代理变化时重建 HTTP 客户端。
func (c *TelegramChannel) SetConfig(cfg TelegramConfig) {
	c.mu.Lock()
	proxyChanged := c.cfg.ProxyURL != cfg.ProxyURL
	c.cfg = cfg
	if proxyChanged || c.client == nil {
		c.client = telegramHTTPClient(cfg.ProxyURL)
	}
	c.mu.Unlock()
	c.setStatus(false, c.Status().SelfID, "")
	c.statusMu.Lock()
	c.status.Endpoint = telegramEndpointLabel(cfg)
	c.statusMu.Unlock()
}

func telegramEndpointLabel(cfg TelegramConfig) string {
	base := strings.TrimSpace(cfg.APIBaseURL)
	if base == "" {
		base = telegramDefaultAPIBase
	}
	return base + " (long polling)"
}

func telegramHTTPClient(proxyURL string) *http.Client {
	transport := &http.Transport{}
	if proxy := strings.TrimSpace(proxyURL); proxy != "" {
		if parsed, err := url.Parse(proxy); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	return &http.Client{
		Transport: transport,
		// 略大于长轮询超时，留出网络往返余量。
		Timeout: (telegramPollTimeoutSeconds + 20) * time.Second,
	}
}

// Connect 登记事件处理器并开始长轮询，直到 ctx 取消。
func (c *TelegramChannel) Connect(ctx context.Context, handler EventHandler) error {
	c.mu.Lock()
	if strings.TrimSpace(c.cfg.BotToken) == "" {
		c.mu.Unlock()
		c.setStatus(false, "", "未配置 Telegram Bot Token")
		return fmt.Errorf("telegram: bot token is required")
	}
	c.handler = handler
	c.mu.Unlock()

	pollCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	defer cancel()

	// 先用 getMe 确认 token 有效，顺便拿到 bot 自己的 ID 和 username。
	// username 是判断「这条消息有没有 @我」的唯一依据，必须拿到。
	if me, err := c.CallAPI(pollCtx, "getMe", nil); err == nil {
		username, _ := me["username"].(string)
		c.mu.Lock()
		c.botUsername = username
		c.mu.Unlock()
		c.setStatus(true, telegramAnyID(me["id"]), "")
	} else {
		c.setStatus(false, "", err.Error())
		return err
	}

	c.pollLoop(pollCtx)
	return pollCtx.Err()
}

// pollLoop 持续拉取更新；失败时退避重试，不因为一次网络抖动就断开。
func (c *TelegramChannel) pollLoop(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := c.fetchUpdates(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.setStatus(false, c.Status().SelfID, err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		c.setStatus(true, c.Status().SelfID, "")
		for _, update := range updates {
			c.dispatch(ctx, update)
		}
	}
}

func (c *TelegramChannel) fetchUpdates(ctx context.Context) ([]telegramUpdate, error) {
	params := map[string]any{
		"timeout": telegramPollTimeoutSeconds,
		// 只订阅消息类更新，避免拉回大量无关事件。
		"allowed_updates": []string{"message", "edited_message", "channel_post"},
	}
	c.mu.RLock()
	offset := c.offset
	c.mu.RUnlock()
	if offset > 0 {
		params["offset"] = offset
	}
	raw, err := c.callRaw(ctx, "getUpdates", params)
	if err != nil {
		return nil, err
	}
	var updates []telegramUpdate
	if err := json.Unmarshal(raw, &updates); err != nil {
		return nil, fmt.Errorf("telegram: decode updates: %w", err)
	}
	for _, update := range updates {
		if update.UpdateID >= offset {
			offset = update.UpdateID + 1
		}
	}
	c.mu.Lock()
	c.offset = offset
	c.mu.Unlock()
	return updates, nil
}

func (c *TelegramChannel) dispatch(ctx context.Context, update telegramUpdate) {
	message := update.Message
	if message == nil {
		message = update.EditedMessage
	}
	if message == nil {
		message = update.ChannelPost
	}
	if message == nil {
		return
	}
	c.mu.RLock()
	handler := c.handler
	username := c.botUsername
	c.mu.RUnlock()
	selfID := c.Status().SelfID
	if handler == nil {
		return
	}
	event := telegramMessageToEvent(message, selfID, username)
	if event.Kind == "" {
		return
	}
	_ = handler(ctx, event)
}

// Send 把统一的出站消息翻译成 Bot API 调用。
func (c *TelegramChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	chatID := strings.TrimSpace(msg.GroupID)
	if chatID == "" {
		chatID = strings.TrimSpace(msg.UserID)
	}
	if chatID == "" {
		return fmt.Errorf("telegram: missing chat id")
	}

	if text := strings.TrimSpace(msg.Text); text != "" {
		// 提及标记在这里落地成 Telegram 自己的形式：正文写「@昵称」，另外附一条
		// text_mention entity 指向用户 id。这是 Telegram 给「没有 username 的人」
		// 准备的提及方式——显示成可点击的名字，对方有通知，不依赖 username。
		text, mentions := renderDianaMentions(text, msg.MentionNames)
		params := map[string]any{
			"chat_id": chatID,
			"text":    text,
			// 统一发纯文本：机器人回复里的 * # ` 等符号不该被当成格式标记。
			"disable_web_page_preview": true,
		}
		// entities 和 parse_mode 是两条路，只传 entities 不会把正文当 Markdown 解析。
		if entities := telegramMentionEntities(mentions); len(entities) > 0 {
			params["entities"] = entities
		}
		if replyID := strings.TrimSpace(msg.ReplyMessageID); replyID != "" {
			params["reply_to_message_id"] = replyID
			// 被回复的消息可能已删除，这时仍然把消息发出去。
			params["allow_sending_without_reply"] = true
		}
		if _, err := c.CallAPI(ctx, "sendMessage", params); err != nil {
			return err
		}
	}

	for _, image := range msg.ImageURLs {
		if err := c.sendMedia(ctx, chatID, "sendPhoto", "photo", image); err != nil {
			return err
		}
	}
	for _, video := range msg.VideoURLs {
		if err := c.sendMedia(ctx, chatID, "sendVideo", "video", video); err != nil {
			return err
		}
	}
	return nil
}

// sendMedia 发送单个媒体。远程 URL 直接交给 Telegram 去拉，本地文件走
// multipart 上传——Telegram 拉不到我们本机的 /media/resolver 地址。
func (c *TelegramChannel) sendMedia(ctx context.Context, chatID, method, field, source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil
	}
	path := telegramLocalPath(source)
	if path == "" {
		_, err := c.CallAPI(ctx, method, map[string]any{"chat_id": chatID, field: source})
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("telegram: read media: %w", err)
	}
	if info.Size() > telegramMaxUploadBytes {
		return fmt.Errorf("telegram: 媒体 %.1fMB 超过 Bot API 50MB 上传限制", float64(info.Size())/(1<<20))
	}
	return c.uploadMedia(ctx, method, chatID, field, path)
}

// telegramLocalPath 判断出站地址是否指向本机文件；不是则返回空串。
func telegramLocalPath(source string) string {
	if path := localMediaPath(source); path != "" {
		return path
	}
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return ""
	}
	if strings.HasPrefix(source, "file://") {
		return strings.TrimPrefix(source, "file://")
	}
	if filepath.IsAbs(source) {
		return source
	}
	return ""
}

func (c *TelegramChannel) uploadMedia(ctx context.Context, method, chatID, field, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("telegram: open media: %w", err)
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("chat_id", chatID); err != nil {
		return err
	}
	part, err := writer.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	endpoint, err := c.methodURL(method)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	_, err = c.do(req)
	return err
}

// CallAPI 透传任意 Bot API 方法，返回 result 字段解出的 map。
func (c *TelegramChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	raw, err := c.callRaw(ctx, action, params)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		// 有些方法返回 true 之类的标量，不是错误。
		return map[string]any{"result": json.RawMessage(raw)}, nil
	}
	return out, nil
}

func (c *TelegramChannel) callRaw(ctx context.Context, action string, params map[string]any) (json.RawMessage, error) {
	action = strings.TrimSpace(action)
	if action == "" {
		return nil, fmt.Errorf("telegram: method is required")
	}
	endpoint, err := c.methodURL(action)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if params == nil {
		payload = []byte("{}")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func (c *TelegramChannel) do(req *http.Request) (json.RawMessage, error) {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("telegram: bad response (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if !envelope.OK {
		return nil, fmt.Errorf("telegram: %s (code %d)", envelope.Description, envelope.ErrorCode)
	}
	return envelope.Result, nil
}

func (c *TelegramChannel) methodURL(method string) (string, error) {
	c.mu.RLock()
	token := strings.TrimSpace(c.cfg.BotToken)
	base := strings.TrimSpace(c.cfg.APIBaseURL)
	c.mu.RUnlock()
	if token == "" {
		return "", fmt.Errorf("telegram: bot token is required")
	}
	if base == "" {
		base = telegramDefaultAPIBase
	}
	return strings.TrimRight(base, "/") + "/bot" + token + "/" + method, nil
}

// Status 返回通道状态。
func (c *TelegramChannel) Status() ChannelStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.status
}

func (c *TelegramChannel) setStatus(connected bool, selfID, lastErr string) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	c.status.Connected = connected
	if selfID != "" {
		c.status.SelfID = selfID
	}
	c.status.LastError = lastErr
	c.status.UpdatedAt = time.Now()
}

// Close 停止长轮询。
func (c *TelegramChannel) Close() error {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.setStatus(false, c.Status().SelfID, "")
	return nil
}

// —— Bot API 数据结构（只保留用得到的字段） ——

type telegramUpdate struct {
	UpdateID      int64            `json:"update_id"`
	Message       *telegramMessage `json:"message,omitempty"`
	EditedMessage *telegramMessage `json:"edited_message,omitempty"`
	ChannelPost   *telegramMessage `json:"channel_post,omitempty"`
}

type telegramMessage struct {
	MessageID      int64            `json:"message_id"`
	Date           int64            `json:"date"`
	Text           string           `json:"text"`
	Caption        string           `json:"caption"`
	From           *telegramUser    `json:"from"`
	Chat           *telegramChat    `json:"chat"`
	NewChatMembers []telegramUser   `json:"new_chat_members,omitempty"`
	ReplyTo        *telegramMessage `json:"reply_to_message"`
	Entities       []telegramEntity `json:"entities"`
	Photo          []telegramPhoto  `json:"photo"`
}

type telegramUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type telegramChat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type telegramEntity struct {
	Type   string        `json:"type"`
	Offset int           `json:"offset"`
	Length int           `json:"length"`
	User   *telegramUser `json:"user,omitempty"`
}

type telegramPhoto struct {
	FileID string `json:"file_id"`
}

// telegramMessageToEvent 把 Bot API 消息映射成统一事件。
//
// 语义对照：
//   - chat.type private -> 私聊；group/supergroup/channel -> 群聊，chat.id 当群号
//   - from.id -> 用户 ID；Telegram 没有 OneBot 那样的群等级，SenderLevel 保持 0，
//     ReplyGate 的等级门槛会按「读不到即放行」处理
//   - ToMe 由文本里的 @username 提及判断
func telegramMessageToEvent(msg *telegramMessage, selfID, botUsername string) MessageEvent {
	if msg == nil || msg.Chat == nil {
		return MessageEvent{}
	}
	// 入群通知：Telegram 把它作为带 new_chat_members 的普通消息下发，
	// 不映射的话欢迎语在 Telegram 上永远不会触发。
	if len(msg.NewChatMembers) > 0 && msg.Chat.Type != "private" {
		joined := msg.NewChatMembers[0]
		return MessageEvent{
			Kind:        EventKindNotice,
			SubType:     "group_increase",
			Time:        msg.Date,
			SelfID:      selfID,
			MessageID:   strconv.FormatInt(msg.MessageID, 10),
			MessageType: "notice",
			GroupID:     strconv.FormatInt(msg.Chat.ID, 10),
			UserID:      strconv.FormatInt(joined.ID, 10),
			SenderName:  telegramDisplayName(&joined),
		}
	}

	text := msg.Text
	if text == "" {
		text = msg.Caption
	}

	// 回复关系落成与 OneBot 同一种 reply 段，模型看到的引用标记因此跨平台一致。
	segments := make([]MessageSegment, 0, 2)
	if msg.ReplyTo != nil && msg.ReplyTo.MessageID != 0 {
		segments = append(segments, MessageSegment{
			Type: "reply",
			Data: map[string]string{"id": strconv.FormatInt(msg.ReplyTo.MessageID, 10)},
		})
	}
	segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": text}})

	event := MessageEvent{
		Time:       msg.Date,
		SelfID:     selfID,
		MessageID:  strconv.FormatInt(msg.MessageID, 10),
		RawMessage: text,
		Segments:   segments,
	}

	switch msg.Chat.Type {
	case "private":
		event.Kind = EventKindPrivate
		event.MessageType = "private"
	case "group", "supergroup", "channel":
		event.Kind = EventKindGroup
		event.MessageType = "group"
		event.GroupID = strconv.FormatInt(msg.Chat.ID, 10)
	default:
		return MessageEvent{}
	}

	if msg.From != nil {
		event.UserID = strconv.FormatInt(msg.From.ID, 10)
		event.SenderName = telegramDisplayName(msg.From)
	}

	// 私聊天然是对机器人说的；群里靠 @提及 判断。
	if event.Kind == EventKindPrivate {
		event.ToMe = true
	} else {
		event.ToMe = telegramMentionsBot(text, msg.Entities, selfID, botUsername)
	}
	return event
}

// telegramMentionsBot 判断消息是否 @ 了本机器人。
//
// 必须精确匹配 username：只要看到「有 @ 就算」的话，群里任何带邮箱或
// @别人的消息都会触发机器人。
//
// Bot API 的 entity offset/length 以 UTF-16 码元计，中文等非 BMP 前缀会让
// 直接按字节切片取错位置，所以先转成 UTF-16 再切。
func telegramMentionsBot(text string, entities []telegramEntity, selfID, botUsername string) bool {
	units := utf16.Encode([]rune(text))
	for _, entity := range entities {
		switch entity.Type {
		case "mention":
			if botUsername == "" {
				continue
			}
			if entity.Offset < 0 || entity.Offset+entity.Length > len(units) {
				continue
			}
			mention := string(utf16.Decode(units[entity.Offset : entity.Offset+entity.Length]))
			if strings.EqualFold(strings.TrimPrefix(mention, "@"), botUsername) {
				return true
			}
		case "text_mention":
			// 没有 username 的机器人不会收到 text_mention，这里仍按 ID 兜底。
			if entity.User != nil && selfID != "" && strconv.FormatInt(entity.User.ID, 10) == selfID {
				return true
			}
		}
	}
	return false
}

// telegramDisplayName 取「名 姓」，都没有时回落 username。
func telegramDisplayName(user *telegramUser) string {
	if user == nil {
		return ""
	}
	name := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	if name == "" {
		return user.Username
	}
	return name
}

func telegramAnyID(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return ""
}

// telegramMentionEntities 把提及位置翻成 Telegram 的 MessageEntity 列表。
//
// user.id 必须是数字：Telegram 的用户 id 就是数字，拿到非数字（比如脱敏别名没
// 还原干净，或者这条消息其实来自别的平台）就跳过这一条——宁可少一个可点击的
// 提及，也不能让整条消息因为参数非法发不出去。正文里的「@昵称」照常留着。
func telegramMentionEntities(mentions []dianaMentionSpan) []map[string]any {
	if len(mentions) == 0 {
		return nil
	}
	entities := make([]map[string]any, 0, len(mentions))
	for _, mention := range mentions {
		userID, err := strconv.ParseInt(strings.TrimSpace(mention.UserID), 10, 64)
		if err != nil {
			continue
		}
		entities = append(entities, map[string]any{
			"type":   "text_mention",
			"offset": mention.Offset,
			"length": mention.Length,
			"user":   map[string]any{"id": userID},
		})
	}
	if len(entities) == 0 {
		return nil
	}
	return entities
}
