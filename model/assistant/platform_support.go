// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 四个国内平台（QQ 官方、钉钉、飞书、企业微信）都是同一套模式：拿 appid/secret
// 换一个有时效的 access token，之后所有 API 调用带着它走。这里放它们共用的部分，
// 各自的适配器只描述自己的端点和报文格式。

// platformTokenCache 缓存有时效的 access token。
//
// 每次调用都重新换 token 会很快撞上平台的频控（企业微信和飞书都对 token 接口
// 单独限流），而 token 过期后继续用又会让所有消息发送失败。这里按平台返回的
// expires_in 缓存，并提前一段时间刷新，避免踩在过期边界上。
type platformTokenCache struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
	// fetch 返回 token 和它的有效期。
	fetch func(ctx context.Context) (string, time.Duration, error)
}

// tokenRefreshMargin 是提前刷新的余量：token 剩不到这么多时间就重新换。
const tokenRefreshMargin = 2 * time.Minute

func (c *platformTokenCache) Get(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expiresAt) {
		return c.token, nil
	}
	if c.fetch == nil {
		return "", fmt.Errorf("assistant: token fetcher is not configured")
	}
	token, ttl, err := c.fetch(ctx)
	if err != nil {
		return "", err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("assistant: platform returned an empty access token")
	}
	if ttl > tokenRefreshMargin {
		ttl -= tokenRefreshMargin
	}
	c.token = token
	c.expiresAt = time.Now().Add(ttl)
	return token, nil
}

// Invalidate 丢弃缓存的 token。平台返回「token 失效」这类错误时调用，
// 让下一次请求重新换一个，而不是拿着已经作废的 token 一直重试。
func (c *platformTokenCache) Invalidate() {
	c.mu.Lock()
	c.token = ""
	c.expiresAt = time.Time{}
	c.mu.Unlock()
}

// platformHTTPResponseLimit 限制单次响应读取体积，避免异常响应把内存吃满。
const platformHTTPResponseLimit = 8 << 20

// platformJSONRequest 发一个 JSON 请求并把响应体读回来。
//
// 只负责传输：HTTP 层失败返回 error，业务错误码由各平台自己的解析函数判断，
// 因为四家的错误信封字段名各不相同。
func platformJSONRequest(ctx context.Context, client *http.Client, method, endpoint string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, platformHTTPResponseLimit))
	if err != nil {
		return nil, err
	}
	// 4xx/5xx 时响应体通常仍是平台的错误 JSON，一并带回去便于定位。
	if resp.StatusCode >= 400 {
		return raw, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(truncateForError(string(raw))))
	}
	return raw, nil
}

// truncateForError 截断错误信息里的响应体，日志里不需要完整报文。
func truncateForError(value string) string {
	const limit = 512
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// platformOutboundText 把出站消息压成一段纯文本。
//
// 这四个平台都不支持 OneBot 那种富消息段数组，能稳定投递的只有文本；提及标记
// 在这里按各平台的规则还原成可见文字，不能原样留着 [diana-at:ID] 漏给用户。
func platformOutboundText(msg OutgoingMessage) string {
	text, _ := renderDianaMentions(msg.Text, msg.MentionNames)
	return strings.TrimSpace(text)
}

// platformChatTarget 取出这条出站消息的会话标识，群聊优先。
func platformChatTarget(msg OutgoingMessage) (id string, isGroup bool) {
	if group := strings.TrimSpace(msg.GroupID); group != "" {
		return group, true
	}
	return strings.TrimSpace(msg.UserID), false
}

// platformEventTime 归一化平台下发的时间戳到秒。
//
// 各家单位不一致：钉钉和企业微信给毫秒，QQ 官方给 RFC3339 字符串，飞书给的是
// 字符串形式的毫秒。统一成秒，事件时间轴才对得上。
func platformEventTime(value any) int64 {
	switch v := value.(type) {
	case int64:
		return normalizeEpoch(v)
	case float64:
		return normalizeEpoch(int64(v))
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			return normalizeEpoch(parsed)
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0
		}
		if parsed, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return normalizeEpoch(parsed)
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05-0700"} {
			if parsed, err := time.Parse(layout, trimmed); err == nil {
				return parsed.Unix()
			}
		}
	}
	return 0
}

// normalizeEpoch 把毫秒时间戳折算成秒；已经是秒的原样返回。
func normalizeEpoch(value int64) int64 {
	if value <= 0 {
		return 0
	}
	// 1e11 秒是公元 5138 年，超过这个量级只可能是毫秒。
	if value > 1e11 {
		return value / 1000
	}
	return value
}

// platformTextSegments 构造只含一段文本的消息段，必要时前置引用段。
func platformTextSegments(text, quotedID string) []MessageSegment {
	segments := make([]MessageSegment, 0, 2)
	if quotedID = strings.TrimSpace(quotedID); quotedID != "" {
		segments = append(segments, MessageSegment{Type: "reply", Data: map[string]string{"id": quotedID}})
	}
	return append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": text}})
}
