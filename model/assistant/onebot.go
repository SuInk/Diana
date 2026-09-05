// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

func (c *OneBotChannel) OutboundBackoffEnabled() bool { return true }

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
	NoticeType  string          `json:"notice_type,omitempty"`
	RequestType string          `json:"request_type,omitempty"`
	Comment     string          `json:"comment,omitempty"`
	Flag        string          `json:"flag,omitempty"`
	MessageID   any             `json:"message_id,omitempty"`
	MessageSeq  any             `json:"message_seq,omitempty"`
	UserID      any             `json:"user_id,omitempty"`
	GroupID     any             `json:"group_id,omitempty"`
	OperatorID  any             `json:"operator_id,omitempty"`
	TargetID    any             `json:"target_id,omitempty"`
	Message     json.RawMessage `json:"message,omitempty"`
	RawMessage  string          `json:"raw_message,omitempty"`
	Sender      struct {
		Nickname string `json:"nickname,omitempty"`
		Card     string `json:"card,omitempty"`
		Role     string `json:"role,omitempty"`
		Level    any    `json:"level,omitempty"`
		Title    string `json:"title,omitempty"`
	} `json:"sender,omitempty"`

	Echo    string `json:"echo,omitempty"`
	Status  any    `json:"status,omitempty"`
	RetCode int    `json:"retcode,omitempty"`
	Data    any    `json:"data,omitempty"`
	Wording string `json:"wording,omitempty"`
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
	_, err := c.SendWithResult(ctx, msg)
	return err
}

// SendWithResult sends a message and preserves the OneBot response message_id.
func (c *OneBotChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if strings.TrimSpace(msg.Text) == "" && len(msg.ImageURLs) == 0 && len(msg.VideoURLs) == 0 {
		return nil, nil
	}
	params := map[string]any{"message": buildOutgoingSegments(msg)}
	action := "send_private_msg"
	if msg.GroupID != "" {
		action = "send_group_msg"
		groupID, err := strconv.ParseInt(msg.GroupID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("diana: invalid group id %q", msg.GroupID)
		}
		params["group_id"] = groupID
	} else {
		userID, err := strconv.ParseInt(msg.UserID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("diana: invalid user id %q", msg.UserID)
		}
		params["user_id"] = userID
	}
	return c.CallAPI(ctx, action, params)
}

// buildOutgoingSegments 将回复消息转换为 OneBot segment 列表。
func buildOutgoingSegments(msg OutgoingMessage) []map[string]any {
	segments := make([]map[string]any, 0, 3+len(msg.ImageURLs)+len(msg.VideoURLs))
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
		segments = append(segments, map[string]any{
			"type": "text",
			"data": map[string]string{"text": " "},
		})
	}
	if len(msg.Segments) > 0 {
		for _, segment := range msg.Segments {
			segmentType := strings.TrimSpace(segment.Type)
			if segmentType == "" || segmentType == "notice" {
				continue
			}
			data := cloneSegmentData(segment.Data)
			if (segmentType == "image" || segmentType == "video") && strings.TrimSpace(data["cached_file"]) != "" {
				data["file"] = data["cached_file"]
			}
			segments = append(segments, map[string]any{"type": segmentType, "data": data})
		}
		return segments
	}
	if msg.ImagesFirst {
		segments = appendOutgoingImageSegments(segments, msg.ImageURLs)
	}
	if msg.Text != "" {
		for _, segment := range TextToOneBotSegments(msg.Text) {
			segments = append(segments, map[string]any{
				"type": segment.Type,
				"data": segment.Data,
			})
		}
	}
	if !msg.ImagesFirst {
		segments = appendOutgoingImageSegments(segments, msg.ImageURLs)
	}
	for _, videoURL := range msg.VideoURLs {
		videoURL = videoFileForOutgoingSegment(videoURL)
		if videoURL == "" {
			continue
		}
		segments = append(segments, map[string]any{
			"type": "video",
			"data": map[string]string{"file": videoURL},
		})
	}
	return segments
}

func buildForwardOutgoingSegments(msg OutgoingMessage) []map[string]any {
	msg.MentionUserID = ""
	segments := buildOutgoingSegments(msg)
	filtered := make([]map[string]any, 0, len(segments))
	for _, segment := range segments {
		if segment["type"] == "at" {
			continue
		}
		filtered = append(filtered, segment)
	}
	return filtered
}

