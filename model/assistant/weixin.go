// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WeixinConfig 是微信（iLink Bot）通道的连接配置。凭据全部来自扫码登录，
// 不由用户手填。
type WeixinConfig struct {
	ProfileID string
	// BotToken 是扫码确认后服务端下发的 bot_token。
	BotToken string
	// BotID 是 ilink_bot_id，也是消息里 to_user_id 的那个值。
	BotID string
	// BaseURL 是扫码确认时服务端指定的接口地址，为空用默认地址。
	BaseURL    string
	CDNBaseURL string
	// StateDir 存游标和 context_token，留空跟着数据目录走。
	StateDir string
}

const (
	// weixinSessionPause 与官方实现一致：token 被判失效后整个账号停一小时再试，
	// 期间继续打接口只会反复吃同一个错误，还可能加重风控。
	weixinSessionPause = time.Hour

	weixinRetryInitial = 2 * time.Second
	weixinRetryMax     = 30 * time.Second
)

// WeixinChannel 通过 iLink Bot 接口接入个人微信。
//
// 收消息是出站长轮询（getupdates），不需要公网地址；游标 get_updates_buf 和每个
// 联系人最近一次的 context_token 落在本地，重启后接着收、接着回。iLink 目前只有
// 私聊：消息里没有可回发的群会话，带 group_id 的消息直接丢弃，不假装支持。
type WeixinChannel struct {
	mu      sync.RWMutex
	cfg     WeixinConfig
	handler EventHandler
	client  *http.Client
	cancel  context.CancelFunc

	statusMu sync.RWMutex
	status   ChannelStatus

	stateMu     sync.Mutex
	state       weixinState
	pausedUntil time.Time

	dedupe *eventDeduper

	retryInitial time.Duration
	retryMax     time.Duration
	pause        time.Duration
}

// weixinState 是需要跨重启保留的会话状态。
type weixinState struct {
	Cursor string `json:"get_updates_buf,omitempty"`
	// ContextTokens 按联系人记最近一次入站带来的 context_token。回消息必须原样
	// 带上它，否则服务端不知道这条回复属于哪段对话。
	ContextTokens map[string]string `json:"context_tokens,omitempty"`
}

// NewWeixinChannel 创建微信通道。
func NewWeixinChannel(cfg WeixinConfig) *WeixinChannel {
	return &WeixinChannel{
		cfg: cfg,
		// 长轮询自己控制每次调用的超时，这里不设全局超时。
		client:       &http.Client{},
		status:       ChannelStatus{Endpoint: firstNonEmpty(cfg.BaseURL, weixinDefaultAPIBase) + " (long-poll)", SelfID: cfg.BotID, UpdatedAt: time.Now()},
		dedupe:       newEventDeduper(10 * time.Minute),
		retryInitial: weixinRetryInitial,
		retryMax:     weixinRetryMax,
		pause:        weixinSessionPause,
	}
}

func (c *WeixinChannel) api() weixinAPI {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return weixinAPI{base: c.cfg.BaseURL, token: c.cfg.BotToken, client: c.client}
}

// Connect 长轮询收消息，直到 ctx 取消。
func (c *WeixinChannel) Connect(ctx context.Context, handler EventHandler) error {
	c.mu.Lock()
	cfg := c.cfg
	c.handler = handler
	c.mu.Unlock()

	if strings.TrimSpace(cfg.BotToken) == "" {
		// 没登录不是连接故障，重试也不会自己好。挂着等配置更新，免得外层按失败
		// 退避重连刷一屏日志。
		c.setStatus(false, "", "尚未扫码登录：请在 WebUI「机器人 → 接入」里扫码绑定微信")
		<-ctx.Done()
		return ctx.Err()
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	defer cancel()

	c.loadState()
	api := c.api()
	api.notify(runCtx, "notifystart")
	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), weixinLightTimeout)
		defer stop()
		api.notify(stopCtx, "notifystop")
	}()

	c.pollLoop(runCtx, api)
	c.setStatus(false, "", "")
	return runCtx.Err()
}

