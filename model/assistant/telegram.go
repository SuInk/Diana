// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

	"github.com/SuInk/diana/model/netguard"
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
	// telegramCloudDownloadLimit 是官方 Bot API getFile 能下载的上限。
	telegramCloudDownloadLimit = 20 << 20
)

// TelegramChannel 通过官方 Bot API 长轮询接入 Telegram。
//
// 选长轮询而不是 webhook：webhook 需要一个带证书的公网地址，家庭或内网
// 部署基本用不了；长轮询只要能出站访问 api.telegram.org 就行。
type TelegramChannel struct {
	// live 记录最近一次轮询完成的时间，轮询循环卡住时据此改报离线。
	live    receiveLiveness
	mu      sync.RWMutex
	cfg     TelegramConfig
	handler EventHandler

	statusMu sync.RWMutex
	status   ChannelStatus

	client *http.Client
	cancel context.CancelFunc
	// botUsername 来自 getMe，用于精确判断 @提及。
	botUsername  string
	knownMembers map[string]map[int64]telegramKnownMember
	// offset 是下一次 getUpdates 的起点，保证已处理的更新不会重复投递。
	offset int64
}

// NewTelegramChannel 创建 Telegram 通道。
func NewTelegramChannel(cfg TelegramConfig) *TelegramChannel {
	return &TelegramChannel{
		cfg:    cfg,
		client: telegramHTTPClient(cfg.ProxyURL),
		status: ChannelStatus{
			Platform:  PlatformTelegram,
			Endpoint:  telegramEndpointLabel(cfg),
			UpdatedAt: time.Now(),
		},
	}
}

// SetConfig 更新连接配置，代理变化时重建 HTTP 客户端。
func (c *TelegramChannel) SetConfig(cfg TelegramConfig) {
	c.mu.Lock()
	proxyChanged := c.cfg.ProxyURL != cfg.ProxyURL
	identityChanged := c.cfg.BotToken != cfg.BotToken || c.cfg.APIBaseURL != cfg.APIBaseURL
	if identityChanged {
		if c.cancel != nil {
			c.cancel()
			c.cancel = nil
		}
		c.knownMembers = nil
		c.offset = 0
		c.botUsername = ""
	}
	c.cfg = cfg
	if proxyChanged || c.client == nil {
		c.client = telegramHTTPClient(cfg.ProxyURL)
	}
	c.mu.Unlock()
	selfID := c.Status().SelfID
	if identityChanged {
		selfID = ""
	}
	c.setStatus(false, selfID, "")
	c.statusMu.Lock()
	if identityChanged {
		c.status.SelfID = ""
	}
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
	// 一次长轮询最多占 HTTP 超时那么久，两轮都没回来才算卡住。
	c.live.setLimit(2 * (telegramPollTimeoutSeconds + 20) * time.Second)
	c.live.mark(time.Now())
	defer c.live.reset()
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := c.fetchUpdates(ctx)
		c.live.mark(time.Now())
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.setStatus(false, c.Status().SelfID, err.Error())
			log.Printf("telegram: getUpdates failed: %v", err)
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
		c.setStatus(true, c.Status().SelfID, "")
		batchOK := true
		for _, update := range updates {
			if err := c.dispatch(ctx, update); err != nil {
				batchOK = false
				log.Printf("telegram: handle update failed: update_id=%d err=%v", update.UpdateID, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
				break
			}
			c.acknowledgeUpdate(update.UpdateID)
		}
		if batchOK {
			backoff = time.Second
		}
	}
}

func (c *TelegramChannel) fetchUpdates(ctx context.Context) ([]telegramUpdate, error) {
	params := map[string]any{
		"timeout": telegramPollTimeoutSeconds,
		// 只订阅消息类更新，避免拉回大量无关事件。message_reaction 是群友给消息挂的表情，
		// 群统计要用；Telegram 只在机器人是群管理员时推送它。chat_join_request 是开了
		// 「审核入群」的群里的入群申请，同样只推给有邀请权限的管理员机器人。
		"allowed_updates": []string{"message", "edited_message", "channel_post", "edited_channel_post", "chat_member", "my_chat_member", "message_reaction", "chat_join_request"},
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return updates, nil
}

func (c *TelegramChannel) acknowledgeUpdate(updateID int64) {
	c.mu.Lock()
	if updateID >= c.offset {
		c.offset = updateID + 1
	}
	c.mu.Unlock()
}

func (c *TelegramChannel) dispatch(ctx context.Context, update telegramUpdate) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.observeMemberUpdate(update.ChatMember, false)
	c.observeMemberUpdate(update.MyChatMember, true)
	if event := telegramBotMuteEvent(update.MyChatMember, c.Status().SelfID); event.Kind != "" {
		c.mu.RLock()
		handler := c.handler
		c.mu.RUnlock()
		if handler != nil {
			if err := handler(ctx, event); err != nil {
				log.Printf("telegram: handle mute change failed: update_id=%d chat_id=%s err=%v", update.UpdateID, event.GroupID, err)
			}
		}
	}
	if update.ChatJoinRequest != nil {
		c.mu.RLock()
		handler := c.handler
		c.mu.RUnlock()
		if handler == nil {
			return nil
		}
		if event, ok := telegramJoinRequestEvent(update.ChatJoinRequest, c.Status().SelfID); ok {
			if err := handler(ctx, event); err != nil {
				log.Printf("telegram: handle join request failed: update_id=%d chat_id=%s err=%v", update.UpdateID, event.GroupID, err)
			}
		}
		return nil
	}
	if update.MessageReaction != nil {
		c.mu.RLock()
		handler := c.handler
		c.mu.RUnlock()
		if handler == nil {
			return nil
		}
		if event := telegramReactionToEvent(update.MessageReaction, c.Status().SelfID); event.Kind != "" {
			if err := handler(ctx, event); err != nil {
				log.Printf("telegram: handle reaction failed: update_id=%d chat_id=%s message_id=%s err=%v", update.UpdateID, event.GroupID, event.MessageID, err)
			}
		}
		return nil
	}
	message := update.Message
	edited := false
	if message == nil {
		message = update.EditedMessage
		edited = message != nil
	}
	if message == nil {
		message = update.ChannelPost
	}
	if message == nil {
		message = update.EditedChannelPost
		edited = message != nil
	}
	if message == nil {
		return nil
	}
	c.observeMemberMessage(message)
	c.mu.RLock()
	handler := c.handler
	username := c.botUsername
	c.mu.RUnlock()
	selfID := c.Status().SelfID
	if handler == nil {
		return nil
	}
	event := telegramMessageToEvent(message, selfID, username)
	if event.Kind == "" {
		return nil
	}
	if edited {
		event.Edited = true
		event.EditTime = message.EditDate
		event.TelegramUpdateID = update.UpdateID
	}
	event = c.resolveIncomingMedia(ctx, event, message)
	if err := handler(ctx, event); err != nil {
		if edited {
			// An edit is itself the durable revision update. Keep its update_id
			// unacknowledged when storage fails so Telegram can redeliver it.
			return fmt.Errorf("handle edited chat %s message %s: %w", event.GroupID, event.MessageID, err)
		}
		// Ordinary messages already use Diana's durable inbound queue. Preserve
		// the established at-most-once transport behavior for paths that may
		// have performed external side effects before returning an error.
		log.Printf("telegram: handle update failed: update_id=%d chat_id=%s message_id=%s err=%v", update.UpdateID, event.GroupID, event.MessageID, err)
	}
	return nil
}

