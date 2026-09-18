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
	"net/url"
	"strings"
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
		client:              &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("diana: OneBot HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("diana: OneBot HTTP API returned status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOneBotWebSocketFrameBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxOneBotWebSocketFrameBytes {
		return nil, errors.New("diana: OneBot HTTP response too large")
	}
	var envelope oneBotEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("diana: invalid OneBot HTTP response: %w", err)
	}
	if envelope.Status == nil {
		return nil, errors.New("diana: OneBot HTTP response is missing status")
	}
	if envelopeStatusText(envelope.Status) == "failed" || !envelopeStatusOK(envelope) {
		return nil, errors.New(oneBotErrorMessage(envelope))
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
	} else if envelope.PostType == "message" || envelope.PostType == "notice" || envelope.PostType == "request" {
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
