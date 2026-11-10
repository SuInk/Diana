// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// QQ 官方机器人的私聊流式消息（/v2/users/{openid}/stream_messages）。
//
// 和 Telegram 草稿不同，这条流收尾后就是正式消息，不会消失：生成中按 replace
// 语义每帧发截至当前的全文，最终回复到了 SendWithResult 时改发结束帧定稿，不再
// 另发一条。群和频道平台不开放这个接口。
//
// 一轮回复分成几条时，每条各开一个流（上层按 draftID 区分）：下一条的首帧到了，
// 上一条就发结束帧定住，记进 settledStreams 等定稿来认领。已经显示出去的字只会
// 往后长，不会缩回去再另发。
const (
	qqStreamStateGenerating = 1
	qqStreamStateDone       = 10
	// qqStreamMaxRetries 是限流（429 / 50002）时的重试次数，退避从 1 秒起翻倍。
	qqStreamMaxRetries = 3
	// 流在平台那边会超时自动结束；超过这个时长的本地状态不再接续。
	qqStreamTTL = 10 * time.Minute
)

var qqStreamRetryBase = time.Second

type qqStream struct {
	// mu 让同一条流的帧按顺序发出：index 必须严格递增，stream_msg_id 要等首帧返回。
	mu          sync.Mutex
	openid      string
	msgSeq      int
	index       int
	streamMsgID string
	// content 是最近一帧成功发出的全文，收不了尾时拿它发结束帧。
	content string
	started bool
	done    bool
	at      time.Time
	// draftID 区分同一轮回复里的第几条，换了就说明上一条写完了。
	draftID int64
}

func (c *QQOfficialChannel) qqStreamFor(replyID, openid string, create bool) *qqStream {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	now := time.Now()
	for id, stream := range c.streams {
		if now.Sub(stream.at) > qqStreamTTL {
			delete(c.streams, id)
		}
	}
	for id, settled := range c.settledStreams {
		if len(settled) == 0 || now.Sub(settled[len(settled)-1].at) > qqStreamTTL {
			delete(c.settledStreams, id)
		}
	}
	stream := c.streams[replyID]
	if stream == nil && create {
		if c.streams == nil {
			c.streams = map[string]*qqStream{}
		}
		stream = &qqStream{openid: openid, at: now}
		c.streams[replyID] = stream
	}
	return stream
}

func (c *QQOfficialChannel) dropQQStream(replyID string, stream *qqStream) {
	c.streamMu.Lock()
	if c.streams[replyID] == stream {
		delete(c.streams, replyID)
	}
	c.streamMu.Unlock()
}