// Send 把统一的出站消息翻译成 Bot API 调用。
func (c *TelegramChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	_, err := c.SendWithResult(ctx, msg)
	return err
}

// SendWithResult 发送消息并把 Telegram 返回的 Message 交回上层。
//
// 上层靠这个返回值拿到平台侧的 message_id，把 Diana 自己这条发言连同 ID 记进历史
// （见 rememberOutgoingWithMessageID）。不实现这个接口的话，出站消息会以空 ID 入库，
// 于是别人「引用 Diana 刚说的那句话」时按 ID 回查必然落空——Diana 完全看不到自己
// 被引用了什么。
func (c *TelegramChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if !msg.LiteralText && strings.Contains(msg.Text, "[CQ:record") {
		return nil, fmt.Errorf("telegram: 未转换的 CQ 音频消息，拒绝按文本发送")
	}
	chatID := strings.TrimSpace(msg.GroupID)
	if chatID == "" {
		chatID = strings.TrimSpace(msg.UserID)
	}
	if chatID == "" {
		return nil, fmt.Errorf("telegram: missing chat id")
	}
	msg, cleanup, err := materializeTelegramInlineMedia(msg)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if msg.ImageAlbum {
		return c.sendPhotoAlbum(ctx, msg, chatID)
	}
	// 一次投递可能发出正文和若干媒体。回查引用时对得上的是正文那条，所以以它为准；
	// 纯媒体消息才退而用第一条媒体的返回值。
	var first map[string]any
	keep := func(result map[string]any) {
		if first == nil && len(result) > 0 {
			first = result
		}
	}

	if text := strings.TrimSpace(msg.Text); text != "" {
		var entities []telegramEntitySpec
		if !msg.LiteralText {
			// 有可信用户名时用 @username，否则以昵称和用户 ID 提及；Markdown 和
			// 提及共用 UTF-16 实体坐标，不依赖 parse_mode 或把昵称当成格式解析。
			text, entities = renderTelegramOutgoingText(text, msg.MentionNames, msg.MentionUsernames)
		}
		if utf16Length(text) > telegramTextLimit {
			return nil, fmt.Errorf("telegram: 文本超过单条消息上限，请降低回复字符上限或允许分条")
		}
		params := map[string]any{
			"chat_id":                  chatID,
			"text":                     text,
			"disable_web_page_preview": true,
		}
		if threadID := strings.TrimSpace(msg.MessageThreadID); threadID != "" {
			params["message_thread_id"] = threadID
		}
		if params2 := telegramEntityParams(entities); len(params2) > 0 {
			params["entities"] = params2
		}
		if reply := telegramReplyParameters(msg.ReplyMessageID); reply != nil {
			params["reply_parameters"] = reply
		}
		result, err := c.CallAPI(ctx, "sendMessage", params)
		if err != nil {
			return nil, err
		}
		keep(result)
	}

	for _, image := range msg.ImageURLs {
		method, field, err := telegramImageMethod(image)
		if err != nil {
			return nil, err
		}
		result, err := c.sendMedia(ctx, chatID, msg.MessageThreadID, method, field, image, msg.ReplyMessageID)
		if err != nil {
			return nil, err
		}
		keep(result)
	}
	for _, video := range msg.VideoURLs {
		result, err := c.sendMedia(ctx, chatID, msg.MessageThreadID, "sendVideo", "video", video, msg.ReplyMessageID)
		if err != nil {
			return nil, err
		}
		keep(result)
	}
	for _, audio := range msg.AudioURLs {
		result, err := c.sendMedia(ctx, chatID, msg.MessageThreadID, "sendAudio", "audio", audio, msg.ReplyMessageID)
		if err != nil {
			return nil, err
		}
		keep(result)
	}
	for _, segment := range msg.Segments {
		if segment.Type != "file" {
			continue
		}
		result, err := c.sendMedia(ctx, chatID, msg.MessageThreadID, "sendDocument", "document", segment.Data["file"], msg.ReplyMessageID)
		if err != nil {
			return nil, err
		}
		keep(result)
	}
	return first, nil
}

// SendTextDraft uses Telegram's native draft API. Telegram currently exposes
// drafts only for private chats; group replies keep the ordinary final-send
// behavior instead of simulating streaming with a trail of edited messages.
func (c *TelegramChannel) SendTextDraft(ctx context.Context, msg OutgoingMessage, draftID int64) error {
	chatID := strings.TrimSpace(msg.UserID)
	text := strings.TrimSpace(msg.Text)
	if chatID == "" || text == "" || strings.TrimSpace(msg.GroupID) != "" {
		return nil
	}
	text, entities := renderTelegramOutgoingText(text, msg.MentionNames, msg.MentionUsernames)
	text, entities = truncateTelegramDraft(text, entities)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	params := map[string]any{
		"chat_id":  chatID,
		"draft_id": draftID,
		"text":     text,
	}
	if threadID := strings.TrimSpace(msg.MessageThreadID); threadID != "" {
		params["message_thread_id"] = threadID
	}
	if entityParams := telegramEntityParams(entities); len(entityParams) > 0 {
		params["entities"] = entityParams
	}
	_, err := c.CallAPI(ctx, "sendMessageDraft", params)
	return err
}

func (c *TelegramChannel) SendChatAction(ctx context.Context, msg OutgoingMessage, action string) error {
	chatID := strings.TrimSpace(msg.GroupID)
	if chatID == "" {
		chatID = strings.TrimSpace(msg.UserID)
	}
	if chatID == "" || strings.TrimSpace(action) == "" {
		return nil
	}
	params := map[string]any{"chat_id": chatID, "action": strings.TrimSpace(action)}
	if threadID := strings.TrimSpace(msg.MessageThreadID); threadID != "" {
		params["message_thread_id"] = threadID
	}
	_, err := c.CallAPI(ctx, "sendChatAction", params)
	return err
}