func (c *WeixinChannel) pollLoop(ctx context.Context, api weixinAPI) {
	backoff := c.retryInitial
	timeout := weixinLongPollTimeout
	for ctx.Err() == nil {
		if wait := c.pauseRemaining(); wait > 0 {
			if !sleepContext(ctx, wait) {
				return
			}
			continue
		}
		c.stateMu.Lock()
		cursor := c.state.Cursor
		c.stateMu.Unlock()

		resp, err := api.getUpdates(ctx, cursor, timeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if isWeixinStaleToken(err) {
				c.pauseSession()
				backoff = c.retryInitial
				continue
			}
			c.setStatus(false, "", weixinPollErrorText(err))
			log.Printf("weixin: getupdates failed: %v; retrying in %s", err, backoff)
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, c.retryMax)
			continue
		}
		backoff = c.retryInitial
		c.setStatus(true, "", "")
		if resp.LongPollingTimeoutMs > 0 {
			timeout = time.Duration(resp.LongPollingTimeoutMs) * time.Millisecond
		}
		if next := resp.GetUpdatesBuf; next != "" && next != cursor {
			c.stateMu.Lock()
			c.state.Cursor = next
			c.stateMu.Unlock()
			c.saveState()
		}
		for _, msg := range resp.Msgs {
			c.dispatch(ctx, api, msg)
		}
	}
}

// weixinPollErrorText 给 WebUI 一句能看懂的原因。业务错误原样带上 errmsg，
// 风控、频控这类拒绝只有服务端的原话说得清。
func weixinPollErrorText(err error) string {
	var apiErr *weixinAPIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("微信服务端拒绝收消息（ret=%d errcode=%d %s），正在退避重试；持续出现可能是账号被风控，请到手机微信确认状态", apiErr.Ret, apiErr.ErrCode, strings.TrimSpace(apiErr.ErrMsg))
	}
	return "连接微信失败，正在重试：" + err.Error()
}

func (c *WeixinChannel) pauseSession() {
	c.stateMu.Lock()
	c.pausedUntil = time.Now().Add(c.pause)
	until := c.pausedUntil
	c.stateMu.Unlock()
	msg := fmt.Sprintf("微信登录已失效（errcode %d），已暂停收发到 %s；如果之后仍然失效，请在 WebUI 重新扫码登录", weixinStaleTokenErrCode, until.Format("15:04"))
	c.setStatus(false, "", msg)
	log.Printf("weixin: %s", msg)
}

func (c *WeixinChannel) pauseRemaining() time.Duration {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.pausedUntil.IsZero() {
		return 0
	}
	remaining := time.Until(c.pausedUntil)
	if remaining <= 0 {
		c.pausedUntil = time.Time{}
		return 0
	}
	return remaining
}

func (c *WeixinChannel) dispatch(ctx context.Context, api weixinAPI, msg weixinMessage) {
	if msg.MessageType == weixinMessageTypeBot {
		return
	}
	if strings.TrimSpace(msg.GroupID) != "" {
		log.Printf("weixin: 丢弃群消息 group=%s：iLink 只支持私聊回复", msg.GroupID)
		return
	}
	from := strings.TrimSpace(msg.FromUserID)
	if from == "" {
		return
	}
	if token := strings.TrimSpace(msg.ContextToken); token != "" {
		c.rememberContextToken(from, token)
	}
	c.mu.RLock()
	selfID := c.cfg.BotID
	cdnBase := c.cfg.CDNBaseURL
	handler := c.handler
	c.mu.RUnlock()

	event, ok := weixinEventFromMessage(msg, selfID)
	if !ok || handler == nil {
		return
	}
	if id := strings.TrimSpace(event.MessageID); id != "" && !c.dedupe.Accept(id) {
		return
	}
	go func() {
		defer recoverGoroutinePanic("weixin.go:dispatch")
		eventCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		// 图片要先从 CDN 取回解密，放在协程里做，别卡住下一轮长轮询。
		c.attachImages(eventCtx, api, cdnBase, &event, msg)
		_ = handler(eventCtx, event)
	}()
}

