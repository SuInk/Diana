// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// OneBotHTTPChannel calls the implementation's HTTP API and receives signed
// HTTP POST events. Event decoding and status semantics are shared with WS.
type OneBotHTTPChannel struct {
	*OneBotReverseServer
	client     *http.Client
	cancelHTTP context.CancelFunc // protected by mu
}

func NewOneBotHTTPChannel(cfg OneBotConfig) *OneBotHTTPChannel {
	return &OneBotHTTPChannel{
		OneBotReverseServer: NewOneBotReverseServer(cfg),
		// 不在 client 上设统一超时：每次调用按 action 在 ctx 上给期限（见 CallAPI）。
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (c *OneBotHTTPChannel) Connect(ctx context.Context, handler EventHandler) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	if c.cancelHTTP != nil {
		c.cancelHTTP()
	}
	c.connectGeneration++
	generation := c.connectGeneration
	c.ctx, c.handler, c.cancelHTTP = ctx, handler, cancel
	c.mu.Unlock()
	defer func() {
		c.mu.RLock()
		current := c.connectGeneration == generation
		c.mu.RUnlock()
		if current {
			c.setStatus(false, c.Status().SelfID, "")
		}
	}()
	// HTTP has no persistent socket. Probe the API periodically; a successful
	// callback alone must not conceal an unreachable API endpoint.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		probeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		data, err := c.CallAPI(probeCtx, "get_login_info", map[string]any{})
		stop()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			c.setStatus(false, c.Status().SelfID, err.Error())
		} else {
			c.connMu.Lock()
			if !c.status.Connected {
				c.status.ConnectionEpoch++
			}
			c.connMu.Unlock()
			c.setStatus(true, stringifyID(data["user_id"]), "")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *OneBotHTTPChannel) Close() error {
	c.mu.Lock()
	if c.cancelHTTP != nil {
		c.cancelHTTP()
	}
	c.mu.Unlock()
	return c.OneBotReverseServer.Close()
}

func (c *OneBotHTTPChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if action == "" || strings.ContainsAny(action, "/\\?#") || action == "." || action == ".." {
		return nil, errors.New("diana: invalid OneBot HTTP action")
	}
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()
	base, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, errors.New("diana: invalid OneBot HTTP API URL")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + action
	base.RawPath = ""
	if params == nil {
		params = map[string]any{}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	// 和 WebSocket 一样按 action 选期限：带媒体 90 秒，其余 30 秒；调用方自己给了
	// 期限就照它的来。以前 client 上固定 60 秒，媒体不够、纯文本又太长。
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, oneBotCallTimeout(action, params, oneBotTextActionTimeout))
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	}
	// 请求体已经写完才出错（等响应超时、连接被掐），接入端可能已经执行了这个
	// action，和 WebSocket 那边一样标成结果不明，交给调用方确认。
	var wroteRequest atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) { wroteRequest.Store(info.Err == nil) },
	}))
	resp, err := c.client.Do(req)
	if err != nil {
		err = fmt.Errorf("diana: OneBot HTTP request failed: %w", err)
		if wroteRequest.Load() {
			return nil, &outboundOutcomeUnknownError{action: action, cause: err}
		}
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOneBotWebSocketFrameBytes+1))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// NapCat 的 HTTP 服务端参数校验不过回 400，正文里说明是哪个参数不对；
		// 带上正文和状态码，让 isPermanentOutboundRejection 能认出来。
		detail := ""
		var failed oneBotEnvelope
		if err == nil && json.Unmarshal(raw, &failed) == nil {
			detail = oneBotErrorMessage(failed)
		}
		if resp.StatusCode == http.StatusBadRequest && detail != "" {
			return nil, &oneBotActionError{retCode: oneBotHTTPBadRequestRetCode, message: fmt.Sprintf("diana: OneBot HTTP API returned status 400: %s", detail)}
		}
		return nil, fmt.Errorf("diana: OneBot HTTP API returned status %d", resp.StatusCode)
	}
	// 到这里已经收到 2xx：接入端接下并处理了这个 action。正文读不全、解析不了，
	// 只是不知道结果，不是没发出去。
	if err != nil {
		return nil, &outboundOutcomeUnknownError{action: action, cause: fmt.Errorf("diana: read OneBot HTTP response: %w", err)}
	}
	if len(raw) > maxOneBotWebSocketFrameBytes {
		return nil, &outboundOutcomeUnknownError{action: action, cause: errors.New("diana: OneBot HTTP response too large")}
	}
	var envelope oneBotEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, &outboundOutcomeUnknownError{action: action, cause: fmt.Errorf("diana: invalid OneBot HTTP response: %w", err)}
	}
	if envelope.Status == nil {
		return nil, errors.New("diana: OneBot HTTP response is missing status")
	}
	if envelopeStatusText(envelope.Status) == "failed" || !envelopeStatusOK(envelope) {
		failure := &oneBotActionError{retCode: envelope.RetCode, message: oneBotErrorMessage(envelope)}
		return nil, classifyOneBotSendFailure(action, failure)
	}
	return oneBotDataMap(envelope.Data), nil
}

