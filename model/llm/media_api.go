// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 语音合成、语音识别和视频生成共用这一层：它们都不是对话接口，SDK 帮不上忙，
// 但错误分类、超时和重试的口径应该和对话一致——同一个网关 503 了，TTS 和
// 对话该给出同一种判断。

// MediaErrorKind 是音视频接口失败的归类。调用方按它决定换不换下一条路由、
// 要不要告诉用户「稍后再试」。
type MediaErrorKind string

const (
	MediaErrorAuth          MediaErrorKind = "auth"
	MediaErrorRateLimit     MediaErrorKind = "rate_limit"
	MediaErrorUpstream      MediaErrorKind = "upstream"
	MediaErrorInvalid       MediaErrorKind = "invalid_request"
	MediaErrorNotFound      MediaErrorKind = "not_found"
	MediaErrorContentPolicy MediaErrorKind = "content_policy"
	MediaErrorTimeout       MediaErrorKind = "timeout"
	MediaErrorNetwork       MediaErrorKind = "network"
	MediaErrorBadResponse   MediaErrorKind = "bad_response"
	// MediaErrorJobFailed 是异步任务本身跑失败了（视频生成），HTTP 都是 200。
	MediaErrorJobFailed MediaErrorKind = "job_failed"
)

// MediaAPIError 是音视频接口的统一错误。
type MediaAPIError struct {
	Op         string
	Kind       MediaErrorKind
	StatusCode int
	Detail     string
	RetryAfter time.Duration
	Err        error
}

func (e *MediaAPIError) Error() string {
	parts := make([]string, 0, 3)
	if e.StatusCode > 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", e.StatusCode))
	}
	if detail := strings.TrimSpace(e.Detail); detail != "" {
		parts = append(parts, detail)
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	return fmt.Sprintf("llm: %s failed (%s): %s", e.Op, e.Kind, strings.Join(parts, ": "))
}

func (e *MediaAPIError) Unwrap() error { return e.Err }

// Retryable 报告同一请求原样再发有没有意义。参数错、鉴权错、内容被拒重发多少次
// 都一样，只会白白多扣几次配额。
func (e *MediaAPIError) Retryable() bool {
	switch e.Kind {
	case MediaErrorRateLimit, MediaErrorUpstream, MediaErrorTimeout, MediaErrorNetwork:
		return true
	}
	return false
}

// MediaErrorKindOf 取出错误的归类，不是 MediaAPIError 时返回空。
func MediaErrorKindOf(err error) MediaErrorKind {
	var apiErr *MediaAPIError
	if errors.As(err, &apiErr) {
		return apiErr.Kind
	}
	return ""
}

// IsRetryableMediaError 报告错误是不是瞬时故障。
func IsRetryableMediaError(err error) bool {
	var apiErr *MediaAPIError
	return errors.As(err, &apiErr) && apiErr.Retryable()
}

func classifyMediaStatus(status int, body string) MediaErrorKind {
	if mediaBodyMentionsPolicy(body) {
		return MediaErrorContentPolicy
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusPaymentRequired:
		return MediaErrorAuth
	case status == http.StatusTooManyRequests:
		return MediaErrorRateLimit
	case status == http.StatusNotFound:
		return MediaErrorNotFound
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return MediaErrorTimeout
	case status >= 500:
		return MediaErrorUpstream
	default:
		return MediaErrorInvalid
	}
}

func mediaBodyMentionsPolicy(body string) bool {
	text := strings.ToLower(body)
	for _, marker := range []string{"content_policy", "content policy", "moderation_blocked", "safety_violation", "content_filter"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func classifyMediaTransportError(ctx context.Context, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		// 调用方自己取消的不是上游故障，原样交回去，别让上层以为该换路由重试。
		if errors.Is(ctxErr, context.Canceled) {
			return ctxErr
		}
		return &MediaAPIError{Op: op, Kind: MediaErrorTimeout, Err: err}
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &MediaAPIError{Op: op, Kind: MediaErrorTimeout, Err: err}
	}
	return &MediaAPIError{Op: op, Kind: MediaErrorNetwork, Err: err}
}

// MediaRetryPolicy 是一次音视频请求的超时与重试口径。
type MediaRetryPolicy struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	// AttemptTimeout 是每次尝试的上限，含读完响应体；零值表示只受调用方 ctx 约束。
	AttemptTimeout time.Duration
}