func telegramReplyParameters(messageID string) map[string]any {
	messageID = strings.TrimSpace(messageID)
	for _, ch := range messageID {
		if ch < '0' || ch > '9' {
			return nil
		}
	}
	parsed, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil || parsed <= 0 {
		return nil
	}
	return map[string]any{"message_id": parsed, "allow_sending_without_reply": true}
}

func writeTelegramReplyParameters(writer *multipart.Writer, replyIDs ...string) error {
	if len(replyIDs) == 0 {
		return nil
	}
	reply := telegramReplyParameters(replyIDs[0])
	if reply == nil {
		return nil
	}
	encoded, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	return writer.WriteField("reply_parameters", string(encoded))
}

// sendMedia 发送单个媒体。远程 URL 直接交给 Telegram 去拉，本地文件走
// multipart 上传——Telegram 拉不到我们本机的 /media/resolver 地址。
func (c *TelegramChannel) sendMedia(ctx context.Context, chatID, threadID, method, field, source string, replyIDs ...string) (map[string]any, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, nil
	}
	path := telegramLocalPath(source)
	if path == "" {
		params := map[string]any{"chat_id": chatID, field: source}
		if len(replyIDs) > 0 {
			if reply := telegramReplyParameters(replyIDs[0]); reply != nil {
				params["reply_parameters"] = reply
			}
		}
		if threadID = strings.TrimSpace(threadID); threadID != "" {
			params["message_thread_id"] = threadID
		}
		return c.CallAPI(ctx, method, params)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("telegram: read media: %w", err)
	}
	if info.Size() > telegramMaxUploadBytes {
		return nil, fmt.Errorf("telegram: 媒体 %.1fMB 超过 Bot API 50MB 上传限制", float64(info.Size())/(1<<20))
	}
	return c.uploadMedia(ctx, method, chatID, threadID, field, path, replyIDs...)
}