func (c *OneBotHTTPChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	_, err := c.SendWithResult(ctx, msg)
	return err
}

func (c *OneBotHTTPChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	return sendOneBotMessage(ctx, msg, c.CallAPI)
}

func (c *OneBotHTTPChannel) SendChatAction(ctx context.Context, msg OutgoingMessage, action string) error {
	return sendOneBotInputStatus(ctx, msg, action, c.CallAPI)
}

// ConnectionOrigin 返回按 HTTP API 地址推导的 http(s) 源（不含端口）。HTTP 接入
// 的事件上报是裸 POST，不经过反向 ws 握手，内嵌的握手版 ConnectionOrigin 永远为
// 空；这里改用配置地址回推，与正向 ws 同一约定，供媒体回源地址兜底链使用。
// cfg 只在 SetConfig 里整体替换，读侧拿 mu 读锁即可。
func (c *OneBotHTTPChannel) ConnectionOrigin() string {
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()
	return OneBotEndpointOrigin(cfg.Endpoint)
}

func (c *OneBotHTTPChannel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c.mu.RLock()
	secret, ctx, handler := c.cfg.HTTPSecret, c.ctx, c.handler
	c.mu.RUnlock()
	if secret == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxOneBotWebSocketFrameBytes))
	if err != nil {
		http.Error(w, "event body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	mac := hmac.New(sha1.New, []byte(secret))
	_, _ = mac.Write(raw)
	signature := strings.TrimSpace(r.Header.Get("X-Signature"))
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, "sha1="))
	if err != nil || !strings.HasPrefix(signature, "sha1=") || !hmac.Equal(provided, mac.Sum(nil)) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	if ctx == nil || ctx.Err() != nil || handler == nil {
		http.Error(w, "OneBot HTTP channel is stopped", http.StatusServiceUnavailable)
		return
	}
	var envelope oneBotEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.PostType == "" || envelope.Echo != "" {
		http.Error(w, "invalid OneBot event", http.StatusBadRequest)
		return
	}
	if envelope.PostType == "meta_event" {
		c.updateAccountStatus(envelope.Status)
	} else if oneBotDispatchedPostType(envelope.PostType) {
		event := messageEventFromEnvelope(envelope)
		if event.Kind != "" {
			go func() {
				defer recoverGoroutinePanic("onebotHTTP.event")
				if err := handler(ctx, event); err != nil {
					c.setStatus(c.Status().Connected, c.Status().SelfID, err.Error())
				}
			}()
		}
	}
	// Replies use the HTTP API, never a synchronous quick operation: LLM work
	// can outlive the implementation's callback timeout.
	w.WriteHeader(http.StatusNoContent)
}
