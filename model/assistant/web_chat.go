// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// WebUI 的聊天页和语音通话都是「主人和机器人私聊」：登录 WebUI 的人就是主人，说的话
// 按主人在这个平台上发来的私聊走正常回复流程，人设、记忆、历史都是同一份，在 QQ 上
// 和机器人私聊的记录聊天页里也看得到。区别只在出口：回复不发到平台上，而是在发送的
// 最后一步交给页面（webChatSink），输入状态、草稿流、合并转发这些只在平台聊天窗口里
// 有意义的东西都跳过。
const (
	webChatMessagePrefix = "web-"
	webChatHistoryCap    = 200
	// webChatOwnerID 是没填主人的机器人在聊天页里给登录者用的账号，和平台账号撞不上。
	webChatOwnerID = "webui-owner"
)

// WebChatMessage 是聊天页里的一条消息。
type WebChatMessage struct {
	ID   string `json:"id"`
	Role string `json:"role"` // user 或 assistant
	Text string `json:"text"`
	// Images 是能直接在网页里显示的图片地址（http/https）。
	Images []string `json:"images,omitempty"`
	// At 是 Unix 秒。
	At int64 `json:"at"`
}

// WebChatInfo 是打开聊天前给前端看的状态。
type WebChatInfo struct {
	BotID    string `json:"bot_id"`
	BotName  string `json:"bot_name"`
	Platform string `json:"platform"`
	// Problem 非空时聊不了，内容是原因。
	Problem string `json:"problem,omitempty"`
}

type webChatSinkKey struct{}

// webChatSink 收下这一轮要发出去的一条消息。
type webChatSink func(WebChatMessage)

func withWebChatSink(ctx context.Context, sink webChatSink) context.Context {
	return context.WithValue(ctx, webChatSinkKey{}, sink)
}

var webChatOutboundSeq atomic.Int64

// deliverToWebChat 在聊天页或通话里代替平台发送。返回的消息 ID 让发送链路照常记历史。
func deliverToWebChat(ctx context.Context, msg OutgoingMessage) (map[string]any, bool) {
	sink, _ := ctx.Value(webChatSinkKey{}).(webChatSink)
	if sink == nil {
		return nil, false
	}
	id := webChatMessagePrefix + "out-" + strconv.FormatInt(webChatOutboundSeq.Add(1), 10)
	text := msg.Text
	if strings.Contains(text, "[CQ:") {
		text = PlainText(CQToSegments(text))
	}
	out := WebChatMessage{ID: id, Role: "assistant", Text: strings.TrimSpace(text), At: time.Now().Unix()}
	for _, url := range msg.ImageURLs {
		if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
			out.Images = append(out.Images, url)
		}
	}
	if out.Text != "" || len(out.Images) > 0 {
		sink(out)
	}
	return map[string]any{"message_id": id}, true
}

// WebChatInfo 检查这台机器人能不能在聊天页里聊：存在、没停用就行。
func (r *Runtime) WebChatInfo(profileID string) WebChatInfo {
	cfg, _, err := r.ownerChatEvent(profileID)
	info := WebChatInfo{BotID: cfg.ID, BotName: cfg.Name, Platform: cfg.Platform}
	if err != nil {
		info.Problem = err.Error()
	}
	return info
}

// ownerChatEvent 造一条「主人发来的私聊」的骨架，聊天页和通话共用。登录 WebUI 的人
// 就是主人，所以不管主人账号填没填、填成什么样都能聊。
func (r *Runtime) ownerChatEvent(profileID string) (BotConfig, MessageEvent, error) {
	profileID = strings.TrimSpace(profileID)
	r.mu.RLock()
	_, exists := r.profileConfigs[profileID]
	r.mu.RUnlock()
	if profileID != "" && !exists {
		return BotConfig{}, MessageEvent{}, fmt.Errorf("机器人不存在")
	}
	cfg := r.profileConfig(profileID).WithDefaults()
	if cfg.ID == "" {
		return cfg, MessageEvent{}, fmt.Errorf("机器人不存在")
	}
	if r.profileDisabled(cfg.ID) {
		return cfg, MessageEvent{}, fmt.Errorf("这台机器人已停用")
	}
	// 填了主人账号就用它，和平台上的私聊是同一段记录。Telegram 主人填的是 @用户名时，
	// 用主人说过话后记下的数字 ID；还不知道就拿用户名本身当账号，单独一段记录。没填
	// 主人时登录者就是主人（见 OwnerIDForEvent），用 webChatOwnerID。
	owner := strings.TrimSpace(cfg.OwnerID)
	userID, username := owner, ""
	if owner == "" {
		userID = webChatOwnerID
	} else if handle := TelegramOwnerUsername(owner); handle != "" && NormalizePlatformID(cfg.Platform) == PlatformTelegram {
		username = handle
		if id, err := r.ownerDeliveryID(MessageEvent{ProfileID: cfg.ID, Platform: PlatformTelegram}, cfg); err == nil {
			userID = id
		}
	}
	event := MessageEvent{
		ProfileID:      cfg.ID,
		Platform:       NormalizePlatformID(cfg.Platform),
		Kind:           EventKindPrivate,
		MessageType:    string(EventKindPrivate),
		SelfID:         strings.TrimSpace(cfg.BotAccount),
		UserID:         userID,
		SenderUsername: username,
		SenderName:     "主人",
		ToMe:           true,
		webChat:        true,
	}
	return cfg, event, nil
}