// SendTextDraft 把生成中的正文推成私聊流式消息的一帧。群和频道直接返回错误，
// 上层草稿据此停掉，最终回复照常发送。
func (c *QQOfficialChannel) SendTextDraft(ctx context.Context, msg OutgoingMessage, draftID int64) error {
	target, isGroup := platformChatTarget(msg)
	replyID := strings.TrimSpace(msg.PassiveReplyMessageID)
	if isGroup || target == "" || replyID == "" {
		return fmt.Errorf("qq: 流式消息只支持私聊被动回复")
	}
	text := qqOfficialPlainText(msg)
	if text == "" {
		return nil
	}
	stream := c.qqStreamFor(replyID, target, true)
	if stream.draftID != 0 && stream.draftID != draftID {
		// 这一轮的下一条开始了：上一条定住，另开一个流。
		if err := c.settleQQStreamSegment(ctx, replyID, stream); err != nil {
			return err
		}
		stream = c.qqStreamFor(replyID, target, true)
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.done {
		return nil
	}
	stream.draftID = draftID
	_, err := c.sendQQStreamFrame(ctx, replyID, stream, qqStreamContent(text), qqStreamStateGenerating)
	if err != nil && !stream.started {
		c.dropQQStream(replyID, stream)
	}
	return err
}

// settleQQStreamSegment 在生成中途把写完的一条定住：用它最后一帧的全文发结束帧，
// 记下来等定稿认领。没发出过帧的流直接丢掉。
func (c *QQOfficialChannel) settleQQStreamSegment(ctx context.Context, replyID string, stream *qqStream) error {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	c.dropQQStream(replyID, stream)
	if !stream.started || stream.done {
		return nil
	}
	stream.done = true
	if _, err := c.sendQQStreamFrame(ctx, replyID, stream, stream.content, qqStreamStateDone); err != nil {
		c.recallQQStream(ctx, stream)
		return err
	}
	c.streamMu.Lock()
	if c.settledStreams == nil {
		c.settledStreams = map[string][]*qqStream{}
	}
	c.settledStreams[replyID] = append(c.settledStreams[replyID], stream)
	c.streamMu.Unlock()
	return nil
}

// takeSettledQQStreams 取走这条入站消息下还没被定稿认领的已定住的流。
func (c *QQOfficialChannel) takeSettledQQStreams(replyID string, all bool) []*qqStream {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	settled := c.settledStreams[replyID]
	if len(settled) == 0 {
		return nil
	}
	if all || len(settled) == 1 {
		delete(c.settledStreams, replyID)
		return settled
	}
	c.settledStreams[replyID] = settled[1:]
	return settled[:1]
}

// settleQQStream 让定稿的一条文字落在这一轮的流式消息上。streamed 为 false 时
// 调用方照常发消息：要么这一轮没流过，要么预览和定稿对不上、已经撤回。
func (c *QQOfficialChannel) settleQQStream(ctx context.Context, replyID, target, text string) (id string, streamed bool) {
	if replyID == "" {
		return "", false
	}
	if settled := c.takeSettledQQStreams(replyID, false); len(settled) > 0 {
		// 中途定住的那条改不了，定稿和它一样才算已经发过。
		if settled[0].openid == target && settled[0].content == qqStreamContent(text) {
			return settled[0].streamMsgID, true
		}
		log.Printf("qq: 流式预览和定稿对不上，撤回预览改为普通发送")
		for _, stream := range append(settled, c.takeSettledQQStreams(replyID, true)...) {
			c.recallQQStream(ctx, stream)
		}
		if err := c.AbandonTextDraft(ctx, OutgoingMessage{UserID: target, PassiveReplyMessageID: replyID}); err != nil {
			log.Printf("qq: 撤回流式预览失败: %v", err)
		}
		return "", false
	}
	stream := c.qqStreamFor(replyID, target, false)
	if stream == nil {
		return "", false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if !stream.started || stream.done || stream.openid != target {
		return "", false
	}
	stream.done = true
	defer c.dropQQStream(replyID, stream)
	id, err := c.sendQQStreamFrame(ctx, replyID, stream, qqStreamContent(text), qqStreamStateDone)
	if err == nil {
		return firstNonEmpty(id, stream.streamMsgID), true
	}
	// 结束帧被拒（平台不许改已经显示的内容）：把停在半截的预览关掉撤回，再普通发送。
	log.Printf("qq: 流式消息收尾失败，撤回预览改为普通发送: %v", err)
	if _, err := c.sendQQStreamFrame(ctx, replyID, stream, stream.content, qqStreamStateDone); err != nil {
		log.Printf("qq: 关闭流式预览失败: %v", err)
	}
	c.recallQQStream(ctx, stream)
	return "", false
}

// recallQQStream 撤回一条流式消息。只在发出 2 分钟内有效，失败只记日志。
func (c *QQOfficialChannel) recallQQStream(ctx context.Context, stream *qqStream) error {
	if stream.streamMsgID == "" {
		return fmt.Errorf("qq: 流式消息没有拿到 id，无法撤回")
	}
	if _, err := c.CallAPI(ctx, http.MethodDelete+" /v2/users/"+url.PathEscape(stream.openid)+"/messages/"+url.PathEscape(stream.streamMsgID), nil); err != nil {
		err = fmt.Errorf("qq: 撤回流式消息失败: %w", err)
		log.Print(err)
		return err
	}
	return nil
}

// AbandonTextDraft 收掉这一轮没等到定稿的流：模型静默、出错，或者被连发打断后
// 不再出正文时，已经发出的帧就停在半截（比如只有一个字）挂在聊天里。先发结束帧
// 把流关掉，再按 stream_msg_id 撤回；中途定住却没被定稿认领的前几条一并撤回。
func (c *QQOfficialChannel) AbandonTextDraft(ctx context.Context, msg OutgoingMessage) error {
	target, isGroup := platformChatTarget(msg)
	replyID := strings.TrimSpace(msg.PassiveReplyMessageID)
	if isGroup || target == "" || replyID == "" {
		return nil
	}
	var errs []error
	for _, settled := range c.takeSettledQQStreams(replyID, true) {
		if err := c.recallQQStream(ctx, settled); err != nil {
			errs = append(errs, err)
		}
	}
	stream := c.qqStreamFor(replyID, target, false)
	if stream == nil {
		return errors.Join(errs...)
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	defer c.dropQQStream(replyID, stream)
	if !stream.started || stream.done || stream.openid != target {
		return errors.Join(errs...)
	}
	stream.done = true
	if _, err := c.sendQQStreamFrame(ctx, replyID, stream, stream.content, qqStreamStateDone); err != nil {
		errs = append(errs, err)
	}
	if err := c.recallQQStream(ctx, stream); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// qqStreamContent 流式消息只收 Markdown；大白话按 Markdown 的换行规则补齐，免得
// 单个换行被吞掉。
func qqStreamContent(text string) string {
	if content, markdown := qqOfficialMarkdownContent(text); markdown {
		return content
	}
	return qqMarkdownLineBreaks(text)
}

// sendQQStreamFrame 发一帧；调用方持有 stream.mu。
func (c *QQOfficialChannel) sendQQStreamFrame(ctx context.Context, replyID string, stream *qqStream, content string, state int) (string, error) {
	auth, err := c.authHeader(ctx)
	if err != nil {
		return "", err
	}
	if stream.msgSeq == 0 {
		// 同一条流的所有帧共用一个 msg_seq，只有 index 递增。
		stream.msgSeq = c.nextPassiveSeq(replyID)
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	endpoint := c.apiBase() + "/v2/users/" + stream.openid + "/stream_messages"
	for attempt := 0; ; attempt++ {
		body := map[string]any{
			"input_mode":   "replace",
			"input_state":  state,
			"content_type": "markdown",
			"content_raw":  content,
			"event_id":     replyID,
			"msg_seq":      stream.msgSeq,
			"index":        stream.index,
		}
		if !strings.HasPrefix(replyID, qqInteractionEventPrefix) {
			body["msg_id"] = replyID
		}
		if stream.streamMsgID != "" {
			body["stream_msg_id"] = stream.streamMsgID
		}
		// 重试也要换新的 index，原样重发会被当成过期帧。
		stream.index++
		raw, err := platformJSONRequest(ctx, client, http.MethodPost, endpoint, map[string]string{"Authorization": auth}, body)
		var envelope struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			ID      string `json:"id"`
			ExtInfo *struct {
				RefIdx string `json:"ref_idx"`
			} `json:"ext_info"`
		}
		decoded := json.Unmarshal(raw, &envelope) == nil
		if err == nil && decoded && envelope.Code != 0 {
			err = fmt.Errorf("qq: 流式消息被拒绝: %s (code %d)", envelope.Message, envelope.Code)
		}
		if err != nil {
			c.noteTokenRejected(err)
			if qqStreamRateLimited(err, envelope.Code) && attempt < qqStreamMaxRetries {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(qqStreamRetryBase << attempt):
				}
				continue
			}
			return "", fmt.Errorf("qq: 流式消息发送失败: %w", err)
		}
		stream.started = true
		stream.content = content
		stream.at = time.Now()
		id := strings.TrimSpace(envelope.ID)
		if stream.streamMsgID == "" {
			stream.streamMsgID = id
		}
		if envelope.ExtInfo != nil {
			c.refs.recordOutbound(envelope.ExtInfo.RefIdx, firstNonEmpty(id, stream.streamMsgID), c.Status().SelfID, time.Now())
		}
		return id, nil
	}
}

func qqStreamRateLimited(err error, code int) bool {
	if code == 50002 {
		return true
	}
	message := err.Error()
	return strings.Contains(message, "http 429") || strings.Contains(message, "50002")
}