// materializeTelegramInlineMedia 把 data URI / base64:// 媒体落成临时文件。
//
// Telegram 不认内联数据：既不是 URL 也不是本地文件的字符串会被当成 file_id，
// 结果是「wrong remote file identifier specified: Wrong padding length」。各个
// 调用方（关系图、渲染图、插件）都按 OneBot 的习惯交 data URI，在通道这一层统一
// 换成文件走 multipart，比要求每个入口各自记得转换可靠。
func materializeTelegramInlineMedia(msg OutgoingMessage) (OutgoingMessage, func(), error) {
	dir := ""
	cleanup := func() {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
	convert := func(sources []string, kind string) ([]string, error) {
		var out []string
		for index, source := range sources {
			trimmed := strings.TrimSpace(source)
			if !strings.HasPrefix(trimmed, "data:") && !strings.HasPrefix(trimmed, "base64://") {
				continue
			}
			body, err := decodeBase64MediaSource(trimmed, telegramMaxUploadBytes)
			if err != nil {
				return nil, fmt.Errorf("telegram: 内联%s数据无效或超过 50MB", kind)
			}
			if dir == "" {
				if dir, err = os.MkdirTemp("", "diana-telegram-inline-*"); err != nil {
					return nil, err
				}
			}
			path := filepath.Join(dir, fmt.Sprintf("%s-%d%s", kind, index, mediaExtensionFor(http.DetectContentType(body))))
			if err := os.WriteFile(path, body, 0o600); err != nil {
				return nil, err
			}
			if out == nil {
				out = append([]string(nil), sources...)
			}
			out[index] = path
		}
		if out == nil {
			return sources, nil
		}
		return out, nil
	}
	var err error
	if msg.ImageURLs, err = convert(msg.ImageURLs, "image"); err != nil {
		cleanup()
		return msg, func() {}, err
	}
	if msg.VideoURLs, err = convert(msg.VideoURLs, "video"); err != nil {
		cleanup()
		return msg, func() {}, err
	}
	if msg.AudioURLs, err = convert(msg.AudioURLs, "audio"); err != nil {
		cleanup()
		return msg, func() {}, err
	}
	return msg, cleanup, nil
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

func (c *TelegramChannel) uploadMedia(ctx context.Context, method, chatID, threadID, field, path string, replyIDs ...string) (map[string]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("telegram: open media: %w", err)
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writeTelegramReplyParameters(writer, replyIDs...); err != nil {
		return nil, err
	}
	if err := writer.WriteField("chat_id", chatID); err != nil {
		return nil, err
	}
	if threadID = strings.TrimSpace(threadID); threadID != "" {
		if err := writer.WriteField("message_thread_id", threadID); err != nil {
			return nil, err
		}
	}
	part, err := writer.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	endpoint, err := c.methodURL(method)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	raw, err := c.do(req)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(raw) == 0 || json.Unmarshal(raw, &out) != nil {
		return nil, nil
	}
	return out, nil
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

// telegramAPIError keeps the Bot API status machine-readable. Callers such as
// the recall probe must distinguish a missing message from throttling or a
// permission error; flattening every response into a string makes that unsafe.
type telegramAPIError struct {
	Code              int
	Description       string
	RetryAfterSeconds int
}

func (e *telegramAPIError) Error() string {
	if e == nil {
		return "telegram: unknown API error"
	}
	return fmt.Sprintf("telegram: %s (code %d)", e.Description, e.Code)
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
		// net/http wraps transport failures in url.Error, whose Error method
		// includes the request URL. Telegram puts the bot token in that URL's
		// path, so returning the wrapper would leak the credential into status
		// responses and reconnect logs.
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			return nil, fmt.Errorf("telegram: request failed: %w", urlErr.Err)
		}
		return nil, fmt.Errorf("telegram: request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := netguard.ReadResponseBody(resp, 8<<20)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("telegram: bad response (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if !envelope.OK {
		return nil, &telegramAPIError{
			Code:              envelope.ErrorCode,
			Description:       envelope.Description,
			RetryAfterSeconds: envelope.Parameters.RetryAfter,
		}
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
	status := c.status
	c.statusMu.RUnlock()
	return c.live.apply("telegram", status, time.Now())
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

const telegramPinnedMessageNoticeSubType = "group_pin"

type telegramUpdate struct {
	ChatMember        *telegramMemberUpdate `json:"chat_member,omitempty"`
	MyChatMember      *telegramMemberUpdate `json:"my_chat_member,omitempty"`
	UpdateID          int64                 `json:"update_id"`
	Message           *telegramMessage      `json:"message,omitempty"`
	EditedMessage     *telegramMessage      `json:"edited_message,omitempty"`
	ChannelPost       *telegramMessage      `json:"channel_post,omitempty"`
	EditedChannelPost *telegramMessage      `json:"edited_channel_post,omitempty"`
	MessageReaction   *telegramReaction     `json:"message_reaction,omitempty"`
	ChatJoinRequest   *telegramJoinRequest  `json:"chat_join_request,omitempty"`
}

// telegramReaction 是 Bot API 的 MessageReactionUpdated：某人对某条消息挂着的表情变了。
// new_reaction 是变化之后的完整集合，所以按整体替换处理。
type telegramReaction struct {
	Chat        *telegramChat          `json:"chat"`
	MessageID   int64                  `json:"message_id"`
	User        *telegramUser          `json:"user,omitempty"`
	Date        int64                  `json:"date"`
	NewReaction []telegramReactionType `json:"new_reaction"`
}

type telegramReactionType struct {
	Type          string `json:"type"`
	Emoji         string `json:"emoji,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
}

type telegramMessage struct {
	MessageID       int64            `json:"message_id"`
	MessageThreadID int64            `json:"message_thread_id,omitempty"`
	Date            int64            `json:"date"`
	EditDate        int64            `json:"edit_date,omitempty"`
	Text            string           `json:"text"`
	Caption         string           `json:"caption"`
	From            *telegramUser    `json:"from"`
	SenderChat      *telegramChat    `json:"sender_chat"`
	Chat            *telegramChat    `json:"chat"`
	NewChatMembers  []telegramUser   `json:"new_chat_members,omitempty"`
	LeftChatMember  *telegramUser    `json:"left_chat_member,omitempty"`
	PinnedMessage   *telegramMessage `json:"pinned_message,omitempty"`
	ReplyTo         *telegramMessage `json:"reply_to_message"`
	// Quote 是用户只选中被回复消息里一段话时 Telegram 给的那一段。
	Quote *telegramTextQuote `json:"quote,omitempty"`
	// ExternalReply 是回复别的聊天或别的话题里的消息，这时 reply_to_message 为空。
	ExternalReply     *telegramExternalReply `json:"external_reply,omitempty"`
	IsTopicMessage    bool                   `json:"is_topic_message,omitempty"`
	ForumTopicCreated json.RawMessage        `json:"forum_topic_created,omitempty"`
	Entities          []telegramEntity       `json:"entities"`
	CaptionEntities   []telegramEntity       `json:"caption_entities"`
	Photo             []telegramPhoto        `json:"photo"`
	Video             *telegramFile          `json:"video,omitempty"`
	Animation         *telegramFile          `json:"animation,omitempty"`
	Sticker           *telegramSticker       `json:"sticker,omitempty"`
	Audio             *telegramFile          `json:"audio,omitempty"`
	Voice             *telegramFile          `json:"voice,omitempty"`
	Document          *telegramFile          `json:"document,omitempty"`
}

type telegramTextQuote struct {
	Text     string `json:"text"`
	IsManual bool   `json:"is_manual,omitempty"`
}

// telegramExternalReply 是 Bot API 的 ExternalReplyInfo：只有来源和媒体，正文要从
// 同一条消息的 quote 里拿。
type telegramExternalReply struct {
	Origin    *telegramMessageOrigin `json:"origin,omitempty"`
	Chat      *telegramChat          `json:"chat,omitempty"`
	MessageID int64                  `json:"message_id,omitempty"`
	Photo     []telegramPhoto        `json:"photo,omitempty"`
	Video     *telegramFile          `json:"video,omitempty"`
	Animation *telegramFile          `json:"animation,omitempty"`
	Sticker   *telegramSticker       `json:"sticker,omitempty"`
	Audio     *telegramFile          `json:"audio,omitempty"`
	Voice     *telegramFile          `json:"voice,omitempty"`
	Document  *telegramFile          `json:"document,omitempty"`
}

type telegramMessageOrigin struct {
	Type            string        `json:"type"`
	SenderUser      *telegramUser `json:"sender_user,omitempty"`
	SenderUserName  string        `json:"sender_user_name,omitempty"`
	SenderChat      *telegramChat `json:"sender_chat,omitempty"`
	Chat            *telegramChat `json:"chat,omitempty"`
	AuthorSignature string        `json:"author_signature,omitempty"`
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
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileID       string `json:"file_id"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type telegramSticker struct {
	FileID       string         `json:"file_id"`
	FileUniqueID string         `json:"file_unique_id"`
	Type         string         `json:"type,omitempty"`
	Emoji        string         `json:"emoji,omitempty"`
	SetName      string         `json:"set_name,omitempty"`
	IsAnimated   bool           `json:"is_animated,omitempty"`
	IsVideo      bool           `json:"is_video,omitempty"`
	FileSize     int64          `json:"file_size,omitempty"`
	Thumbnail    *telegramPhoto `json:"thumbnail,omitempty"`
	Thumb        *telegramPhoto `json:"thumb,omitempty"`
}

type telegramFile struct {
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileID       string `json:"file_id"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type telegramPinnedMessageView struct {
	MessageID       string   `json:"message_id"`
	MessageThreadID string   `json:"message_thread_id,omitempty"`
	EventTime       int64    `json:"event_time,omitempty"`
	SenderUserID    string   `json:"sender_user_id,omitempty"`
	Sender          string   `json:"sender,omitempty"`
	Text            string   `json:"text,omitempty"`
	ContentTypes    []string `json:"content_types,omitempty"`
}

// telegramPinnedMessageFromChat 把 getChat 返回的当前置顶收敛成工具需要的稳定字段。
// Bot API 只给当前 pinned_message，没有历史列表；没有置顶时返回 nil。
func telegramPinnedMessageFromChat(data map[string]any) (*telegramPinnedMessageView, error) {
	fields := data
	if nested, ok := data["result"].(map[string]any); ok {
		fields = nested
	}
	raw, exists := fields["pinned_message"]
	if !exists || raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("telegram: encode pinned message: %w", err)
	}
	var message telegramMessage
	if err := json.Unmarshal(encoded, &message); err != nil {
		return nil, fmt.Errorf("telegram: decode pinned message: %w", err)
	}
	if message.MessageID <= 0 {
		return nil, fmt.Errorf("telegram: pinned message has no valid message id")
	}
	view := &telegramPinnedMessageView{
		MessageID:       strconv.FormatInt(message.MessageID, 10),
		MessageThreadID: telegramOptionalID(message.MessageThreadID),
		EventTime:       message.Date,
		Text:            firstNonEmpty(strings.TrimSpace(message.Text), strings.TrimSpace(message.Caption)),
	}
	if message.From != nil {
		view.SenderUserID = strconv.FormatInt(message.From.ID, 10)
		view.Sender = telegramDisplayName(message.From)
	} else if message.SenderChat != nil {
		view.SenderUserID = strconv.FormatInt(message.SenderChat.ID, 10)
		view.Sender = strings.TrimSpace(message.SenderChat.Title)
	}
	addType := func(kind string, present bool) {
		if present {
			view.ContentTypes = append(view.ContentTypes, kind)
		}
	}
	addType("text", view.Text != "")
	addType("image", len(message.Photo) > 0)
	addType("video", message.Video != nil)
	addType("animation", message.Animation != nil)
	addType("sticker", message.Sticker != nil)
	addType("audio", message.Audio != nil)
	addType("voice", message.Voice != nil)
	addType("file", message.Document != nil)
	return view, nil
}

// telegramMessageToEvent 把 Bot API 消息映射成统一事件。
//
// 语义对照：
//   - chat.type private -> 私聊；group/supergroup/channel -> 群聊，chat.id 当群号
//   - from.id -> 用户 ID；Telegram 没有 OneBot 那样的群等级，SenderLevel 保持 0，
//     ReplyGate 的等级门槛会按「读不到即放行」处理
//   - ToMe 由文本里的 @username 提及判断
//
// telegramReactionToEvent 把表情回应变化翻成统一的 message_reaction 通知。
// 匿名管理员以群身份挂的表情没有 user，统计不了是谁，直接忽略。
func telegramReactionToEvent(reaction *telegramReaction, selfID string) MessageEvent {
	if reaction == nil || reaction.Chat == nil || reaction.User == nil || reaction.MessageID == 0 || reaction.Chat.Type == "private" {
		return MessageEvent{}
	}
	emojis := make([]string, 0, len(reaction.NewReaction))
	for _, item := range reaction.NewReaction {
		switch item.Type {
		case "emoji":
			emojis = append(emojis, item.Emoji)
		case "custom_emoji":
			emojis = append(emojis, "custom:"+item.CustomEmojiID)
		case "paid":
			emojis = append(emojis, "⭐")
		}
	}
	messageID := strconv.FormatInt(reaction.MessageID, 10)
	return MessageEvent{
		Kind:        EventKindNotice,
		SubType:     messageReactionSubType,
		Time:        reaction.Date,
		SelfID:      selfID,
		MessageID:   messageID,
		MessageType: "notice",
		GroupID:     strconv.FormatInt(reaction.Chat.ID, 10),
		GroupName:   strings.TrimSpace(reaction.Chat.Title),
		UserID:      strconv.FormatInt(reaction.User.ID, 10),
		SenderName:  telegramDisplayName(reaction.User),
		Segments:    []MessageSegment{messageReactionSegment(messageID, emojis, messageReactionReplace)},
	}
}

func telegramMessageToEvent(msg *telegramMessage, selfID, botUsername string) MessageEvent {
	if msg == nil || msg.Chat == nil {
		return MessageEvent{}
	}
	// Telegram 把新置顶作为一条服务消息推送。它不是聊天正文，不应触发自动回复；
	// 映射成 notice 后仍会进入插件观察和通知审计，供上层追踪置顶变更。
	if msg.PinnedMessage != nil && msg.PinnedMessage.MessageID != 0 && msg.Chat.Type != "private" {
		pinned := telegramQuotedMessage(msg.PinnedMessage)
		if pinned != nil && strings.TrimSpace(pinned.GroupID) == "" {
			pinned.GroupID = strconv.FormatInt(msg.Chat.ID, 10)
		}
		pinnedID := strconv.FormatInt(msg.PinnedMessage.MessageID, 10)
		data := map[string]string{"notice_type": telegramPinnedMessageNoticeSubType, "pinned_message_id": pinnedID}
		operatorID := ""
		if msg.From != nil {
			operatorID = strconv.FormatInt(msg.From.ID, 10)
			data["operator_id"] = operatorID
		}
		userID, senderName := "", ""
		if msg.PinnedMessage.From != nil {
			userID = strconv.FormatInt(msg.PinnedMessage.From.ID, 10)
			senderName = telegramDisplayName(msg.PinnedMessage.From)
		}
		return MessageEvent{
			Kind: EventKindNotice, SubType: telegramPinnedMessageNoticeSubType,
			Time: msg.Date, SelfID: selfID, MessageID: strconv.FormatInt(msg.MessageID, 10), MessageType: "notice",
			GroupID: strconv.FormatInt(msg.Chat.ID, 10), GroupName: strings.TrimSpace(msg.Chat.Title),
			UserID: userID, OperatorID: operatorID, SenderName: senderName, Quoted: pinned,
			RawMessage: "[群置顶消息已更新]", Segments: []MessageSegment{{Type: "notice", Data: data}},
		}
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
			GroupName:   strings.TrimSpace(msg.Chat.Title),
			UserID:      strconv.FormatInt(joined.ID, 10),
			SenderName:  telegramDisplayName(&joined),
		}
	}

	// 退群同理：映射成和 OneBot 一样的 group_decrease，退群审计才能跨平台共用。
	// 操作者不是本人就是被踢。
	if msg.LeftChatMember != nil && msg.Chat.Type != "private" {
		left := msg.LeftChatMember
		leftID := strconv.FormatInt(left.ID, 10)
		subType, operatorID := "leave", ""
		if msg.From != nil && msg.From.ID != left.ID {
			subType, operatorID = "kick", strconv.FormatInt(msg.From.ID, 10)
		}
		data := map[string]string{"notice_type": "group_decrease", "sub_type": subType}
		if operatorID != "" {
			data["operator_id"] = operatorID
		}
		return MessageEvent{
			Kind:        EventKindNotice,
			SubType:     "group_decrease",
			Time:        msg.Date,
			SelfID:      selfID,
			MessageID:   strconv.FormatInt(msg.MessageID, 10),
			MessageType: "notice",
			GroupID:     strconv.FormatInt(msg.Chat.ID, 10),
			GroupName:   strings.TrimSpace(msg.Chat.Title),
			UserID:      leftID,
			OperatorID:  operatorID,
			SenderName:  telegramDisplayName(left),
			Segments:    []MessageSegment{{Type: "notice", Data: data}},
		}
	}

	text := msg.Text
	entities := msg.Entities
	if text == "" {
		text = msg.Caption
		entities = msg.CaptionEntities
	}

	// 回复关系落成与 OneBot 同一种 reply 段，模型看到的引用标记因此跨平台一致。
	quoted := telegramReplyQuote(msg)
	segments := make([]MessageSegment, 0, 2)
	replyID := ""
	if reply := telegramRepliedMessage(msg); reply != nil {
		replyID = strconv.FormatInt(reply.MessageID, 10)
	} else if quoted != nil {
		replyID = quoted.MessageID
	}
	if replyID != "" {
		segments = append(segments, MessageSegment{
			Type: "reply",
			Data: map[string]string{"id": replyID},
		})
	}
	segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": text}})
	segments = append(segments, telegramMediaSegments(telegramMediaOf(msg))...)

	event := MessageEvent{
		Platform:        PlatformTelegram,
		MentionTargets:  telegramMentionTargets(text, entities, selfID, botUsername),
		Time:            msg.Date,
		SelfID:          selfID,
		SelfUsername:    strings.TrimSpace(botUsername),
		MessageID:       strconv.FormatInt(msg.MessageID, 10),
		MessageThreadID: telegramOptionalID(msg.MessageThreadID),
		RawMessage:      text,
		Segments:        segments,
		Quoted:          quoted,
	}

	switch msg.Chat.Type {
	case "private":
		event.Kind = EventKindPrivate
		event.MessageType = "private"
	case "group", "supergroup", "channel":
		event.Kind = EventKindGroup
		event.MessageType = "group"
		event.GroupID = strconv.FormatInt(msg.Chat.ID, 10)
		// Bot API 没有「列出我加入的群」，控制台只能从消息里认识这个群，
		// 群名也只有这一个来源。
		event.GroupName = strings.TrimSpace(msg.Chat.Title)
	default:
		return MessageEvent{}
	}

	if msg.From != nil {
		event.SenderIsBot = msg.From.IsBot && msg.SenderChat == nil
		event.UserID = strconv.FormatInt(msg.From.ID, 10)
		event.SenderName = telegramDisplayName(msg.From)
		if msg.SenderChat == nil && !msg.From.IsBot {
			event.SenderUsername = msg.From.Username
		}
		if msg.SenderChat == nil && msg.From.ID > 0 {
			username := msg.From.Username
			event.SenderMentionUsername = &username
		}
	}

	// 私聊天然是对机器人说的；群里靠 @提及 判断。
	if event.Kind == EventKindPrivate {
		event.ToMe = true
	} else {
		event.ToMe = telegramMentionsBot(text, entities, selfID, botUsername)
	}
	return event
}

func telegramOptionalID(value int64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func (c *TelegramChannel) resolveIncomingMedia(ctx context.Context, event MessageEvent, msg *telegramMessage) MessageEvent {
	for index, segment := range event.Segments {
		fileID := strings.TrimSpace(segment.Data["file_id"])
		if fileID == "" || (segment.Type != "image" && segment.Type != "video" && segment.Type != "record" && segment.Type != "file") {
			continue
		}
		path, err := c.downloadIncomingFile(ctx, event, segment)
		if err != nil {
			log.Printf("telegram: download incoming media failed: chat_id=%s message_id=%s type=%s err=%v", event.GroupID, event.MessageID, segment.Type, err)
			continue
		}
		data := cloneSegmentData(segment.Data)
		data["cached_file"] = path
		data["file"] = path
		data[imageContentSHA256Key] = filepath.Base(filepath.Dir(path))
		event.Segments[index].Data = data
	}
	return event
}

func (c *TelegramChannel) downloadIncomingFile(ctx context.Context, event MessageEvent, segment MessageSegment) (string, error) {
	dir, err := historyMediaDir()
	if err != nil {
		return "", err
	}
	c.mu.RLock()
	key := "telegram:" + c.cfg.APIBaseURL + ":" + c.cfg.BotToken + ":" + strings.TrimSpace(segment.Data["file_id"])
	base := firstNonEmpty(strings.TrimRight(strings.TrimSpace(c.cfg.APIBaseURL), "/"), telegramDefaultAPIBase)
	c.mu.RUnlock()
	// 官方 Bot API 的 getFile 只给 20MB 以内的文件，超过必回 file is too big；
	// 自建 Bot API Server 没有这个限制，照常下载。
	if base == telegramDefaultAPIBase && telegramSegmentOverCloudLimit(segment) {
		return "", fmt.Errorf("telegram: file is %s bytes, over the %d-byte Bot API download limit", segment.Data["file_size"], telegramCloudDownloadLimit)
	}
	legacyKey := key
	if uniqueID := strings.TrimSpace(segment.Data["file_unique_id"]); uniqueID != "" {
		key = "telegram-unique:" + base + ":" + uniqueID
	}
	return fetchMediaContent(ctx, dir, key, telegramMaxUploadBytes, func(ctx context.Context) ([]byte, string, string, error) {
		// Upgrade existing file-id entries without downloading their content again.
		if key != legacyKey {
			if path := cachedMediaSource(dir, legacyKey, telegramMaxUploadBytes); path != "" {
				body, err := os.ReadFile(path)
				return body, segment.Data["mime"], filepath.Base(path), err
			}
		}
		body, remotePath, err := c.downloadFileByID(ctx, strings.TrimSpace(segment.Data["file_id"]), telegramMaxUploadBytes)
		return body, segment.Data["mime"], filepath.Base(firstNonEmpty(segment.Data["name"], remotePath, "media")), err
	})
}

// telegramSegmentOverCloudLimit 判断这个媒体段是否超过官方 Bot API 的下载上限。
func telegramSegmentOverCloudLimit(segment MessageSegment) bool {
	size, err := strconv.ParseInt(strings.TrimSpace(segment.Data["file_size"]), 10, 64)
	return err == nil && size > telegramCloudDownloadLimit
}

// telegramOversizedMediaReason 给取不到内容的 Telegram 媒体一句真实原因。超过
// 20MB 的视频/文件机器人根本下载不到，笼统说「没有关键帧」会让模型猜成格式问题。
func telegramOversizedMediaReason(event MessageEvent) string {
	if event.Platform != PlatformTelegram {
		return ""
	}
	for _, segment := range event.Segments {
		if segment.Data["cached_file"] != "" || !telegramSegmentOverCloudLimit(segment) {
			continue
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(segment.Data["file_size"]), 10, 64)
		return fmt.Sprintf("Telegram 机器人接口只能下载 20MB 以内的文件，这条媒体约 %.1fMB，取不到原文件，无法查看内容", float64(size)/(1<<20))
	}
	return ""
}

// downloadFileByID 把 file_id 换成文件内容。下载地址里带着 Bot Token
// （/file/bot<token>/...），所以这个 URL 只能留在进程内部，任何情况下都不能
// 交给浏览器去请求。
func (c *TelegramChannel) downloadFileByID(ctx context.Context, fileID string, maxBytes int) ([]byte, string, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil, "", fmt.Errorf("telegram: file id is required")
	}
	result, err := c.CallAPI(ctx, "getFile", map[string]any{"file_id": fileID})
	if err != nil {
		return nil, "", err
	}
	remotePath := strings.TrimSpace(stringFromAny(result["file_path"]))
	if remotePath == "" {
		return nil, "", fmt.Errorf("getFile returned no file_path")
	}
	c.mu.RLock()
	cfg := c.cfg
	client := c.client
	c.mu.RUnlock()
	base := strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL), "/")
	if base == "" {
		base = telegramDefaultAPIBase
	}
	if client == nil {
		client = http.DefaultClient
	}
	downloadURL := base + "/file/bot" + strings.TrimSpace(cfg.BotToken) + "/" + strings.TrimLeft(remotePath, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("telegram: invalid file request")
	}
	downloadClient := *client
	downloadClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := downloadClient.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			err = urlErr.Err
		}
		return nil, "", fmt.Errorf("telegram: file download failed: %s", strings.ReplaceAll(err.Error(), cfg.BotToken, "[redacted]"))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("file download HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) == 0 || len(body) > maxBytes {
		return nil, "", fmt.Errorf("downloaded media size is invalid")
	}
	return body, remotePath, nil
}

func writeTelegramHistoryMedia(event MessageEvent, segment MessageSegment, remotePath string, body []byte) (string, error) {
	category := map[string]string{"image": "image", "video": "video", "record": "audio", "file": "file"}[segment.Type]
	return writeHistoryMedia(historyMediaWriteRequest{Platform: PlatformTelegram, EventTime: event.Time, TargetKind: string(event.Kind), GroupID: event.GroupID, UserID: event.UserID, MessageID: event.MessageID, Category: category, Source: segment.Data["file_id"], FileName: filepath.Base(firstNonEmpty(segment.Data["name"], remotePath, "media")), Body: body, ContentType: segment.Data["mime"]})
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
			mention, ok := telegramEntityText(units, entity)
			if !ok {
				continue
			}
			if strings.EqualFold(strings.TrimPrefix(mention, "@"), botUsername) {
				return true
			}
		case "bot_command":
			// 群里 /cmd@username 是 Telegram 指定机器人的标准写法，但 Bot API
			// 只给一个 bot_command entity，不会再补 mention。不认这里，发给
			// 自己的命令就成了「别人的指令」，被当场忽略。
			if botUsername == "" {
				continue
			}
			command, ok := telegramEntityText(units, entity)
			if !ok {
				continue
			}
			if _, target, found := strings.Cut(command, "@"); found && strings.EqualFold(target, botUsername) {
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

// telegramEntityText 按 entity 的 UTF-16 偏移取原文，越界时返回 false。
func telegramEntityText(units []uint16, entity telegramEntity) (string, bool) {
	if entity.Offset < 0 || entity.Length < 0 || entity.Offset+entity.Length > len(units) {
		return "", false
	}
	return string(utf16.Decode(units[entity.Offset : entity.Offset+entity.Length])), true
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

// telegramMedia 是消息里可能带的媒体字段。Message 和 ExternalReplyInfo 各有一份，
// 收成同一个形状后正文和引用共用一套段构造。
type telegramMedia struct {
	Photo     []telegramPhoto
	Video     *telegramFile
	Animation *telegramFile
	Sticker   *telegramSticker
	Audio     *telegramFile
	Voice     *telegramFile
	Document  *telegramFile
}

func telegramMediaOf(msg *telegramMessage) telegramMedia {
	if msg == nil {
		return telegramMedia{}
	}
	return telegramMedia{Photo: msg.Photo, Video: msg.Video, Animation: msg.Animation, Sticker: msg.Sticker, Audio: msg.Audio, Voice: msg.Voice, Document: msg.Document}
}

func telegramExternalMediaOf(reply *telegramExternalReply) telegramMedia {
	if reply == nil {
		return telegramMedia{}
	}
	return telegramMedia{Photo: reply.Photo, Video: reply.Video, Animation: reply.Animation, Sticker: reply.Sticker, Audio: reply.Audio, Voice: reply.Voice, Document: reply.Document}
}

// telegramMediaSegments 把媒体字段落成消息段。图片取最大的那张；动画和视频贴纸
// 不能直接交给图片模型，改用 Telegram 同时给的静态缩略图。
func telegramMediaSegments(media telegramMedia) []MessageSegment {
	var segments []MessageSegment
	if len(media.Photo) > 0 {
		photo := media.Photo[len(media.Photo)-1]
		segments = append(segments, MessageSegment{Type: "image", Data: map[string]string{"file_id": photo.FileID, "file_unique_id": photo.FileUniqueID, "file_size": strconv.FormatInt(photo.FileSize, 10)}})
	}
	if sticker := media.Sticker; sticker != nil && strings.TrimSpace(sticker.FileID) != "" {
		fileID := sticker.FileID
		fileUniqueID := sticker.FileUniqueID
		fileSize := sticker.FileSize
		name := "sticker.webp"
		mime := "image/webp"
		usable := true
		if sticker.IsAnimated || sticker.IsVideo {
			thumbnail := sticker.Thumbnail
			if thumbnail == nil {
				// 兼容较旧 Bot API server 使用的 thumb 字段。
				thumbnail = sticker.Thumb
			}
			if thumbnail == nil || strings.TrimSpace(thumbnail.FileID) == "" {
				usable = false
			} else {
				fileID = thumbnail.FileID
				fileUniqueID = thumbnail.FileUniqueID
				fileSize = thumbnail.FileSize
				name = "sticker-preview.webp"
			}
		}
		if usable {
			summary := strings.TrimSpace(sticker.Emoji)
			if summary == "" {
				summary = "Telegram 贴纸"
			}
			segments = append(segments, MessageSegment{Type: "image", Data: map[string]string{
				"file_id": fileID, "file_unique_id": fileUniqueID, "file_size": strconv.FormatInt(fileSize, 10), "name": name, "mime": mime,
				"sub_type": "telegram_sticker", "summary": summary, "emoji": strings.TrimSpace(sticker.Emoji),
				"sticker_set": strings.TrimSpace(sticker.SetName), "sticker_type": strings.TrimSpace(sticker.Type),
				"sticker_file_id": sticker.FileID, "sticker_unique_id": strings.TrimSpace(sticker.FileUniqueID),
			}})
		}
	}
	appendFile := func(kind string, file *telegramFile) {
		if file == nil || strings.TrimSpace(file.FileID) == "" {
			return
		}
		segments = append(segments, MessageSegment{Type: kind, Data: map[string]string{
			"file_id": file.FileID, "file_unique_id": file.FileUniqueID, "name": file.FileName, "mime": file.MimeType, "file_size": strconv.FormatInt(file.FileSize, 10),
		}})
	}
	appendFile("video", media.Video)
	appendFile("video", media.Animation)
	appendFile("record", media.Voice)
	appendFile("record", media.Audio)
	appendFile("file", media.Document)
	return segments
}

// telegramRepliedMessage 返回这条消息真正在回复的那条。
//
// 论坛话题里，没有回复任何人的普通消息也带着 reply_to_message，指向话题创建时的
// 服务消息（forum_topic_created）：话题本身就是一条消息，Desktop 等客户端发到话题里
// 时会挂上这层回复。把它当引用，话题里每句话都成了「回复话题标题」，模型会反复针对
// 一个空引用作答，话题若是机器人开的还会被误判成在回复机器人。
func telegramRepliedMessage(msg *telegramMessage) *telegramMessage {
	if msg == nil || msg.ReplyTo == nil || msg.ReplyTo.MessageID == 0 {
		return nil
	}
	reply := msg.ReplyTo
	if len(reply.ForumTopicCreated) > 0 {
		return nil
	}
	if msg.IsTopicMessage && msg.MessageThreadID != 0 && reply.MessageID == msg.MessageThreadID {
		return nil
	}
	return reply
}

// telegramReplyQuote 把 reply_to_message 里内联的引用内容直接填成 Quoted。
//
// OneBot 只给一个消息 id，引用内容得回头再查一次（get_msg）；Telegram 不一样，
// 它在同一条更新里就把被引用消息的正文和发送者一并送来了。上游此前只取了 id、
// 把内容丢掉，于是引用还原完全依赖「这条消息 Diana 以前见过并存过」——被引用的
// 是重启前的旧消息、或超出保留范围的消息时就还原不出来，模型自然没法针对引用
// 做语义分析。既然内容是白给的，就不该扔掉再去查。
//
// 用户只选中原消息里的一段来回复时，Telegram 另给一个 quote：那一段才是对方要接的
// 话，用它代替整条正文。回复别的聊天里的消息时 reply_to_message 为空，内容在
// external_reply（来源和媒体）和 quote（正文）里。
func telegramReplyQuote(msg *telegramMessage) *QuotedMessage {
	if msg == nil {
		return nil
	}
	quoteText := ""
	if msg.Quote != nil {
		quoteText = strings.TrimSpace(msg.Quote.Text)
	}
	if reply := telegramRepliedMessage(msg); reply != nil {
		if quoteText == "" {
			return telegramQuotedMessage(reply)
		}
		partial := *reply
		partial.Text, partial.Caption = quoteText, ""
		return telegramQuotedMessage(&partial)
	}
	external := msg.ExternalReply
	if external == nil {
		return nil
	}
	quoted := &QuotedMessage{RawMessage: quoteText}
	if origin := external.Origin; origin != nil {
		switch {
		case origin.SenderUser != nil:
			quoted.UserID = strconv.FormatInt(origin.SenderUser.ID, 10)
			quoted.SenderName = telegramDisplayName(origin.SenderUser)
		case origin.SenderChat != nil:
			quoted.UserID = strconv.FormatInt(origin.SenderChat.ID, 10)
			quoted.SenderName = strings.TrimSpace(origin.SenderChat.Title)
		case origin.Chat != nil:
			quoted.UserID = strconv.FormatInt(origin.Chat.ID, 10)
			quoted.SenderName = strings.TrimSpace(firstNonEmpty(origin.AuthorSignature, origin.Chat.Title))
		default:
			quoted.SenderName = strings.TrimSpace(origin.SenderUserName)
		}
	}
	chat := external.Chat
	if chat == nil && external.Origin != nil {
		chat = external.Origin.Chat
	}
	if chat != nil && chat.ID != 0 && external.MessageID != 0 {
		// 别的聊天里的消息号和本群的消息号是两套编号：直接用数字会撞上本群同号的
		// 另一条消息，从历史里还原出一句毫不相干的话。加上来源聊天做前缀，只用于
		// 展示和去重，不会被当成本群的消息去查。
		quoted.MessageID = "external:" + strconv.FormatInt(chat.ID, 10) + ":" + strconv.FormatInt(external.MessageID, 10)
	}
	if chat != nil && chat.ID != 0 {
		// 跨聊天引用一定不是在回复本群里的机器人发言，群号填来源聊天，
		// explicitlyRepliesToBot 据此不把它算成直接回复。
		quoted.GroupID = strconv.FormatInt(chat.ID, 10)
	}
	return finishTelegramQuoted(quoted, quoteText, telegramExternalMediaOf(external))
}

// telegramQuotedMessage 把一条被引用（或被置顶）的消息转成 Quoted。
func telegramQuotedMessage(reply *telegramMessage) *QuotedMessage {
	if reply == nil || reply.MessageID == 0 {
		return nil
	}
	text := reply.Text
	if text == "" {
		text = reply.Caption
	}
	quoted := &QuotedMessage{
		MessageID:  strconv.FormatInt(reply.MessageID, 10),
		SenderName: telegramDisplayName(reply.From),
		RawMessage: text,
	}
	if reply.From != nil {
		quoted.UserID = strconv.FormatInt(reply.From.ID, 10)
		if reply.SenderChat == nil && reply.From.ID > 0 {
			username := reply.From.Username
			quoted.SenderMentionUsername = &username
		}
	}
	if reply.Chat != nil && reply.Chat.Type != "private" {
		quoted.GroupID = strconv.FormatInt(reply.Chat.ID, 10)
	}
	return finishTelegramQuoted(quoted, text, telegramMediaOf(reply))
}

func finishTelegramQuoted(quoted *QuotedMessage, text string, media telegramMedia) *QuotedMessage {
	if text != "" {
		quoted.Segments = append(quoted.Segments, MessageSegment{Type: "text", Data: map[string]string{"text": text}})
	}
	// 引用的是图片、贴纸、语音或文件时留下可见的占位，否则模型只看到空引用，
	// 会以为对方引了个寂寞。
	quoted.Segments = append(quoted.Segments, telegramMediaSegments(media)...)
	if len(quoted.Segments) == 0 && quoted.RawMessage == "" && quoted.SenderName == "" {
		return nil
	}
	return quoted
}