// WebChatHistory 读主人和这台机器人最近的私聊，旧的在前。平台上的私聊和聊天页、
// 通话里说的都在里面。
func (r *Runtime) WebChatHistory(ctx context.Context, profileID string, limit int) ([]WebChatMessage, error) {
	cfg, event, err := r.ownerChatEvent(profileID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > webChatHistoryCap {
		limit = webChatHistoryCap
	}
	event = r.bindInboundEventIdentity(event)
	session := sessionKey(event)
	r.mu.RLock()
	memory := append([]MessageEvent(nil), r.history[session]...)
	store := r.messageStore
	r.mu.RUnlock()
	history := memory
	if store != nil {
		stored, err := store.ListRecentMessageEvents(ctx, session, limit)
		if err != nil {
			return nil, err
		}
		history = mergeMessageHistory(memory, stored, limit)
	}
	sort.SliceStable(history, func(i, j int) bool { return history[i].Time < history[j].Time })
	if len(history) > limit {
		history = history[len(history)-limit:]
	}
	bot := firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(event.SelfID))
	out := make([]WebChatMessage, 0, len(history))
	for _, item := range history {
		if item.crossGroupContext || isPokeHistoryEvent(item) || isSubscriptionPushEvent(item) {
			continue
		}
		msg := WebChatMessage{ID: item.MessageID, Role: "user", At: item.Time}
		if assistantHistoryEvent(item, bot) {
			msg.Role = "assistant"
		}
		msg.Text = strings.TrimSpace(historyPlainText(item))
		if msg.Text == "" && strings.TrimSpace(item.botReply) != "" {
			msg.Text = strings.TrimSpace(item.botReply)
		}
		for _, segment := range item.Segments {
			if segment.Type != "image" {
				continue
			}
			url := strings.TrimSpace(segment.Data["url"])
			if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
				msg.Images = append(msg.Images, url)
			} else if msg.Text == "" {
				msg.Text = "[图片]"
			}
		}
		if msg.Text == "" && len(msg.Images) == 0 {
			continue
		}
		out = append(out, msg)
	}
	return out, nil
}

// WebChatSend 把聊天页里打的一句话当成主人发来的私聊，同步走完一轮回复，每条回复交给 emit。
func (r *Runtime) WebChatSend(ctx context.Context, profileID, text string, emit func(WebChatMessage)) (WebChatMessage, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return WebChatMessage{}, errors.New("消息是空的")
	}
	_, event, err := r.ownerChatEvent(profileID)
	if err != nil {
		return WebChatMessage{}, err
	}
	event.MessageID = webChatMessagePrefix + "in-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	user := WebChatMessage{ID: event.MessageID, Role: "user", Text: text, At: time.Now().Unix()}
	return user, r.replyAsOwner(ctx, event, text, emit)
}

// replyAsOwner 把 text 当成 event 描述的主人私聊走一轮回复，回复交给 sink，不发到平台。
func (r *Runtime) replyAsOwner(ctx context.Context, event MessageEvent, text string, sink webChatSink) error {
	event.Time = time.Now().Unix()
	event.RawMessage = text
	event.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}
	event = r.bindInboundEventIdentity(event)
	ctx = withWebChatSink(ctx, sink)
	ctx = withLLMUsageContext(ctx, event)
	ctx = r.withDebugTraceContext(ctx, event)
	ctx = withContextBudgetCap(ctx, r.effectiveConfigForEvent(event).MaxContextTokens)
	r.noteDirectedInbound(event)
	r.noteSenderTurnArrival(event)
	prepared, routedText, handled, outcome := r.prepareMessageEvent(ctx, event)
	if !handled {
		r.finishSenderTurn(event)
		return nil
	}
	select {
	case r.sem <- struct{}{}:
		r.incActive(1)
	case <-ctx.Done():
		r.finishSenderTurn(event)
		return ctx.Err()
	}
	defer func() {
		<-r.sem
		r.incActive(-1)
	}()
	_, err := r.replyAndRecord(ctx, prepared, routedText, outcome)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