func appendOutgoingImageSegments(segments []map[string]any, imageURLs []string) []map[string]any {
	for _, imageURL := range imageURLs {
		imageURL = imageFileForOutgoingSegment(imageURL)
		if imageURL == "" {
			continue
		}
		segments = append(segments, map[string]any{
			"type": "image",
			"data": map[string]string{"file": imageURL},
		})
	}
	return segments
}

func imageFileForOutgoingSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "data:image/") {
		if _, data, ok := strings.Cut(value, ","); ok && strings.TrimSpace(data) != "" {
			return "base64://" + strings.TrimSpace(data)
		}
		return ""
	}
	return value
}

func videoFileForOutgoingSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return "file://" + value
	}
	return value
}

func buildForwardNodes(chunks []string, senderName string, senderUIN string) []map[string]any {
	nodes := make([]map[string]any, 0, len(chunks))
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		nodes = append(nodes, map[string]any{
			"type": "node",
			"data": map[string]any{
				"name":    senderName,
				"uin":     senderUIN,
				"content": buildForwardOutgoingSegments(OutgoingMessage{Text: chunk}),
			},
		})
	}
	return nodes
}

// CallAPI 发送 OneBot action 并等待 echo 响应。
func (c *OneBotChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()
	if conn == nil {
		return nil, errors.New("diana: onebot websocket is not connected")
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
	if envelope.PostType != "message" && envelope.PostType != "notice" && envelope.PostType != "request" {
		// 其它未知事件当前不进入机器人处理链路。
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
	go func() {
		if err := handler(ctx, event); err != nil {
			c.setStatus(c.Status().Connected, c.Status().SelfID, err.Error())
		}
	}()
	return nil
}

// resolveCall 根据 echo 唤醒等待中的 API 调用。
func (c *OneBotChannel) resolveCall(envelope oneBotEnvelope) {
	// 和反向连接一样，API 响应不能阻塞唯一的 WebSocket 读循环。原子移除 echo 后
	// 非阻塞投递，重复响应或调用方超时都只会丢掉这一帧，不会让后续事件断流。
	value, ok := c.pending.LoadAndDelete(envelope.Echo)
	if !ok {
		return
	}
	resultCh, ok := value.(chan callResult)
	if !ok {
		return
	}
	result := callResult{data: oneBotDataMap(envelope.Data)}
	if !envelopeStatusOK(envelope) {
		// 不同 OneBot 实现错误字段不一致，尽量取 wording/message/body，最后再拼状态码。
		message := envelope.Wording
		if message == "" {
			message = oneBotErrorMessage(envelope)
		}
		if message == "" {
			message = fmt.Sprintf("onebot api failed: status=%s retcode=%d", envelopeStatusText(envelope.Status), envelope.RetCode)
		}
		result = callResult{err: errors.New(message)}
	}
	select {
	case resultCh <- result:
	default:
		// 通道已满说明调用方已经拿到结果或不再等待；丢弃重复/过期响应。
	}
}

func oneBotDataMap(data any) map[string]any {
	switch value := data.(type) {
	case nil:
		return nil
	case map[string]any:
		return value
	case []any:
		return map[string]any{"items": value, "list": value}
	default:
		return map[string]any{"value": value}
	}
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
	if envelope.PostType == "request" {
		return oneBotRequestMessageEvent(envelope)
	}
	if envelope.PostType == "notice" {
		// notice 没有正文，只保留群/用户/子类型供欢迎语等逻辑判断。
		subType := firstNonEmpty(envelope.NoticeType, envelope.SubType)
		// notify 类通知的有效子类型在 sub_type 里（poke/lucky_king/honor），
		// notice_type 只是一层壳；不展开的话戳一戳和抢红包王长得一模一样。
		if subType == "notify" && strings.TrimSpace(envelope.SubType) != "" {
			subType = envelope.SubType
		}
		messageID := firstNonEmpty(stringifyID(envelope.MessageID), stringifyID(envelope.TargetID))
		return MessageEvent{
			Kind:        EventKindNotice,
			SubType:     subType,
			Time:        envelope.Time,
			SelfID:      stringifyID(envelope.SelfID),
			UserID:      stringifyID(envelope.UserID),
			TargetID:    stringifyID(envelope.TargetID),
			OperatorID:  stringifyID(envelope.OperatorID),
			GroupID:     stringifyID(envelope.GroupID),
			MessageID:   messageID,
			MessageType: envelope.MessageType,
			Segments:    noticeSegmentsFromEnvelope(envelope, subType, messageID),
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
		Kind:             kind,
		SubType:          envelope.SubType,
		Time:             envelope.Time,
		SelfID:           selfID,
		UserID:           stringifyID(envelope.UserID),
		GroupID:          stringifyID(envelope.GroupID),
		MessageID:        stringifyID(envelope.MessageID),
		MessageSeq:       stringifyID(envelope.MessageSeq),
		MessageType:      envelope.MessageType,
		RawMessage:       rawMessage,
		Segments:         segments,
		SenderName:       envelope.Sender.Card,
		SenderRole:       string(NormalizeGroupRole(envelope.Sender.Role)),
		SenderLevel:      parseGroupLevel(envelope.Sender.Level),
		SenderLevelLabel: strings.TrimSpace(stringFromAny(envelope.Sender.Level)),
		SenderTitle:      strings.TrimSpace(envelope.Sender.Title),
	}
	if event.SenderName == "" {
		event.SenderName = envelope.Sender.Nickname
	}
	event.ToMe = hasAt(segments, selfID)
	return event
}

func noticeSegmentsFromEnvelope(envelope oneBotEnvelope, subType string, messageID string) []MessageSegment {
	data := map[string]string{}
	add := func(key string, value string) {
		if value = strings.TrimSpace(value); value != "" {
			data[key] = value
		}
	}
	add("notice_type", subType)
	add("sub_type", envelope.SubType)
	add("message_id", messageID)
	add("target_id", stringifyID(envelope.TargetID))
	add("operator_id", stringifyID(envelope.OperatorID))
	if len(data) == 0 {
		return nil
	}
	return []MessageSegment{{Type: "notice", Data: data}}
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

func parseGroupLevel(value any) int {
	text := stringifyID(value)
	if len(text) >= 2 && strings.EqualFold(text[:2], "lv") {
		text = strings.TrimSpace(text[2:])
	}
	level, err := strconv.Atoi(text)
	if err != nil || level < 0 {
		return 0
	}
	return level
}

// AtMentionName 取 at 段里附带的昵称。各家 OneBot 实现字段名不一致，按群名片、
// 昵称的优先级依次取。
func AtMentionName(segment MessageSegment) string {
	return strings.TrimSpace(firstNonEmpty(segment.Data["name"], segment.Data["card"], segment.Data["nickname"]))
}

// AtMentionText 把一次提及渲染成可读文本。只有账号时退回「@号码」——光一串数字
// 看不出提到了谁，但没有昵称可用时也只能如此。
func AtMentionText(qq, name string) string {
	qq = strings.TrimSpace(qq)
	if name = strings.TrimSpace(name); name == "" {
		return "@" + qq
	}
	if qq == "" {
		return "@" + name
	}
	return "@" + name + "（" + qq + "）"
}

// AtMentionNameResolver 给没带昵称的 at 段补一个名字。平台自己在 at 段里给了名字
// 时不会被调用，查不到名字返回空串即可。
type AtMentionNameResolver func(userID string) string

// plainTextOptions 调整两处标记的渲染方式。零值等于 PlainText 的原行为：at 段只有
// 号码时就写号码，引用段写成给模型看的 [diana-reply:ID]。
type plainTextOptions struct {
	// resolveName 只在 at 段自己没带昵称时被调用。
	resolveName AtMentionNameResolver
	// renderReply 换掉引用段的渲染。返回空串表示这一段不写出来。
	renderReply func(messageID string) string
	// renderForward 换掉合并转发段的渲染。留空时保持 PlainText 的原行为——正文里
	// 摊出那串 resid。
	renderForward func(segment MessageSegment) string
}

// PlainText 将 OneBot segment 列表转换为可读纯文本。
func PlainText(segments []MessageSegment) string {
	return plainTextWithOptions(segments, plainTextOptions{})
}

func plainTextWithOptions(segments []MessageSegment, options plainTextOptions) string {
	var builder strings.Builder
	for _, segment := range segments {
		switch segment.Type {
		case "text":
			builder.WriteString(segment.Data["text"])
		case "at":
			if qq := segment.Data["qq"]; qq != "" && qq != "all" {
				name := AtMentionName(segment)
				if name == "" && options.resolveName != nil {
					name = strings.TrimSpace(options.resolveName(qq))
				}
				builder.WriteString(AtMentionText(qq, name))
				builder.WriteString(" ")
			}
		case "image":
			// 图片通过多模态 part 传递，不伪装成模型无法读取的文本。
		case "video":
			builder.WriteString("[视频]")
		case "record":
			if transcript := strings.TrimSpace(segment.Data[voiceSTTTranscriptKey]); transcript != "" {
				builder.WriteString("[语音转写] ")
				builder.WriteString(transcript)
			} else {
				builder.WriteString("[语音]")
			}
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
				if options.renderReply != nil {
					builder.WriteString(options.renderReply(id))
					break
				}
				builder.WriteString(replyMarkerPrefix)
				builder.WriteString(id)
				builder.WriteString("]")
			}
		case "forward":
			if options.renderForward != nil {
				builder.WriteString(options.renderForward(segment))
				break
			}
			if summary := strings.TrimSpace(segment.Data["summary"]); summary != "" {
				builder.WriteString(summary)
			} else if id := firstNonEmpty(segment.Data["id"], segment.Data["resid"], segment.Data["forward_id"]); id != "" {
				builder.WriteString("[合并转发:")
				builder.WriteString(id)
				builder.WriteString("]")
			} else {
				builder.WriteString("[合并转发]")
			}
		case "json", "xml":
			// 分享卡片以前一个字都不写：卡片内容全靠链接解析那条路从原始串里抠 URL
			// 再去抓。链接解析没命中的卡片——小程序、群视频邀请、音乐分享——模型那边
			// 就是一片空白，只知道「有人说了句什么」。这里挖一行标题出来。
			//
			// 摘要里只放标题、描述和来源，不放 jumpUrl：正文里多一个链接会连带影响
			// 链接解析的命中判断，也容易被模型照抄进回复。
			builder.WriteString(cardSegmentLabel(segment))
		}
	}
	return strings.TrimSpace(builder.String())
}

// replyMarkerPrefix 是 Diana 自己的引用标记，与具体平台无关：入站把各平台的
// 回复关系渲染成它交给模型，出站再解析回 OutgoingMessage.ReplyMessageID，由各
// 平台适配器落成自己的形式（OneBot 的 reply 段、Telegram 的 reply_to_message_id）。
// 用带前缀的自有词而不是平台原生说法，既跨平台统一，也不会和正文里的自然表达撞车。
const replyMarkerPrefix = "[diana-reply:"

// extractOutgoingReplyMarker 解析模型写在正文开头的引用标记。
// 出站若不解析回来，标记就会作为纯文本发出去。只认开头、且 ID 形如可选负号加
// 数字的写法，避免把正文里提到的字样当成指令。
func extractOutgoingReplyMarker(text string) (string, string, bool) {
	if !strings.HasPrefix(text, replyMarkerPrefix) {
		return "", text, false
	}
	prefix := replyMarkerPrefix
	end := strings.Index(text, "]")
	if end < 0 {
		return "", text, false
	}
	id := text[len(prefix):end]
	if !validOutgoingReplyMessageID(id) {
		return "", text, false
	}
	return id, strings.TrimLeft(text[end+1:], " \t\r\n"), true
}

// stripReplyMarkers 去掉正文里 Diana 自己渲染的引用标记。标记表达的是回复关系，
// 不是发送者写下的字：拿它做触发称呼匹配，等于任何人回复任何一条消息都会因为
// 标记里的 "diana" 命中称呼，从而被当成在叫机器人。
func stripReplyMarkers(text string) string {
	for _, prefix := range []string{replyMarkerPrefix} {
		offset := 0
		for {
			relative := strings.Index(text[offset:], prefix)
			if relative < 0 {
				break
			}
			start := offset + relative
			relativeEnd := strings.Index(text[start:], "]")
			if relativeEnd < 0 {
				break
			}
			end := start + relativeEnd
			if !validOutgoingReplyMessageID(text[start+len(prefix) : end]) {
				// 不是真的标记，跳过这次命中继续往后找，别漏掉后面的真标记。
				offset = start + len(prefix)
				continue
			}
			text = text[:start] + text[end+1:]
			offset = start
		}
	}
	return text
}

func validOutgoingReplyMessageID(id string) bool {
	if strings.HasPrefix(id, "-") {
		id = id[1:]
	}
	if id == "" || len(id) > 20 {
		return false
	}
	for index := 0; index < len(id); index++ {
		if id[index] < '0' || id[index] > '9' {
			return false
		}
	}
	return true
}

// ImageURLs 提取 OneBot 图片段里可被远端多模态模型读取的图片 URL。
func ImageURLs(segments []MessageSegment) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, segment := range segments {
		if segment.Type != "image" {
			continue
		}
		for _, key := range []string{"cached_file", "sourcePath", "source_path", "filePath", "file_path", "path", "url", "image_url", "src", "file"} {
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

func hasImageSegment(segments []MessageSegment) bool {
	return imageSegmentCount(segments) > 0
}

func imageSegmentCount(segments []MessageSegment) int {
	count := 0
	for _, segment := range segments {
		if segment.Type == "image" {
			count++
		}
	}
	return count
}

// VideoURLs 提取 OneBot 视频段里的远程 URL 或 NapCat 提供的本地绝对路径。
func VideoURLs(segments []MessageSegment) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, segment := range segments {
		if segment.Type != "video" {
			continue
		}
		for _, key := range []string{"url", "video_url", "src", "file", "path"} {
			videoURL := normalizedHTTPURL(segment.Data[key])
			if videoURL == "" {
				videoURL = localVideoPath(segment.Data[key])
			}
			if videoURL == "" {
				continue
			}
			if _, ok := seen[videoURL]; ok {
				break
			}
			seen[videoURL] = struct{}{}
			out = append(out, videoURL)
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
	if localPath := normalizedLocalImagePath(value); localPath != "" {
		return localPath
	}
	return normalizedHTTPURL(value)
}

func normalizedLocalImagePath(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "file://") {
		value = strings.TrimPrefix(value, "file://")
	}
	if value == "" || !filepath.IsAbs(value) {
		return ""
	}
	info, err := os.Stat(value)
	if err != nil || info.IsDir() {
		return ""
	}
	return value
}

func normalizedHTTPURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
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
	// 平台中立的提及标记先翻成 CQ at 码，后面照原路走。模型写的是
	// [diana-at:ID]，OneBot 这边它就是一个 at 段。
	parsed := CQToSegments(dianaMentionsToCQ(text))
	segments := make([]MessageSegment, 0, len(parsed)+2)
	for index, segment := range parsed {
		if segment.Type == "text" {
			segments = appendTextWithMentions(segments, segment.Data["text"])
			continue
		}
		segments = append(segments, segment)
		if segment.Type == "at" && !nextSegmentStartsWithWhitespace(parsed, index+1) {
			segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": " "}})
		}
	}
	if len(segments) == 0 {
		return []MessageSegment{{Type: "text", Data: map[string]string{"text": ""}}}
	}
	return segments
}

func appendTextWithMentions(segments []MessageSegment, text string) []MessageSegment {
	start := 0
	for index := 0; index < len(text); index++ {
		if text[index] != '@' || !mentionPrefixAllowed(text, index) {
			continue
		}
		end := index + 1
		for end < len(text) && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		digits := end - index - 1
		if digits < 5 || digits > 12 || (end < len(text) && mentionIDContinuation(text[end])) {
			continue
		}
		if index > start {
			segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": text[start:index]}})
		}
		segments = append(segments, MessageSegment{Type: "at", Data: map[string]string{"qq": text[index+1 : end]}})
		if end >= len(text) || !chatWhitespaceByte(text[end]) {
			segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": " "}})
		}
		start = end
		index = end - 1
	}
	if start < len(text) {
		segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": text[start:]}})
	} else if start == 0 && text == "" {
		segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": ""}})
	}
	return segments
}

func mentionPrefixAllowed(text string, index int) bool {
	if index == 0 {
		return true
	}
	previous := text[index-1]
	return !((previous >= 'a' && previous <= 'z') ||
		(previous >= 'A' && previous <= 'Z') ||
		(previous >= '0' && previous <= '9') ||
		previous == '_' || previous == '@' || previous == '/')
}

func mentionIDContinuation(value byte) bool {
	return (value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9') || value == '_'
}

func chatWhitespaceByte(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func nextSegmentStartsWithWhitespace(segments []MessageSegment, index int) bool {
	if index >= len(segments) || segments[index].Type != "text" {
		return false
	}
	text := segments[index].Data["text"]
	return text != "" && chatWhitespaceByte(text[0])
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
func stripBotMentions(text string, botAccount string) string {
	text = strings.TrimSpace(text)
	if botAccount == "" {
		return text
	}
	replacements := []string{
		"[CQ:at,qq=" + botAccount + "]",
		"@" + botAccount,
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