// DefaultMediaRetryPolicy 与对话的瞬时重试同量级：最多再试两次。音视频请求贵，
// 多试几次省不了用户的等待，只会多扣配额。
var DefaultMediaRetryPolicy = MediaRetryPolicy{MaxRetries: 2, BaseDelay: time.Second, MaxDelay: 20 * time.Second}

func (p MediaRetryPolicy) delay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if p.MaxDelay > 0 && retryAfter > p.MaxDelay {
			return p.MaxDelay
		}
		return retryAfter
	}
	delay := p.BaseDelay << attempt
	if p.MaxDelay > 0 && delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	return delay
}

// mediaHTTP 是一个已经解析好地址和凭据的音视频接口客户端。
type mediaHTTP struct {
	cfg     ProviderConfig
	client  *http.Client
	baseURL string
	// auth 把凭据写进请求。OpenAI 兼容接口用 Bearer，ElevenLabs 用 xi-api-key。
	auth   func(*http.Request)
	policy MediaRetryPolicy
	sleep  func(context.Context, time.Duration) error
}

func newMediaHTTP(cfg ProviderConfig, defaultBaseURL string, opts []ClientOption) (*mediaHTTP, error) {
	cfg = cfg.WithDefaults()
	RegisterProviderSecrets(cfg)
	if strings.TrimSpace(cfg.APIKey) == "" && strings.TrimSpace(cfg.OAuthProvider) == "" {
		return nil, ErrMissingAPIKey
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	for name := range cfg.Headers {
		if !validHeaderName(name) {
			return nil, fmt.Errorf("llm: invalid header name %q", name)
		}
	}
	options := clientOptions{httpClient: http.DefaultClient}
	for _, opt := range opts {
		opt(&options)
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	m := &mediaHTTP{
		cfg:     cfg,
		client:  httpClientWithConfigCredentials(options.httpClient, options.credentials, cfg),
		baseURL: baseURL,
		policy:  DefaultMediaRetryPolicy,
		sleep:   mediaRetrySleep,
	}
	m.auth = func(req *http.Request) {
		if key := strings.TrimSpace(cfg.APIKey); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	return m, nil
}

// mediaRetrySleep 是重试之间的等待，测试里换成不等。
var mediaRetrySleep = sleepContext

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (m *mediaHTTP) url(endpoint string, query url.Values) (string, error) {
	joined, err := joinOpenAICompatibleURL(m.baseURL, endpoint)
	if err != nil {
		return "", err
	}
	if len(query) > 0 {
		joined += "?" + query.Encode()
	}
	return joined, nil
}

// mediaRequestSpec 描述一次请求。body 以字节给出，重试时每次重建 Reader。
type mediaRequestSpec struct {
	op          string
	method      string
	endpoint    string
	query       url.Values
	body        []byte
	contentType string
	accept      string
}

func (m *mediaHTTP) newRequest(ctx context.Context, spec mediaRequestSpec) (*http.Request, error) {
	target, err := m.url(spec.endpoint, spec.query)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if spec.body != nil {
		body = bytes.NewReader(spec.body)
	}
	req, err := http.NewRequestWithContext(ctx, spec.method, target, body)
	if err != nil {
		return nil, err
	}
	m.auth(req)
	if spec.contentType != "" {
		req.Header.Set("Content-Type", spec.contentType)
	}
	if spec.accept != "" {
		req.Header.Set("Accept", spec.accept)
	}
	// 配置档里的自定义头最后写，同名时以部署方配置的为准。
	for name, value := range m.cfg.NormalizedHeaders() {
		req.Header.Set(name, value)
	}
	if userAgent := m.cfg.UserAgentWithDefault(); strings.TrimSpace(userAgent) != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	return req, nil
}

// mediaResult 是读完的响应。
type mediaResult struct {
	status int
	header http.Header
	body   []byte
}

// do 发出请求并读完响应体，瞬时故障按策略重试。maxBytes 限制响应体大小，
// 超限直接报错而不是截断——截断的音频和视频放不出来，比报错更难查。
func (m *mediaHTTP) do(ctx context.Context, spec mediaRequestSpec, maxBytes int64) (*mediaResult, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		result, err := m.attempt(ctx, spec, maxBytes)
		if err == nil {
			return result, nil
		}
		lastErr = err
		var apiErr *MediaAPIError
		if !errors.As(err, &apiErr) || !apiErr.Retryable() || attempt >= m.policy.MaxRetries {
			return nil, lastErr
		}
		if sleepErr := m.sleep(ctx, m.policy.delay(attempt, apiErr.RetryAfter)); sleepErr != nil {
			return nil, lastErr
		}
	}
}

func (m *mediaHTTP) attempt(ctx context.Context, spec mediaRequestSpec, maxBytes int64) (*mediaResult, error) {
	attemptCtx, cancel := ctx, context.CancelFunc(func() {})
	if m.policy.AttemptTimeout > 0 {
		attemptCtx, cancel = context.WithTimeout(ctx, m.policy.AttemptTimeout)
	}
	defer cancel()
	resp, err := m.send(attemptCtx, spec)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, classifyMediaTransportError(attemptCtx, spec.op, err)
	}
	if int64(len(body)) > maxBytes {
		return nil, &MediaAPIError{Op: spec.op, Kind: MediaErrorBadResponse, StatusCode: resp.StatusCode, Detail: fmt.Sprintf("response exceeds %d MiB", maxBytes>>20)}
	}
	return &mediaResult{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// send 发出请求，只在拿到 2xx 响应头时返回响应；非 2xx 转成 MediaAPIError。
// 流式调用直接用它：响应头到了之后就不再重试，已经吐给调用方的音频收不回来。
func (m *mediaHTTP) send(ctx context.Context, spec mediaRequestSpec) (*http.Response, error) {
	req, err := m.newRequest(ctx, spec)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, classifyMediaTransportError(ctx, spec.op, err)
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return resp, nil
	}
	defer resp.Body.Close()
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	detail := strings.TrimSpace(string(errBody))
	if looksLikeCloudflareBlock(detail) {
		detail = "Cloudflare blocked the API request before it reached the upstream service"
	} else if len(detail) > 2000 {
		detail = detail[:2000]
	}
	return nil, &MediaAPIError{
		Op:         spec.op,
		Kind:       classifyMediaStatus(resp.StatusCode, detail),
		StatusCode: resp.StatusCode,
		Detail:     detail,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if wait := time.Until(at); wait > 0 {
			return wait
		}
	}
	return 0
}

// jsonErrorOn200 认出「HTTP 200 但正文是 JSON 错误」的兼容服务：不少自建 TTS 层
// 出错时照样回 200，把 {"error": ...} 当音频存下来只会得到一个放不出来的文件。
func jsonErrorOn200(op string, result *mediaResult) error {
	contentType := strings.ToLower(result.header.Get("Content-Type"))
	if !strings.Contains(contentType, "json") && !bytes.HasPrefix(bytes.TrimSpace(result.body), []byte("{")) {
		return nil
	}
	detail := strings.TrimSpace(string(result.body))
	if len(detail) > 2000 {
		detail = detail[:2000]
	}
	kind := MediaErrorBadResponse
	if mediaBodyMentionsPolicy(detail) {
		kind = MediaErrorContentPolicy
	}
	return &MediaAPIError{Op: op, Kind: kind, StatusCode: result.status, Detail: detail}
}
