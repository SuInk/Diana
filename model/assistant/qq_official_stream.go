// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// QQ 官方机器人的私聊流式消息（/v2/users/{openid}/stream_messages）。
//
// 和 Telegram 草稿不同，这条流收尾后就是正式消息，不会消失：生成中按 replace
// 语义每帧发截至当前的全文，最终回复到了 SendWithResult 时改发结束帧，用定稿
// 文本替换掉预览，不再另发一条。群和频道平台不开放这个接口。
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
	started     bool
	done        bool
	at          time.Time
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
func (c *QQOfficialChannel) SendTextDraft(ctx context.Context, msg OutgoingMessage, _ int64) error {
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
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.done {
		return nil
	}
	_, err := c.sendQQStreamFrame(ctx, replyID, stream, qqStreamContent(text), qqStreamStateGenerating)
	if err != nil && !stream.started {
		c.dropQQStream(replyID, stream)
	}
	return err
}

// finishQQStream 用定稿文本发结束帧。没有进行中的流时 ok 为 false，调用方照常发消息。
func (c *QQOfficialChannel) finishQQStream(ctx context.Context, replyID, target, text string) (id string, ok bool, err error) {
	if replyID == "" {
		return "", false, nil
	}
	stream := c.qqStreamFor(replyID, target, false)
	if stream == nil {
		return "", false, nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if !stream.started || stream.done || stream.openid != target {
		return "", false, nil
	}
	stream.done = true
	defer c.dropQQStream(replyID, stream)
	id, err = c.sendQQStreamFrame(ctx, replyID, stream, qqStreamContent(text), qqStreamStateDone)
	return firstNonEmpty(id, stream.streamMsgID), true, err
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
			if strings.Contains(err.Error(), "http 401") {
				c.tokens.Invalidate()
			}
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