// weixinEventFromMessage 把一条入站消息映射成统一事件，不做任何网络请求。
//
// 语义对照：
//   - from_user_id 形如 xxx@im.wechat，是对方在这个 bot 下的稳定 ID
//   - 没有昵称字段，SenderName 只能先用 ID 顶上
//   - iLink 的消息都是发给 bot 本身的私聊，一律 ToMe
func weixinEventFromMessage(msg weixinMessage, selfID string) (MessageEvent, bool) {
	userID := strings.TrimSpace(msg.FromUserID)
	if userID == "" {
		return MessageEvent{}, false
	}
	var parts []string
	hasImage := false
	var quoted *QuotedMessage
	for _, item := range msg.ItemList {
		switch item.Type {
		case weixinItemText:
			if item.TextItem != nil && strings.TrimSpace(item.TextItem.Text) != "" {
				parts = append(parts, strings.TrimSpace(item.TextItem.Text))
			}
		case weixinItemVoice:
			// 语音只取服务端的转写文字；原始 silk 音频暂不下载。
			if item.VoiceItem != nil && strings.TrimSpace(item.VoiceItem.Text) != "" {
				parts = append(parts, strings.TrimSpace(item.VoiceItem.Text))
			} else {
				parts = append(parts, "[语音]")
			}
		case weixinItemImage:
			hasImage = true
		case weixinItemFile:
			name := ""
			if item.FileItem != nil {
				name = strings.TrimSpace(item.FileItem.FileName)
			}
			parts = append(parts, strings.TrimSpace("[文件] "+name))
		case weixinItemVideo:
			parts = append(parts, "[视频]")
		}
		if quoted == nil && item.RefMsg != nil {
			quoted = weixinQuoted(item)
		}
	}
	text := strings.Join(parts, "\n")
	if text == "" && !hasImage {
		return MessageEvent{}, false
	}
	messageID := string(msg.MessageID)
	if messageID == "" {
		for _, item := range msg.ItemList {
			if item.MsgID != "" {
				messageID = string(item.MsgID)
				break
			}
		}
	}
	raw := text
	if raw == "" {
		raw = "[图片]"
	}
	event := MessageEvent{
		Kind:        EventKindPrivate,
		MessageType: "private",
		Time:        platformEventTime(msg.CreateTimeMs),
		SelfID:      strings.TrimSpace(selfID),
		MessageID:   messageID,
		RawMessage:  raw,
		UserID:      userID,
		SenderName:  userID,
		ToMe:        true,
		Quoted:      quoted,
	}
	if text != "" {
		event.Segments = platformTextSegments(text, "")
	}
	return event, true
}

// weixinQuoted 还原引用。新版客户端可能只给 svr_id 不给原文，这时只留 ID。
func weixinQuoted(item weixinMessageItem) *QuotedMessage {
	ref := item.RefMsg
	text := ""
	if ref.MessageItem != nil && ref.MessageItem.TextItem != nil {
		text = strings.TrimSpace(ref.MessageItem.TextItem.Text)
	}
	if text == "" {
		text = strings.TrimSpace(ref.Title)
	}
	if text == "" && ref.SvrID == "" {
		return nil
	}
	quoted := &QuotedMessage{MessageID: string(ref.SvrID), RawMessage: text}
	if text != "" {
		quoted.Segments = platformTextSegments(text, "")
	}
	return quoted
}

