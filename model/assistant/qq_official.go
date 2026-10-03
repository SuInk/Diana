// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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

	// qqDedupErrorCode 是开放平台「消息被去重，请检查请求msgseq」的错误码。同一
	// msg_id 下连发多条时偶发命中，换一个 msg_seq 重发一次即可通过。
	qqDedupErrorCode = "40054005"

	// qqQuotaExhaustedErrorCode 是「被动回复时间或次数超过限制」的错误码，重试无益。
	qqQuotaExhaustedErrorCode = "40034128"

	// qqMessageTypeQuote 标记入站消息是引用消息：被引用的正文与附件直接放在顶层
	// msg_elements 里，引用索引在 message_scene.ext 的 ref_msg_idx，入站不出现
	// message_reference 字段。
	qqMessageTypeQuote = 103

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
	guildChannels map[string]string
	avatarURLs    map[string]string
	mu            sync.RWMutex
	cfg           QQOfficialConfig
	// apiBaseOverride 只给测试指向本地服务。
	apiBaseOverride string
	handler         EventHandler
	client          *http.Client
	cancel          context.CancelFunc

	statusMu sync.RWMutex
	status   ChannelStatus

	tokens *platformTokenCache

	// sessionID 和 lastSeq 用于断线后 RESUME，避免重连丢事件。
	connMu    sync.Mutex
	conn      *websocket.Conn
	sessionID string
	lastSeq   int64

	seqMu      sync.Mutex
	passiveSeq map[string]qqPassiveSeq

	// sent 记着最近发出的群消息，用来认出全量模式下平台回推的自发消息。
	sent qqSentLedger
	// refs 是 REFIDX_ 引用索引：入站按 message_scene.ext 的 msg_idx 登记，出站按
	// 发送响应 ext_info.ref_idx 登记，收到引用消息时用 ref_msg_idx 反查。
	refs qqRefLedger
}

type qqPassiveSeq struct {
	seq int
	at  time.Time
}

