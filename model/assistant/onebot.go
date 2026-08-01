package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type OneBotConfig struct {
	Endpoint    string
	AccessToken string
}

type OneBotChannel struct {
	cfg     OneBotConfig
	dialer  *websocket.Dialer
	connMu  sync.RWMutex
	writeMu sync.Mutex
	conn    *websocket.Conn
	status  ChannelStatus
	pending sync.Map
	closed  chan struct{}
}

type callResult struct {
	data map[string]any
	err  error
}

type oneBotEnvelope struct {
	Time        int64           `json:"time,omitempty"`
	SelfID      any             `json:"self_id,omitempty"`
	PostType    string          `json:"post_type,omitempty"`
	MessageType string          `json:"message_type,omitempty"`
	SubType     string          `json:"sub_type,omitempty"`
	MessageID   any             `json:"message_id,omitempty"`
	UserID      any             `json:"user_id,omitempty"`
	GroupID     any             `json:"group_id,omitempty"`
	Message     json.RawMessage `json:"message,omitempty"`
	RawMessage  string          `json:"raw_message,omitempty"`
	Sender      struct {
		Nickname string `json:"nickname,omitempty"`
		Card     string `json:"card,omitempty"`
	} `json:"sender,omitempty"`

	Echo    string         `json:"echo,omitempty"`
	Status  any            `json:"status,omitempty"`
	RetCode int            `json:"retcode,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	Wording string         `json:"wording,omitempty"`
}

// NewOneBotChannel 创建正向 OneBot WebSocket channel。
func NewOneBotChannel(cfg OneBotConfig) *OneBotChannel {
	return &OneBotChannel{
		cfg:    cfg,
		dialer: websocket.DefaultDialer,
		status: ChannelStatus{
			Endpoint:  cfg.Endpoint,
			UpdatedAt: time.Now(),
		},
		closed: make(chan struct{}),
	}
}

// Connect 主动连接 OneBot WebSocket 并读取事件。
func (c *OneBotChannel) Connect(ctx context.Context, handler EventHandler) error {
	if strings.TrimSpace(c.cfg.Endpoint) == "" {
		return ErrMissingOneBotEndpoint
	}

	header := http.Header{}
	if c.cfg.AccessToken != "" {
		// 正向 WebSocket 连接时 token 放在 Authorization 里，兼容 go-cqhttp/NapCat 常见配置。
		header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	}

	conn, _, err := c.dialer.DialContext(ctx, c.cfg.Endpoint, header)
	if err != nil {
		c.setStatus(false, "", err.Error())
		return err
	}

	c.connMu.Lock()
	if c.conn != nil {
		// 重新连接成功后关闭旧连接，避免两个 read loop 同时消费事件。
		_ = c.conn.Close()
	}
	c.conn = conn
	c.connMu.Unlock()
	c.setStatus(true, "", "")

	for {
		select {
		case <-ctx.Done():
			_ = c.Close()
			return ctx.Err()
		case <-c.closed:
			return nil
		default:
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			c.setStatus(false, c.Status().SelfID, err.Error())
			return err
		}
		if err := c.handleFrame(ctx, handler, data); err != nil {
			c.setStatus(c.Status().Connected, c.Status().SelfID, err.Error())
		}
	}
}

// Send 通过 OneBot API 发送私聊或群聊消息。
func (c *OneBotChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	if strings.TrimSpace(msg.Text) == "" {
		return nil
	}
	params := map[string]any{"message": buildOutgoingSegments(msg)}
	action := "send_private_msg"
	if msg.GroupID != "" {
		action = "send_group_msg"
		groupID, err := strconv.ParseInt(msg.GroupID, 10, 64)
		if err != nil {
			return fmt.Errorf("qqbot: invalid group id %q", msg.GroupID)
		}
		params["group_id"] = groupID
	} else {
		userID, err := strconv.ParseInt(msg.UserID, 10, 64)
		if err != nil {
			return fmt.Errorf("qqbot: invalid user id %q", msg.UserID)
		}
		params["user_id"] = userID
	}
	_, err := c.CallAPI(ctx, action, params)
	return err
}

// buildOutgoingSegments 将回复消息转换为 OneBot segment 列表。
func buildOutgoingSegments(msg OutgoingMessage) []map[string]any {
	segments := make([]map[string]any, 0, 3)
	if msg.ReplyMessageID != "" {
		// 群聊回复先带 reply，再 at 原发送者，NapCat 会按 OneBot segment 顺序发送。
		segments = append(segments, map[string]any{
			"type": "reply",
			"data": map[string]string{"id": msg.ReplyMessageID},
		})
	}
	if msg.MentionUserID != "" {
		segments = append(segments, map[string]any{
			"type": "at",
			"data": map[string]string{"qq": msg.MentionUserID},
		})
	}
	for _, segment := range TextToOneBotSegments(msg.Text) {
		segments = append(segments, map[string]any{
			"type": segment.Type,
			"data": segment.Data,
		})
	}
	return segments
}

// CallAPI 发送 OneBot action 并等待 echo 响应。
func (c *OneBotChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()
	if conn == nil {
		return nil, errors.New("qqbot: onebot websocket is not connected")
	}

	echo := strconv.FormatInt(time.Now().UnixNano(), 36)
	resultCh := make(chan callResult, 1)
	// OneBot API 调用通过 echo 关联异步返回；pending map 等待 read loop 解析响应。
	c.pending.Store(echo, resultCh)
	defer c.pending.Delete(echo)

	req := map[string]any{
		"action": action,
		"params": params,
		"echo":   echo,
	}
	c.writeMu.Lock()
	err := conn.WriteJSON(req)
	c.writeMu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		return result.data, result.err
	}
}

// Status 返回 OneBot channel 当前连接状态。
func (c *OneBotChannel) Status() ChannelStatus {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.status
}

// Close 关闭 OneBot WebSocket 连接。
func (c *OneBotChannel) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}

	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	c.status.Connected = false
	c.status.UpdatedAt = time.Now()
	return err
}

// handleFrame 解析单帧 OneBot 数据并分发响应或事件。
func (c *OneBotChannel) handleFrame(ctx context.Context, handler EventHandler, data []byte) error {
	var envelope oneBotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.Echo != "" {
		// 带 echo 的帧是 API 调用响应，不再当作消息事件处理。
		c.resolveCall(envelope)
		return nil
	}
	if envelope.PostType == "meta_event" {
		if selfID := stringifyID(envelope.SelfID); selfID != "" {
			c.setStatus(true, selfID, "")
		}
		return nil
	}
	if envelope.PostType != "message" && envelope.PostType != "notice" {
		// 其它 meta/request 类事件当前不需要进入机器人回复链路。
		return nil
	}

	event := messageEventFromEnvelope(envelope)
	if event.Kind == "" {
		return nil
	}
	if event.SelfID != "" {
		c.setStatus(true, event.SelfID, "")
	}
	if handler == nil {
		return nil
	}
	return handler(ctx, event)
}

// resolveCall 根据 echo 唤醒等待中的 API 调用。
func (c *OneBotChannel) resolveCall(envelope oneBotEnvelope) {
	value, ok := c.pending.Load(envelope.Echo)
	if !ok {
		return
	}
	resultCh, ok := value.(chan callResult)
	if !ok {
		return
	}
	if envelopeStatusOK(envelope) {
		resultCh <- callResult{data: envelope.Data}
		return
	}
	// 不同 OneBot 实现错误字段不一致，尽量取 wording/message/body，最后再拼状态码。
	message := envelope.Wording
	if message == "" {
		message = oneBotErrorMessage(envelope)
	}
	if message == "" {
		message = fmt.Sprintf("onebot api failed: status=%s retcode=%d", envelopeStatusText(envelope.Status), envelope.RetCode)
	}
	resultCh <- callResult{err: errors.New(message)}
}

// oneBotErrorMessage 从 OneBot 响应中提取错误信息。
func oneBotErrorMessage(envelope oneBotEnvelope) string {
	message := envelope.Wording
	if message != "" {
		return message
	}
	var messageText string
	if err := json.Unmarshal(envelope.Message, &messageText); err == nil {
		return messageText
	}
	if len(envelope.Message) > 0 {
		return string(envelope.Message)
	}
	return fmt.Sprintf("onebot api failed: status=%s retcode=%d", envelopeStatusText(envelope.Status), envelope.RetCode)
}

// envelopeStatusOK 判断 OneBot API 响应是否成功。
func envelopeStatusOK(envelope oneBotEnvelope) bool {
	if envelope.RetCode == 0 {
		return true
	}
	// NapCat 某些响应会给 status=ok 但 retcode 不稳定，这里兼容 status 字符串。
	status, ok := envelope.Status.(string)
	return ok && strings.EqualFold(status, "ok")
}

// envelopeStatusText 将 OneBot status 字段转换为文本。
func envelopeStatusText(status any) string {
	switch value := status.(type) {
	case nil:
		return ""
	case string:
		return value
	default:
		data, err := json.Marshal(value)
		if err == nil {
			return string(data)
		}
		return fmt.Sprint(value)
	}
}

// setStatus 更新 OneBot channel 状态快照。
func (c *OneBotChannel) setStatus(connected bool, selfID string, lastError string) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.status = ChannelStatus{
		Connected: connected,
		Endpoint:  c.cfg.Endpoint,
		SelfID:    selfID,
		LastError: lastError,
		UpdatedAt: time.Now(),
	}
}

// messageEventFromEnvelope 将 OneBot 原始 envelope 转换为内部事件。
func messageEventFromEnvelope(envelope oneBotEnvelope) MessageEvent {
	if envelope.PostType == "notice" {
		// notice 没有正文，只保留群/用户/子类型供欢迎语等逻辑判断。
		return MessageEvent{
			Kind:        EventKindNotice,
			SubType:     envelope.SubType,
			Time:        envelope.Time,
			SelfID:      stringifyID(envelope.SelfID),
			UserID:      stringifyID(envelope.UserID),
			GroupID:     stringifyID(envelope.GroupID),
			MessageType: envelope.MessageType,
		}
	}
	kind := EventKindPrivate
	if envelope.MessageType == "group" {
		kind = EventKindGroup
	}
	if envelope.MessageType != "private" && envelope.MessageType != "group" {
		return MessageEvent{}
	}

	segments := parseOneBotMessage(envelope.Message, envelope.RawMessage)
	rawMessage := envelope.RawMessage
	if rawMessage == "" {
		// 有些实现只给 message segment，没有 raw_message，需要反向拼成人可读文本。
		rawMessage = PlainText(segments)
	}
	selfID := stringifyID(envelope.SelfID)
	event := MessageEvent{
		Kind:        kind,
		SubType:     envelope.SubType,
		Time:        envelope.Time,
		SelfID:      selfID,
		UserID:      stringifyID(envelope.UserID),
		GroupID:     stringifyID(envelope.GroupID),
		MessageID:   stringifyID(envelope.MessageID),
		MessageType: envelope.MessageType,
		RawMessage:  rawMessage,
		Segments:    segments,
		SenderName:  envelope.Sender.Card,
	}
	if event.SenderName == "" {
		event.SenderName = envelope.Sender.Nickname
	}
	event.ToMe = hasAt(segments, selfID)
	return event
}

// parseOneBotMessage 解析 OneBot message 字段为 segment 列表。
func parseOneBotMessage(raw json.RawMessage, fallback string) []MessageSegment {
	if len(raw) > 0 && string(raw) != "null" {
		var segments []MessageSegment
		if err := json.Unmarshal(raw, &segments); err == nil {
			return segments
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			// message 字段可能是 CQ 字符串，也可能是 segment 数组，两种格式都支持。
			return CQToSegments(text)
		}
	}
	return CQToSegments(fallback)
}

// stringifyID 将 OneBot 里可能为数字或字符串的 ID 统一成字符串。
func stringifyID(value any) string {
	// OneBot ID 在不同实现里可能是 number/string/json.Number，统一转成字符串避免精度/比较问题。
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case json.Number:
		return v.String()
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// PlainText 将 OneBot segment 列表转换为可读纯文本。
func PlainText(segments []MessageSegment) string {
	var builder strings.Builder
	for _, segment := range segments {
		switch segment.Type {
		case "text":
			builder.WriteString(segment.Data["text"])
		case "at":
			if qq := segment.Data["qq"]; qq != "" && qq != "all" {
				builder.WriteString("@")
				builder.WriteString(qq)
				builder.WriteString(" ")
			}
		case "image":
			builder.WriteString("[图片]")
		case "video":
			builder.WriteString("[视频]")
		case "file":
			// 文件段只放摘要文本，真正文件读取交给文件解析插件处理。
			name := firstNonEmpty(segment.Data["name"], segment.Data["file"], segment.Data["filename"])
			if name == "" {
				name = "文件"
			}
			builder.WriteString("[文件:")
			builder.WriteString(name)
			builder.WriteString("]")
		case "reply":
			if id := segment.Data["id"]; id != "" {
				builder.WriteString("[回复:")
				builder.WriteString(id)
				builder.WriteString("]")
			}
		}
	}
	return strings.TrimSpace(builder.String())
}

// ImageURLs 提取 OneBot 图片段里可被远端多模态模型读取的图片 URL。
func ImageURLs(segments []MessageSegment) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, segment := range segments {
		if segment.Type != "image" {
			continue
		}
		for _, key := range []string{"url", "image_url", "src", "file"} {
			imageURL := normalizedImageURL(segment.Data[key])
			if imageURL == "" {
				continue
			}
			if _, ok := seen[imageURL]; ok {
				break
			}
			seen[imageURL] = struct{}{}
			out = append(out, imageURL)
			break
		}
	}
	return out
}

func normalizedImageURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "base64://") {
		data := strings.TrimSpace(strings.TrimPrefix(value, "base64://"))
		if data == "" {
			return ""
		}
		return "data:image/jpeg;base64," + data
	}
	if strings.HasPrefix(value, "data:image/") {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	if parsed.Host == "" {
		return ""
	}
	return value
}

// TextToOneBotSegments 将文本转换为 OneBot segment 列表。
func TextToOneBotSegments(text string) []MessageSegment {
	return CQToSegments(text)
}

// CQToSegments 将 CQ 码文本解析为 OneBot segment 列表。
func CQToSegments(text string) []MessageSegment {
	var segments []MessageSegment
	for len(text) > 0 {
		idx := strings.Index(text, "[CQ:")
		if idx < 0 {
			if text != "" {
				segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": text}})
			}
			break
		}
		if idx > 0 {
			segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": text[:idx]}})
		}
		end := strings.Index(text[idx:], "]")
		if end < 0 {
			// 不完整 CQ 码按普通文本保留，避免吞掉用户输入。
			segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": text[idx:]}})
			break
		}
		code := text[idx+4 : idx+end]
		segments = append(segments, parseCQSegment(code))
		text = text[idx+end+1:]
	}
	if len(segments) == 0 {
		return []MessageSegment{{Type: "text", Data: map[string]string{"text": ""}}}
	}
	return segments
}

// parseCQSegment 解析单个 CQ 码片段。
func parseCQSegment(code string) MessageSegment {
	parts := strings.Split(code, ",")
	segment := MessageSegment{Type: parts[0], Data: map[string]string{}}
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		segment.Data[key] = unescapeCQ(value)
	}
	return segment
}

// EscapeCQText 转义 CQ 文本里的特殊字符。
func EscapeCQText(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "[", "&#91;")
	text = strings.ReplaceAll(text, "]", "&#93;")
	return text
}

// unescapeCQ 还原 CQ 参数里的转义字符。
func unescapeCQ(text string) string {
	replacer := strings.NewReplacer("&#91;", "[", "&#93;", "]", "&amp;", "&", "&#44;", ",")
	return replacer.Replace(text)
}

// hasAt 判断消息 segment 是否 at 了机器人。
func hasAt(segments []MessageSegment, selfID string) bool {
	if selfID == "" {
		return false
	}
	for _, segment := range segments {
		if segment.Type == "at" && segment.Data["qq"] == selfID {
			return true
		}
	}
	return false
}

// stripBotMentions 从输入文本里移除机器人的 at 标记。
func stripBotMentions(text string, botQQ string) string {
	text = strings.TrimSpace(text)
	if botQQ == "" {
		return text
	}
	replacements := []string{
		"[CQ:at,qq=" + botQQ + "]",
		"@" + botQQ,
	}
	for _, value := range replacements {
		text = strings.ReplaceAll(text, value, "")
	}
	return strings.TrimSpace(text)
}

// oneBotEndpointWithToken 给 OneBot endpoint 补充 access_token 查询参数。
func oneBotEndpointWithToken(endpoint string, token string) string {
	if token == "" {
		return endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	q := parsed.Query()
	if q.Get("access_token") == "" {
		// 反向 WS 的一些部署习惯把 token 放查询参数，这里只在未设置时补上。
		q.Set("access_token", token)
		parsed.RawQuery = q.Encode()
	}
	return parsed.String()
}