// attachImages 把入站图片取回、解密、落到历史媒体目录，再挂成 image 段。
// 取不回来就留一个占位文字，至少让模型知道对方发过图。
func (c *WeixinChannel) attachImages(ctx context.Context, api weixinAPI, cdnBase string, event *MessageEvent, msg weixinMessage) {
	for index, item := range msg.ItemList {
		if item.Type != weixinItemImage || item.ImageItem == nil {
			continue
		}
		body, err := api.downloadMedia(ctx, cdnBase, item.ImageItem.Media, item.ImageItem.AESKey)
		if err == nil {
			contentType := imageContentType(http.DetectContentType(body), body)
			if !strings.HasPrefix(contentType, "image/") {
				err = fmt.Errorf("weixin: 解密出的内容不是图片")
			} else {
				var path string
				path, err = writeHistoryImage(PlatformWeixin, event.Time, string(EventKindPrivate), "", event.UserID, fmt.Sprintf("%s-%d", event.MessageID, index), "weixin-cdn", body, contentType)
				if err == nil {
					event.Segments = append(event.Segments, MessageSegment{Type: "image", Data: map[string]string{"file": path, "cached_file": path}})
					continue
				}
			}
		}
		log.Printf("weixin: 入站图片取回失败 message=%s: %v", event.MessageID, err)
		event.Segments = append(event.Segments, MessageSegment{Type: "text", Data: map[string]string{"text": "[图片]"}})
	}
}

// Send 发送文本和图片。iLink 一次 sendmessage 只带一个 item，图文分开发。
func (c *WeixinChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	if strings.TrimSpace(msg.GroupID) != "" {
		return fmt.Errorf("weixin: iLink 只支持私聊，无法发往群 %s", msg.GroupID)
	}
	to := strings.TrimSpace(msg.UserID)
	if to == "" {
		return fmt.Errorf("weixin: 缺少接收人")
	}
	if wait := c.pauseRemaining(); wait > 0 {
		return fmt.Errorf("weixin: 登录已失效，暂停发送中（约 %d 分钟后重试），请在 WebUI 重新扫码登录", int(wait.Minutes())+1)
	}
	c.mu.RLock()
	token := c.cfg.BotToken
	cdnBase := c.cfg.CDNBaseURL
	c.mu.RUnlock()
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("weixin: 尚未扫码登录")
	}
	api := c.api()
	contextToken := c.contextToken(to)

	text := platformOutboundText(msg)
	images := msg.ImageURLs
	if len(msg.VideoURLs)+len(msg.AudioURLs) > 0 {
		// 视频和语音的上传协议还没对照实现，丢了要说出来，别让人以为发出去了。
		log.Printf("weixin: 跳过 %d 个视频、%d 个音频：暂不支持发送", len(msg.VideoURLs), len(msg.AudioURLs))
		if text == "" && len(images) == 0 {
			return fmt.Errorf("weixin: 暂不支持发送视频或语音")
		}
	}
	if msg.ImagesFirst {
		if err := c.sendImages(ctx, api, cdnBase, to, contextToken, images); err != nil {
			return err
		}
		images = nil
	}
	if text != "" {
		item := weixinMessageItem{Type: weixinItemText}
		item.TextItem = &struct {
			Text string `json:"text,omitempty"`
		}{Text: text}
		if err := c.sendItem(ctx, api, to, contextToken, item); err != nil {
			return err
		}
	}
	return c.sendImages(ctx, api, cdnBase, to, contextToken, images)
}

func (c *WeixinChannel) sendImages(ctx context.Context, api weixinAPI, cdnBase, to, contextToken string, sources []string) error {
	for _, source := range sources {
		body, _, err := readHistoryImageSource(ctx, source, 0)
		if err != nil {
			return fmt.Errorf("weixin: 读取待发送图片失败: %w", err)
		}
		uploaded, err := api.uploadImage(ctx, cdnBase, to, body)
		if err != nil {
			return c.checkSendError(err)
		}
		item := weixinMessageItem{Type: weixinItemImage, ImageItem: &weixinImageItem{
			Media: &weixinCDNMedia{
				EncryptQueryParam: uploaded.DownloadParam,
				// 官方实现把十六进制密钥串再做 base64，入站解码按 32 位十六进制那条路走。
				AESKey:      base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(uploaded.AESKey))),
				EncryptType: 1,
			},
			MidSize: uploaded.CiphertextSize,
		}}
		if err := c.sendItem(ctx, api, to, contextToken, item); err != nil {
			return err
		}
	}
	return nil
}