// NewQQOfficialChannel 创建 QQ 官方机器人通道。
func NewQQOfficialChannel(cfg QQOfficialConfig) *QQOfficialChannel {
	channel := &QQOfficialChannel{
		cfg:    cfg,
		client: &http.Client{Timeout: 60 * time.Second},
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
	c.guildChannels = nil
	c.avatarURLs = nil
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
	if c.apiBaseOverride != "" {
		return c.apiBaseOverride
	}
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
	go func() {
		defer recoverGoroutinePanic("qqOfficial.heartbeatLoop")
		c.heartbeatLoop(sessionCtx, conn, interval)
	}()

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
	var source qqOfficialMessage
	_ = json.Unmarshal(payload.Data, &source)
	if payload.T == "GROUP_MESSAGE_CREATE" && source.Author.Bot && event.UserID != event.SelfID &&
		c.sent.matches(event.GroupID, event.MessageID, event.RawMessage, time.Now()) {
		// 回推的 author.id 对不上 READY 给的 selfID 时，靠发送记录兜底认出自己。
		// selfID 还没拿到就没法交给运行时按自发消息处理，只能直接丢掉。
		if event.SelfID == "" {
			return
		}
		event.UserID = event.SelfID
	}
	c.mu.Lock()
	if event.GuildID != "" {
		if c.guildChannels == nil {
			c.guildChannels = map[string]string{}
		}
		c.guildChannels[event.GroupID] = event.GuildID
	}
	if source.Author.Avatar != "" {
		if c.avatarURLs == nil {
			c.avatarURLs = map[string]string{}
		}
		c.avatarURLs[event.GroupID+"\x00"+event.UserID] = source.Author.Avatar
	}
	c.mu.Unlock()

	// 入站登记：本条消息的 msg_idx 到发言者，以及出站 ref_idx 到自己，都进引用
	// 索引；随后用 ref_msg_idx 反查被引用者，回填到事件里。晚一步登记就查不到
	// 自己刚才那条，所以必须在 handler 之前做。
	if key := source.sceneExtValue("msg_idx"); key != "" {
		c.refs.recordInbound(key, event.MessageID, event.UserID, source.Author.Bot, time.Now())
	}
	if event.Quoted != nil && event.Quoted.MessageID != "" {
		if sender, _, ok := c.refs.lookup(event.Quoted.MessageID, time.Now()); ok && sender != "" {
			event.Quoted.UserID = sender
			if sender == event.SelfID {
				// 引用的正是自己的发言：当被点名处理。
				event.ToMe = true
			}
		}
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
// 上层靠它把 Diana 自己这条发言连同平台 ID 记进历史；而别人引用这条消息时，
// 入站事件给的是 message_scene.ext 的 ref_msg_idx（REFIDX_ 键空间，与这里的 id
// 不是同一个空间），回查靠发送响应的 ext_info.ref_idx 登记进引用索引，不靠这个
// id。不带 id 的话出站消息以空 ID 入库，历史锚点就断了。
func (c *QQOfficialChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	target, isGroup := platformChatTarget(msg)
	if target == "" {
		return nil, fmt.Errorf("qq: 缺少会话标识")
	}
	text := platformOutboundText(msg)
	if text == "" && len(msg.ImageURLs) == 0 && len(msg.VideoURLs) == 0 && len(msg.AudioURLs) == 0 {
		return nil, nil
	}
	endpoint := c.apiBase() + "/v2/users/" + target + "/messages"
	c.mu.RLock()
	knownGuild := c.guildChannels[target]
	c.mu.RUnlock()
	isGuild := isGroup && (msg.PlatformScope == "qq_guild" || knownGuild != "")
	if isGroup {
		endpoint = c.apiBase() + "/v2/groups/" + target + "/messages"
	}
	if isGuild {
		endpoint = c.apiBase() + "/channels/" + url.PathEscape(target) + "/messages"
	}
	auth, err := c.authHeader(ctx)
	if err != nil {
		return nil, err
	}
	// 群里的主动消息能力已被平台关闭，不带 msg_id 会被 40034105 拒收；被动回复
	// 用触发这轮的入站消息 ID，拿不到才退回引用目标。
	replyID := firstNonEmpty(strings.TrimSpace(msg.PassiveReplyMessageID), strings.TrimSpace(msg.ReplyMessageID))
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	// post 发一条消息。同一个 msg_id 下的多条回复要靠递增的 msg_seq 区分，否则第二条起被去重拒收。
	post := func(body map[string]any) (string, error) {
		if replyID != "" {
			body["msg_id"] = replyID
			if !isGuild {
				body["msg_seq"] = c.nextPassiveSeq(replyID)
			}
		}
		request := func() ([]byte, error) {
			return platformJSONRequest(ctx, client, http.MethodPost, endpoint, map[string]string{
				"Authorization": auth,
			}, body)
		}
		raw, err := request()
		if err != nil && strings.Contains(err.Error(), "http 401") {
			// token 过期时开放平台返回 401；丢掉缓存让下一次重新换。
			c.tokens.Invalidate()
		}
		if err != nil && strings.Contains(err.Error(), qqDedupErrorCode) && replyID != "" && !isGuild {
			// 40054005「消息被去重」：换一个 msg_seq 重发一次可通过，原样重发
			// 只会再次被拒。
			body["msg_seq"] = c.nextPassiveSeq(replyID)
			raw, err = request()
			if err != nil && strings.Contains(err.Error(), "http 401") {
				c.tokens.Invalidate()
			}
		}
		if err != nil {
			return "", fmt.Errorf("qq: 发送失败: %w", err)
		}
		var envelope struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			ID      string `json:"id"`
			ExtInfo *struct {
				RefIdx string `json:"ref_idx"`
			} `json:"ext_info"`
		}
		if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Code != 0 {
			return "", fmt.Errorf("qq: 发送被拒绝: %s (code %d)", envelope.Message, envelope.Code)
		}
		// ext_info.ref_idx 是这条自发消息的引用索引键，别人引用这条消息时入站
		// ref_msg_idx 就会等于它，不登记就无法反查被引用的是自己哪句话。频道消息不登记。
		if !isGuild && envelope.ExtInfo != nil {
			c.refs.recordOutbound(envelope.ExtInfo.RefIdx, envelope.ID, c.Status().SelfID, time.Now())
		}
		return strings.TrimSpace(envelope.ID), nil
	}

	var firstID string
	if text != "" {
		// msg_type 0 是纯文本；频道消息没有这个字段。
		body := map[string]any{"content": text}
		if !isGuild {
			body["msg_type"] = 0
			// 出站引用与被动回复正交：message_reference 渲染引用卡片，msg_id 永远指向
			// 触发本轮的消息，两者同体携带互不干扰。引用目标按官方字段说明取 REFIDX_
			// 形态，从引用索引反查；查不到（进程重启后索引丢失、目标超出时效）就不带
			// 引用照常发送，避免引用值非法被平台整条拒收。引用只挂在文本消息上。
			if quoteRef := c.refs.refIdxFor(msg.ReplyMessageID, time.Now()); quoteRef != "" {
				body["message_reference"] = map[string]any{"message_id": quoteRef}
			}
		}
		id, err := post(body)
		if err != nil {
			return nil, err
		}
		if isGroup && !isGuild {
			c.sent.record(target, id, text, time.Now())
		}
		firstID = id
	}
	if isGuild && (len(msg.ImageURLs) > 0 || len(msg.VideoURLs) > 0 || len(msg.AudioURLs) > 0) {
		return nil, fmt.Errorf("qq: 频道消息暂不支持发送富媒体")
	}
	prefix := c.apiBase() + "/v2/users/" + target
	if isGroup {
		prefix = c.apiBase() + "/v2/groups/" + target
	}
	// sendMedia 上传一个媒体并以 msg_type 7 的富媒体消息发出。图片、视频、语音
	// 共用同一条分片上传链路，差别只在 file_type。
	sendMedia := func(source string, fileType int, maxBytes int64) (string, error) {
		fileInfo, err := c.qqUploadMedia(ctx, auth, prefix, source, fileType, maxBytes)
		if err != nil {
			return "", err
		}
		return post(map[string]any{
			"msg_type": 7,
			"media":    map[string]any{"file_info": fileInfo},
		})
	}
	for _, source := range msg.ImageURLs {
		if strings.TrimSpace(source) == "" {
			continue
		}
		id, err := sendMedia(source, qqFileTypeImage, qqMaxImageBytes)
		if err != nil {
			return nil, err
		}
		if firstID == "" {
			firstID = id
		}
	}
	for _, source := range msg.VideoURLs {
		if strings.TrimSpace(source) == "" {
			continue
		}
		id, err := sendMedia(source, qqFileTypeVideo, qqMaxVideoBytes)
		if err != nil {
			return nil, err
		}
		if firstID == "" {
			firstID = id
		}
	}
	for _, source := range msg.AudioURLs {
		if strings.TrimSpace(source) == "" {
			continue
		}
		id, err := sendMedia(source, qqFileTypeAudio, qqMaxAudioBytes)
		if err != nil {
			return nil, err
		}
		if firstID == "" {
			firstID = id
		}
	}
	if firstID != "" {
		return map[string]any{"message_id": firstID}, nil
	}
	return nil, nil
}

// qqSentLedgerTTL 是发送记录的有效期；平台回推通常在一两秒内，留足余量即可。
const qqSentLedgerTTL = 2 * time.Minute

// qqSentLedgerCap 限制记录条数，防止刷屏时无限增长。
const qqSentLedgerCap = 256

// qqRefLedgerTTL 是 REFIDX_ 引用索引的有效期。REFIDX_ 本身无时效说明，这里与
// 会话历史窗口对齐：更早的消息在历史里也找不到了，索引留着没意义。
const qqRefLedgerTTL = 24 * time.Hour

// qqRefLedgerCap 限制索引条数，防止长期运行无限增长。
const qqRefLedgerCap = 1024

// qqRefLedger 登记两条键空间到发送者身份的映射：
//   - 出站：发送响应 ext_info.ref_idx → 自己（selfID）
//   - 入站：message_scene.ext.msg_idx → 发言者
//
// 别人引用一条消息时，入站事件只给 ref_msg_idx，引用元素不带 author，
// 想知道「被引用的是谁说的」只能靠这张表反查。
type qqRefLedger struct {
	mu      sync.Mutex
	entries []qqRefEntry
}

type qqRefEntry struct {
	// key 是 REFIDX_ 引用键；msgID 是同一消息的平台 id（入站 d.id / 出站发送响应
	// id）。出站引用（message_reference）按官方字段说明取 REFIDX_ 形态，而运行时
	// 侧引用目标用的是平台 id，两个方向都靠这张表互查。
	key   string
	msgID string
	user  string
	bot   bool
	at    time.Time
}

// recordInbound 登记一条入站消息的 msg_idx。speaker 已经是统一事件的 UserID。
func (l *qqRefLedger) recordInbound(key, msgID, userID string, bot bool, now time.Time) {
	l.record(qqRefEntry{key: strings.TrimSpace(key), msgID: strings.TrimSpace(msgID), user: strings.TrimSpace(userID), bot: bot, at: now}, now)
}

// recordOutbound 登记一次出站发送返回的 ext_info.ref_idx。被引用者是自己，
// 记下 selfID 供反查。
func (l *qqRefLedger) recordOutbound(key, msgID, selfID string, now time.Time) {
	l.record(qqRefEntry{key: strings.TrimSpace(key), msgID: strings.TrimSpace(msgID), user: strings.TrimSpace(selfID), bot: true, at: now}, now)
}

func (l *qqRefLedger) record(entry qqRefEntry, now time.Time) {
	if entry.key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	for i := range l.entries {
		if l.entries[i].key == entry.key {
			l.entries[i] = entry
			return
		}
	}
	l.entries = append(l.entries, entry)
	if len(l.entries) > qqRefLedgerCap {
		l.entries = l.entries[len(l.entries)-qqRefLedgerCap:]
	}
}

// lookup 反查一个引用索引键（入站 ref_msg_idx / msg_idx）。取不到返回空。
func (l *qqRefLedger) lookup(key string, now time.Time) (userID string, isBot bool, ok bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	for i := range l.entries {
		if l.entries[i].key == key {
			return l.entries[i].user, l.entries[i].bot, true
		}
	}
	return "", false, false
}

// refIdxFor 由消息的平台 id（入站 d.id / 出站发送响应 id）反查它的 REFIDX_
// 引用键。没登记过（进程重启后索引丢失、目标超出时效）返回空串。
func (l *qqRefLedger) refIdxFor(msgID string, now time.Time) string {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	for i := range l.entries {
		if l.entries[i].msgID == msgID {
			return l.entries[i].key
		}
	}
	return ""
}

func (l *qqRefLedger) pruneLocked(now time.Time) {
	keep := 0
	for _, entry := range l.entries {
		if now.Sub(entry.at) <= qqRefLedgerTTL {
			l.entries[keep] = entry
			keep++
		}
	}
	l.entries = l.entries[:keep]
}

// qqSentLedger 记录最近发出的群消息。
//
// 开启「获取群内全部消息」后，机器人自己的发言会以 GROUP_MESSAGE_CREATE 回推。
// 只按 author.id == selfID 认自己的话，平台一旦只带 member_openid 就会把回声
// 当成群友发言，接话策略可能让它接自己的话。消息 id 对得上最可靠；回推 id 与
// 发送返回的 id 不同空间时，再退到同群、同正文、有效期内的匹配。调用方只在
// author.bot 为真时才查，所以正文匹配不会误伤真人复读。
type qqSentLedger struct {
	mu      sync.Mutex
	entries []qqSentEntry
}

type qqSentEntry struct {
	group string
	id    string
	text  string
	at    time.Time
}

func (l *qqSentLedger) record(group, id, text string, now time.Time) {
	text = strings.TrimSpace(text)
	if group == "" || (id == "" && text == "") {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	l.entries = append(l.entries, qqSentEntry{group: group, id: id, text: text, at: now})
	if len(l.entries) > qqSentLedgerCap {
		l.entries = l.entries[len(l.entries)-qqSentLedgerCap:]
	}
}

// matches 判断一条入站群消息是不是自己刚发的；命中后移除该记录，避免同一条
// 正文之后被重复认领。
func (l *qqSentLedger) matches(group, id, text string, now time.Time) bool {
	text = strings.TrimSpace(text)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	hit := -1
	for i, entry := range l.entries {
		if entry.group != group {
			continue
		}
		if id != "" && entry.id == id {
			hit = i
			break
		}
		if hit < 0 && text != "" && entry.text == text {
			hit = i
		}
	}
	if hit < 0 {
		return false
	}
	l.entries = append(l.entries[:hit], l.entries[hit+1:]...)
	return true
}

func (l *qqSentLedger) pruneLocked(now time.Time) {
	keep := 0
	for _, entry := range l.entries {
		if now.Sub(entry.at) <= qqSentLedgerTTL {
			l.entries[keep] = entry
			keep++
		}
	}
	l.entries = l.entries[:keep]
}

// CallAPI 透传开放平台的 REST 接口，action 形如 "GET /users/@me/guilds"。
// nextPassiveSeq 给同一个 msg_id 的第 N 次被动回复分配 msg_seq（从 1 起）。
func (c *QQOfficialChannel) nextPassiveSeq(msgID string) int {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	now := time.Now()
	if c.passiveSeq == nil {
		c.passiveSeq = map[string]qqPassiveSeq{}
	}
	for id, entry := range c.passiveSeq {
		// 群聊被动回复窗口 5 分钟、单聊 60 分钟，超过一小时的记录不会再被用到。
		if now.Sub(entry.at) > time.Hour {
			delete(c.passiveSeq, id)
		}
	}
	entry := c.passiveSeq[msgID]
	entry.seq++
	entry.at = now
	c.passiveSeq[msgID] = entry
	return entry.seq
}

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
	endpoint, err := platformRequestURL(c.apiBase(), path, method, params)
	if err != nil {
		return nil, err
	}
	raw, err := platformJSONRequest(ctx, client, method, endpoint, map[string]string{
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
	// Attachments 携带图片、语音、文件等富媒体，content 里只有文字；不解析它们，
	// 群友发的图就整个进不到上下文。
	Attachments []qqOfficialAttachment `json:"attachments"`
	// GroupOpenID 只在群消息里出现，是这个群对本机器人的稳定标识。
	GroupOpenID string `json:"group_openid"`
	// GroupID 是全量群消息（GROUP_MESSAGE_CREATE）里携带的群标识，与 group_openid 同值，取不到后者时兜底。
	GroupID string `json:"group_id"`
	// Mentions 只在全量群消息里出现：@ 了机器人时带 is_you 标记。
	Mentions []qqOfficialMention `json:"mentions"`
	// ChannelID / GuildID 出现在频道消息里。
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	Timestamp any    `json:"timestamp"`
	Author    struct {
		Avatar string `json:"avatar,omitempty"`
		ID     string `json:"id"`
		// Bot 标记发言者是机器人；机器人自己的发言也会在全量群消息里回推。
		Bot bool `json:"bot,omitempty"`
		// UserOpenID 是单聊里的用户标识；MemberOpenID 是群里的。
		UserOpenID   string `json:"user_openid"`
		MemberOpenID string `json:"member_openid"`
		Username     string `json:"username"`
	} `json:"author"`
	Member struct {
		Nick  string   `json:"nick"`
		Roles []string `json:"roles"`
	} `json:"member"`
	// MessageReference 属于发送方向协议，入站事件不携带；保留解析以兼容频道消息。
	MessageReference *struct {
		MessageID string `json:"message_id"`
	} `json:"message_reference"`
	// MessageType 标记消息形态：0 纯文本、102 聊天记录（合并转发原件）、103 引用。
	MessageType int `json:"message_type"`
	// MsgElements 只在引用消息（MessageType=103）出现，是被引用消息的正文与
	// 附件，随事件直接下发，无需回查历史。
	MsgElements []qqOfficialMsgElement `json:"msg_elements"`
	// MessageScene.ext 是 key=value 字符串数组：msg_idx 是本条消息的引用索引键，
	// ref_msg_idx 指向被引用消息；auth_token 是敏感值，不解析。
	MessageScene *qqOfficialMessageScene `json:"message_scene"`
}

// qqOfficialMessageScene 是消息自带的场景信息，只有 ext 数组被用到。
type qqOfficialMessageScene struct {
	Ext []string `json:"ext"`
}

// sceneExtValue 取 message_scene.ext 里某个键的值，取不到返回空串。值是整串
// 比对（REFIDX_ 键不可解析），不能按 = 切分——base64 值理论上可能带 = 填充。
func (m *qqOfficialMessage) sceneExtValue(key string) string {
	if m.MessageScene == nil {
		return ""
	}
	prefix := key + "="
	for _, item := range m.MessageScene.Ext {
		if value, ok := strings.CutPrefix(strings.TrimSpace(item), prefix); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// qqOfficialMsgElement 是引用消息里被引用内容的一个元素。content 是拍平的正文，
// attachments 与顶层附件同构；元素的 message_type 恒为 103 而非被引用消息的真实
// 类型、不含 author，都不解析。
type qqOfficialMsgElement struct {
	Content     string                 `json:"content"`
	Attachments []qqOfficialAttachment `json:"attachments"`
}

// qqOfficialMention 是全量群消息里 @ 目标的描述。
type qqOfficialMention struct {
	ID           string `json:"id"`
	OpenID       string `json:"openid"`
	UserOpenID   string `json:"user_openid"`
	MemberOpenID string `json:"member_openid"`
	// IsYou 是网关给的「这个 @ 指向当前机器人」标记。
	IsYou bool `json:"is_you"`
}

// pointsTo 判断这个 @ 是否指向当前机器人：优先认网关给的 is_you，拿不到时再按
// 各个 id 字段和 selfID 比对。
func (m qqOfficialMention) pointsTo(selfID string) bool {
	if m.IsYou {
		return true
	}
	for _, id := range []string{m.ID, m.OpenID, m.UserOpenID, m.MemberOpenID} {
		if id != "" && id == selfID {
			return true
		}
	}
	return false
}

// botMentioned 判断这条全量群消息是否叫了当前机器人。
func (m *qqOfficialMessage) botMentioned(selfID string) bool {
	for _, mention := range m.Mentions {
		if mention.pointsTo(selfID) {
			return true
		}
	}
	return false
}

// stripQQBotMentionTokens 去掉 content 里残留的机器人 mention 标记。
//
// 文档说 content 已去除 @ 前缀，但标记形如 <@openid>，解析不到位就会把一串十六
// 进制带进模型上下文，所以按 mentions 里指向自己的条目再剥一次。
func stripQQBotMentionTokens(text string, mentions []qqOfficialMention, selfID string) string {
	for _, mention := range mentions {
		if !mention.pointsTo(selfID) {
			continue
		}
		for _, id := range []string{mention.ID, mention.OpenID, mention.UserOpenID, mention.MemberOpenID, selfID} {
			if id == "" {
				continue
			}
			text = strings.ReplaceAll(text, "<@"+id+">", "")
			text = strings.ReplaceAll(text, "<@!"+id+">", "")
		}
	}
	return strings.TrimSpace(text)
}

// qqOfficialAttachment 是消息附带的一条富媒体附件。官方只给元信息和下载地址，
// 图片、语音、视频、文件都靠 content_type 区分。
//
// content_type 不全是 MIME：语音是裸值 voice（附带 asr_refer_text 平台转写和
// voice_wav_url），文件是裸值 file（md / zip / mp3 的取值相同，区分只能靠 filename
// 扩展名）。size 用于文件解析决定是否嗅探，其余字段不使用就不声明。
type qqOfficialAttachment struct {
	ContentType  string `json:"content_type"`
	Filename     string `json:"filename"`
	URL          string `json:"url"`
	Size         int64  `json:"size"`
	AsrReferText string `json:"asr_refer_text"`
}

var (
	// qqFaceTagPattern 匹配 content 里的表情标签，实到过两种形态：表情商城的
	// <faceType=4,faceId="",ext="…">，以及系统表情的 <faceType=6,faceId="0",ext="…">。
	qqFaceTagPattern = regexp.MustCompile(`<faceType=\d+[^>]*>`)
	// qqFaceExtPattern 从单个表情标签里抠出 base64 的 ext 字段。
	qqFaceExtPattern = regexp.MustCompile(`ext="([^"]*)"`)
)

// qqOfficialFaceText 把 content 里的表情标签翻成可读文本。
//
// 名字只藏在标签的 ext 里：base64 解开是 JSON，text 字段就是表情名，名字自带方括号
// （如 [吃芒果]）。原样透传会把一串协议码带进模型上下文，翻成 [表情:[吃芒果]] 模型
// 才分得清表情和正文里写的字。名字取不到时（ext 里的 text 为空，或标签里根本没有
// ext）退回 [表情]，至少让模型知道这里有个表情。
//
// 表情图不在这里：官方对表情商城这类只给名字、不给图，自家表情则是空名标签配一张
// 附件图，图案由 qqOfficialMediaSegments 单独拆成图片段，这个占位不代替它。
func qqOfficialFaceText(content string) string {
	if !strings.Contains(content, "<faceType") {
		return content
	}
	return qqFaceTagPattern.ReplaceAllStringFunc(content, func(tag string) string {
		if name := qqOfficialFaceTagName(tag); name != "" {
			return "[表情:" + name + "]"
		}
		return "[表情]"
	})
}

// qqOfficialFaceTagName 解出表情标签里的名字，取不到返回空串。
func qqOfficialFaceTagName(tag string) string {
	match := qqFaceExtPattern.FindStringSubmatch(tag)
	if match == nil {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(match[1])
	if err != nil {
		return ""
	}
	var payload struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(decoded, &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.Text)
}

// qqOfficialMediaSegments 把附件拆成消息段，紧跟正文段之后。官方事件的富媒体全在
// attachments 里、content 只剩文字；不拆成消息段，这些媒体不会进入后续链路。
//
// 图片段走现有的识图与图片描述流程；视频段走抽帧；语音段带上平台自带的
// asr_refer_text 转写（有转写时不必再跑本地语音识别）；其余（裸值 file，md/zip/mp3
// 这类）落成文件段，文件名和大小留给文件解析插件。
//
// 自家表情也走图片段这条路：content 是空名的 <faceType=…> 标签（正文只剩 [表情]
// 占位），图案本体就是附件图，所以入站不会被当成纯文字消息。
func qqOfficialMediaSegments(attachments []qqOfficialAttachment) []MessageSegment {
	var segments []MessageSegment
	for _, attachment := range attachments {
		mediaURL := qqOfficialAttachmentURL(attachment.URL)
		if mediaURL == "" {
			continue
		}
		switch qqOfficialAttachmentKind(attachment) {
		case "image":
			segments = append(segments, MessageSegment{Type: "image", Data: map[string]string{"url": mediaURL, "file": mediaURL}})
		case "video":
			segments = append(segments, MessageSegment{Type: "video", Data: map[string]string{"url": mediaURL, "file": attachment.Filename}})
		case "record":
			data := map[string]string{"url": mediaURL, "file": attachment.Filename}
			if transcript := strings.TrimSpace(attachment.AsrReferText); transcript != "" {
				data[voiceSTTTranscriptKey] = transcript
			}
			segments = append(segments, MessageSegment{Type: "record", Data: data})
		default:
			data := map[string]string{"url": mediaURL, "file": mediaURL, "name": attachment.Filename}
			if attachment.Size > 0 {
				data["size"] = strconv.FormatInt(attachment.Size, 10)
			}
			segments = append(segments, MessageSegment{Type: "file", Data: data})
		}
	}
	return segments
}

// qqOfficialAttachmentKind 把附件归到统一消息段的那一类。
//
// 官方文档写的是 MIME，实际语音是裸值 voice、文件是裸值 file；视频是标准
// video/mp4。图片仍按 image 前缀放行（此前只处理这一类，行为保持不变）。
func qqOfficialAttachmentKind(attachment qqOfficialAttachment) string {
	contentType := strings.ToLower(strings.TrimSpace(attachment.ContentType))
	switch {
	case strings.HasPrefix(contentType, "image"):
		return "image"
	case strings.HasPrefix(contentType, "video"):
		return "video"
	case contentType == "voice" || strings.HasPrefix(contentType, "audio"):
		return "record"
	default:
		return "file"
	}
}

// qqOfficialAttachmentURL 规范化附件地址：防守一下。实测都是 https
// （multimedia.nt.qq.com.cn），拿到的地址缺协议时补上 https，那种地址下载器不认。
func qqOfficialAttachmentURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "://") {
		value = "https://" + strings.TrimPrefix(value, "//")
	}
	return value
}

var (
	// qqMergedForwardHeaderPattern 匹配合并聊天记录的首行，两种形态：发起人
	// 昵称（如 [张三的聊天记录]）和 [群聊的聊天记录]。
	qqMergedForwardHeaderPattern = regexp.MustCompile(`^\[.+的聊天记录\]$`)
	// qqMergedForwardSeparatorPattern 匹配条目分隔行 === 消息 N ===，序号从 1 递增。
	qqMergedForwardSeparatorPattern = regexp.MustCompile(`^=== 消息 \d+ ===$`)
	// qqMergedForwardAttachmentPattern 匹配附件行前缀 [附件N]；序号恒为 1，
	// 一个条目最多一个附件。
	qqMergedForwardAttachmentPattern = regexp.MustCompile(`^\[附件\d+\]\s*`)
	// qqMergedForwardFieldPattern 匹配附件行里的字段标记。值里的冒号（URL 的
	// https:// 等）不会被误认：标记要求完整的字段名打头。字段分隔符只有半角
	// 冒号。
	qqMergedForwardFieldPattern = regexp.MustCompile(`类型:|文件名:|尺寸:|大小:|URL:`)
)

// qqMergedForwardAttachment 是合并聊天记录附件行拆出来的字段，只留解析要用的。
type qqMergedForwardAttachment struct {
	kind string
	name string
	url  string
	// urlStart 是 URL 字段在原行里的起始下标，用来把 URL 从正文里抹掉；-1 表示没有。
	urlStart int
}

// qqOfficialMergedForwardMedia 拆出合并聊天记录 content 里的媒体段，并把正文里的
// 一次性下载地址抹掉。
//
// 官方对合并转发不给 msg_elements / attachments，全部内容压成一个 content 字符串，
// 媒体只以「[附件1] 类型:图片 文件名:… 尺寸:… 大小:… URL:…」的文本行出现。不拆的
// 话这些媒体进不了识图 / 抽帧链路（同一张图单独发能被识别，放进合并记录则不进入），
// 而且正文的一半以上是带 rkey 的一次性地址——接话评分按尾部 180 字截断，截到的
// 整段都是 URL。
//
// 带 URL 的附件行换成消息段（图片 / 动图→image，视频→video，语音→record）并从
// 正文里抹掉 URL，行内留下类型 / 文件名 / 尺寸 / 大小等字段。文件类附件不带 URL
// （只有文件名和大小），拆不出段就整行留在正文里；未知类型同样不拆。
// 首行 [某某的聊天记录] 与 === 消息 N === 分隔行同时出现才认定是合并转发，避免把
// 恰好引用了这个格式的普通长文误拆。
func qqOfficialMergedForwardMedia(text string) (string, []MessageSegment) {
	if !qqOfficialLooksLikeMergedForward(text) {
		return text, nil
	}
	lines := strings.Split(text, "\n")
	var segments []MessageSegment
	for i, line := range lines {
		attachment, ok := qqMergedForwardAttachmentFields(line)
		if !ok {
			continue
		}
		segment, cleaned, handled := qqMergedForwardAttachmentSegment(attachment, line)
		if !handled {
			continue
		}
		lines[i] = cleaned
		segments = append(segments, segment)
	}
	return strings.Join(lines, "\n"), segments
}

// qqOfficialLooksLikeMergedForward 按首行标题与条目分隔行判断 content 是不是合并聊天记录。
func qqOfficialLooksLikeMergedForward(text string) bool {
	firstLine := text
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		firstLine = text[:idx]
	}
	if !qqMergedForwardHeaderPattern.MatchString(strings.TrimRight(firstLine, "\r")) {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		if qqMergedForwardSeparatorPattern.MatchString(strings.TrimRight(line, "\r")) {
			return true
		}
	}
	return false
}

// qqMergedForwardAttachmentFields 解出附件行的字段。字段组合随类型有差（文件行没有
// 尺寸和 URL），统一按字段标记切、不假定顺序；字段序以 类型 → 文件名 打头，
// 这两个不齐就当作普通文本行，避免误拆。
func qqMergedForwardAttachmentFields(line string) (qqMergedForwardAttachment, bool) {
	loc := qqMergedForwardAttachmentPattern.FindStringIndex(line)
	if loc == nil {
		return qqMergedForwardAttachment{}, false
	}
	region := line[loc[1]:]
	matches := qqMergedForwardFieldPattern.FindAllStringIndex(region, -1)
	if len(matches) < 2 ||
		region[matches[0][0]:matches[0][1]] != "类型:" ||
		region[matches[1][0]:matches[1][1]] != "文件名:" {
		return qqMergedForwardAttachment{}, false
	}
	attachment := qqMergedForwardAttachment{urlStart: -1}
	for i, match := range matches {
		key := region[match[0]:match[1]]
		end := len(region)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		value := strings.TrimSpace(region[match[1]:end])
		switch key {
		case "类型:":
			attachment.kind = value
		case "文件名:":
			attachment.name = value
		case "URL:":
			attachment.url = value
			attachment.urlStart = loc[1] + match[0]
		}
	}
	if attachment.kind == "" || attachment.name == "" {
		return qqMergedForwardAttachment{}, false
	}
	return attachment, true
}

// qqMergedForwardAttachmentSegment 把附件行换算成消息段，返回抹掉 URL 后的行；
// 换不成段时原行原样返回。
func qqMergedForwardAttachmentSegment(attachment qqMergedForwardAttachment, line string) (MessageSegment, string, bool) {
	mediaURL := qqOfficialAttachmentURL(attachment.url)
	if mediaURL == "" {
		return MessageSegment{}, line, false
	}
	switch attachment.kind {
	case "图片", "动图":
		return MessageSegment{Type: "image", Data: map[string]string{"url": mediaURL, "file": mediaURL}},
			qqMergedForwardLineWithoutURL(line, attachment.urlStart), true
	case "视频":
		return MessageSegment{Type: "video", Data: map[string]string{"url": mediaURL, "file": attachment.name}},
			qqMergedForwardLineWithoutURL(line, attachment.urlStart), true
	case "语音":
		return MessageSegment{Type: "record", Data: map[string]string{"url": mediaURL, "file": attachment.name}},
			qqMergedForwardLineWithoutURL(line, attachment.urlStart), true
	case "文件":
		// 文件类附件不带 URL；带 URL 时按附件 file 段同样处理，交给文件解析插件。
		return MessageSegment{Type: "file", Data: map[string]string{"url": mediaURL, "file": mediaURL, "name": attachment.name}},
			qqMergedForwardLineWithoutURL(line, attachment.urlStart), true
	default:
		return MessageSegment{}, line, false
	}
}

// qqMergedForwardLineWithoutURL 抹掉附件行里的 URL 字段（含它前面的空白）。
func qqMergedForwardLineWithoutURL(line string, urlStart int) string {
	if urlStart < 0 || urlStart >= len(line) {
		return line
	}
	return strings.TrimRight(line[:urlStart], " \t")
}

// qqOfficialEventFromDispatch 把网关事件映射成统一事件。
//
// 语义对照：
//   - GROUP_AT_MESSAGE_CREATE 群里 @ 机器人 -> 群聊，group_openid 当群号
//   - GROUP_MESSAGE_CREATE 全量群消息 -> 群聊。开通「接收所有群消息」能力后
//     @ 机器人的消息也走这个事件（不再推 GROUP_AT_MESSAGE_CREATE），是否被
//     点名认 mentions 里的 is_you 标记；普通消息 ToMe=false，交给群触发词和
//     接话策略处理
//   - C2C_MESSAGE_CREATE 单聊 -> 私聊
//   - AT_MESSAGE_CREATE 频道里 @ 机器人 -> 群聊，channel_id 当群号
//
// GROUP_AT_MESSAGE_CREATE 只推「@ 了机器人」的消息，收到即被点名；全量模式下
// 群里所有消息都会到达，ToMe 必须按 mentions 逐条判断，不能一律当被点名。
func qqOfficialEventFromDispatch(eventType string, data json.RawMessage, selfID string) (MessageEvent, bool) {
	var msg qqOfficialMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return MessageEvent{}, false
	}
	text := qqOfficialFaceText(strings.TrimSpace(msg.Content))
	// 入站引用不随 message_reference 下发：引用消息是 message_type=103 +
	// msg_elements + message_scene.ext 的 ref_msg_idx，被引用正文与附件直接随事件
	// 携带。只有频道消息保留 message_reference 兼容。
	quoted := ""
	if msg.MessageReference != nil {
		quoted = msg.MessageReference.MessageID
	}
	botMentioned := false
	if eventType == "GROUP_MESSAGE_CREATE" {
		botMentioned = msg.botMentioned(selfID)
		if botMentioned {
			text = stripQQBotMentionTokens(text, msg.Mentions, selfID)
		}
	}

	// 合并聊天记录不给 attachments，媒体全压在 content 文本里：拆成消息段，
	// 同时抹掉正文里的一次性下载地址。
	text, mergedMedia := qqOfficialMergedForwardMedia(text)
	segments := platformTextSegments(text, quoted)
	segments = append(segments, mergedMedia...)
	segments = append(segments, qqOfficialMediaSegments(msg.Attachments)...)
	event := MessageEvent{
		Time:       platformEventTime(msg.Timestamp),
		SelfID:     selfID,
		MessageID:  msg.ID,
		RawMessage: text,
		Segments:   segments,
		SenderName: firstNonEmpty(msg.Member.Nick, msg.Author.Username),
		ToMe:       true,
	}
	if quoted != "" {
		event.Quoted = &QuotedMessage{MessageID: quoted}
	}
	// 引用消息：被引用的正文与附件在 msg_elements 里，索引键在 ref_msg_idx。
	// TMP_ 前缀是聊天记录的临时键，不属于 REFIDX_ 键空间，取不到发送者，
	// 但正文还在元素里，引用关系照常交付。引用元素不带 author，发送者只能由
	// 调用方拿索引反查后回填。
	if msg.MessageType == qqMessageTypeQuote {
		quotedText, quotedMedia := qqOfficialQuotedPayload(msg.MsgElements)
		refKey := firstNonEmpty(msg.sceneExtValue("ref_msg_idx"), quoted)
		event.Quoted = &QuotedMessage{
			MessageID:  refKey,
			RawMessage: quotedText,
			Segments:   quotedMedia,
		}
	}

	switch eventType {
	case "GROUP_AT_MESSAGE_CREATE":
		event.PlatformScope = "qq_group"
		event.Kind = EventKindGroup
		event.MessageType = "group"
		event.GroupID = msg.GroupOpenID
		event.UserID = firstNonEmpty(msg.Author.MemberOpenID, msg.Author.ID)
	case "GROUP_MESSAGE_CREATE":
		event.PlatformScope = "qq_group"
		event.Kind = EventKindGroup
		event.MessageType = "group"
		event.GroupID = firstNonEmpty(msg.GroupOpenID, msg.GroupID)
		event.UserID = firstNonEmpty(msg.Author.MemberOpenID, msg.Author.ID)
		// 机器人自己的发言也会回推；归到 SelfID 让运行时按自发消息处理。
		if msg.Author.Bot && selfID != "" && msg.Author.ID == selfID {
			event.UserID = selfID
		}
		event.ToMe = botMentioned
	case "C2C_MESSAGE_CREATE":
		event.Kind = EventKindPrivate
		event.MessageType = "private"
		event.UserID = firstNonEmpty(msg.Author.UserOpenID, msg.Author.ID)
	case "AT_MESSAGE_CREATE":
		event.PlatformScope = "qq_guild"
		event.GuildID = msg.GuildID
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
	// 别的机器人（同群的其他 Bot）发的话只当上下文，不当成对我说的：回复它们既没
	// 意义，也回不出去，还可能两个机器人互相接话。
	if msg.Author.Bot && event.Kind == EventKindGroup && event.UserID != selfID {
		event.ToMe = false
	}
	return event, true
}

// qqOfficialQuotedPayload 把引用消息 msg_elements 里的被引用正文与附件拼出来。
//
// 文字直接在元素 content 里，媒体附件与顶层 attachments 完全同构（复用
// qqOfficialMediaSegments 拆段）。图文混发不拆元素：同一个元素同时带正文与附件。
// 元素里拍平的展示文本（如 [图片] 占位）不动，那是平台给的原文。
func qqOfficialQuotedPayload(elements []qqOfficialMsgElement) (string, []MessageSegment) {
	var parts []string
	var segments []MessageSegment
	for _, element := range elements {
		if content := strings.TrimSpace(element.Content); content != "" {
			parts = append(parts, content)
		}
		segments = append(segments, qqOfficialMediaSegments(element.Attachments)...)
	}
	return strings.Join(parts, "\n"), segments
}