func (c *WeixinChannel) sendItem(ctx context.Context, api weixinAPI, to, contextToken string, item weixinMessageItem) error {
	_, err := api.sendMessage(ctx, weixinMessage{
		FromUserID:   "",
		ToUserID:     to,
		ClientID:     weixinClientID(),
		MessageType:  weixinMessageTypeBot,
		MessageState: weixinMessageStateEnd,
		ItemList:     []weixinMessageItem{item},
		ContextToken: contextToken,
	})
	return c.checkSendError(err)
}

func (c *WeixinChannel) checkSendError(err error) error {
	if err != nil && isWeixinStaleToken(err) {
		c.pauseSession()
	}
	return err
}

func weixinClientID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "diana-weixin-" + hex.EncodeToString(buf[:])
}

// CallAPI 透传 iLink 接口，action 是 ilink/bot/ 之后的路径，例如 "getconfig"。
func (c *WeixinChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	action = strings.Trim(strings.TrimSpace(action), "/")
	if action == "" {
		return nil, fmt.Errorf("weixin: 缺少接口路径")
	}
	payload := map[string]any{}
	for key, value := range params {
		payload[key] = value
	}
	payload["base_info"] = weixinBaseInfoPayload()
	raw, err := c.api().post(ctx, "ilink/bot/"+strings.TrimPrefix(action, "ilink/bot/"), payload, weixinAPITimeout)
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
func (c *WeixinChannel) Status() ChannelStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.status
}

func (c *WeixinChannel) setStatus(connected bool, selfID, lastErr string) {
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
func (c *WeixinChannel) Close() error {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// —— 本地状态 ——

func (c *WeixinChannel) rememberContextToken(userID, token string) {
	c.stateMu.Lock()
	if c.state.ContextTokens == nil {
		c.state.ContextTokens = map[string]string{}
	}
	changed := c.state.ContextTokens[userID] != token
	c.state.ContextTokens[userID] = token
	c.stateMu.Unlock()
	if changed {
		c.saveState()
	}
}

func (c *WeixinChannel) contextToken(userID string) string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state.ContextTokens[userID]
}

// statePath 按 bot ID 分文件：同一个微信号重新扫码后能接着用旧游标，换号则从头开始。
func (c *WeixinChannel) statePath() string {
	c.mu.RLock()
	dir := c.cfg.StateDir
	key := firstNonEmpty(c.cfg.BotID, c.cfg.ProfileID)
	c.mu.RUnlock()
	if strings.TrimSpace(key) == "" {
		return ""
	}
	if dir == "" {
		dir = weixinStateDir()
	}
	return filepath.Join(dir, safeHistoryPart(key)+".json")
}

func weixinStateDir() string {
	if dbPath := strings.TrimSpace(os.Getenv("APP_DB_PATH")); dbPath != "" {
		return filepath.Join(filepath.Dir(dbPath), "weixin")
	}
	if cacheDir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cacheDir, "diana", "weixin")
	}
	return "weixin"
}

func (c *WeixinChannel) loadState() {
	path := c.statePath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var state weixinState
	if err := json.Unmarshal(raw, &state); err != nil {
		log.Printf("weixin: 本地状态文件损坏，从头开始收消息: %v", err)
		return
	}
	c.stateMu.Lock()
	c.state = state
	c.stateMu.Unlock()
}

func (c *WeixinChannel) saveState() {
	path := c.statePath()
	if path == "" {
		return
	}
	c.stateMu.Lock()
	raw, err := json.Marshal(c.state)
	c.stateMu.Unlock()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("weixin: 保存本地状态失败: %v", err)
		return
	}
	// 先写临时文件再改名，进程在写到一半时退出也不会留下半截 JSON。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("weixin: 保存本地状态失败: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("weixin: 保存本地状态失败: %v", err)
	}
}
