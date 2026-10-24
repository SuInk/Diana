// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

var errContentPolicyRejection = errors.New("llm content policy rejection")

// A committed preview must never be followed by a second model response.
var errLLMOutputCommitted = llm.ErrOutputCommitted

type committedLLMError struct{ cause error }

func (e committedLLMError) Error() string        { return e.cause.Error() }
func (e committedLLMError) Unwrap() error        { return e.cause }
func (e committedLLMError) Is(target error) bool { return target == errLLMOutputCommitted }

type llmFailurePolicy struct {
	retry, failover bool
	cooldown        time.Duration
	// immediate 表示不必攒够连续失败次数就冷却：鉴权失败、额度用完这类状态，
	// 下一次请求照样会撞上。限流、5xx、超时多半是一阵子的，要连续失败才冷却。
	immediate bool
}

func llmErrorPolicy(err error) llmFailurePolicy {
	if err == nil || errors.Is(err, errLLMOutputCommitted) || errors.Is(err, context.Canceled) {
		return llmFailurePolicy{}
	}
	if errors.Is(err, errLLMCandidateTimeout) {
		return llmFailurePolicy{failover: true, cooldown: llmCooldownTimeout}
	}
	if errors.Is(err, errModelRequestTimeout) || errors.Is(err, llm.ErrUnverifiedRejection) || errors.Is(err, llm.ErrDecisionRequired) {
		return llmFailurePolicy{failover: true}
	}
	if errors.Is(err, errContentPolicyRejection) || isContentPolicyRejection(err) || llm.IsContextOverflowError(err) || errors.Is(err, llm.ErrCompletionHasNoText) {
		return llmFailurePolicy{}
	}
	if errors.Is(err, llm.ErrCompletionTruncatedNoText) {
		return llmFailurePolicy{failover: true}
	}
	if errors.Is(err, llm.ErrCompletionEmpty) {
		return llmFailurePolicy{retry: true, failover: true}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return llmFailurePolicy{retry: true, failover: true}
	}
	status := llm.ErrorStatusCode(err)
	text := strings.ToLower(err.Error())
	// 额度用完、账号池全被限住：原地再发只会拿到同一句话，直接换下一个候选。
	// Gateways sometimes report exhausted account pools as 503.
	if status == 402 || (status == 0 || status == 429 || status >= 500) && containsAnyMarker(text, []string{"quota", "resource_exhausted", "resource exhausted", "accounts limited", "account limited", "accounts are limited", "usage limit", "billing", "credit", "余额不足", "额度"}) {
		return llmFailurePolicy{failover: true, cooldown: llmCooldownDefault, immediate: true}
	}
	// 限流、并发超限多半过一两秒就放行，先在原模型上重试，不行再切。
	if status == 429 || (status == 0 || status >= 500) && containsAnyMarker(text, []string{"rate limit", "rate_limit", "ratelimit", "rate-limited", "rate limited", "concurrency limit", "too many requests", "限流", "请求过于频繁"}) {
		return llmFailurePolicy{retry: true, failover: true, cooldown: llmCooldownDefault}
	}
	if status == 401 || status == 403 {
		return llmFailurePolicy{failover: true, cooldown: llmCooldownDefault, immediate: true}
	}
	if isModelUnavailableLLMError(err) {
		return llmFailurePolicy{failover: true, cooldown: llmCooldownDefault}
	}
	switch status {
	case 500, 502, 503, 504, 529:
		return llmFailurePolicy{retry: true, failover: true, cooldown: llmCooldownTransient}
	}
	if status != 0 {
		return llmFailurePolicy{}
	}
	if containsAnyMarker(text, []string{"unauthorized", "forbidden", "invalid api key", "invalid_api_key", "incorrect api key", "authentication", "permission_error", "未授权", "无权限"}) {
		return llmFailurePolicy{failover: true, cooldown: llmCooldownDefault, immediate: true}
	}
	if containsAnyMarker(text, []string{"context deadline exceeded", "client.timeout exceeded", "timeout awaiting response headers", "response header timeout", "response body idle timeout", "stream idle timeout"}) {
		return llmFailurePolicy{retry: true, failover: true}
	}
	if containsAnyMarker(text, []string{"bad gateway", "service unavailable", "gateway timeout", "cloudflare", "eof", "connection reset", "connection refused", "connection aborted", "unexpected end of file", "server closed idle connection", "stream ended before completion"}) {
		return llmFailurePolicy{retry: true, failover: true, cooldown: llmCooldownTransient}
	}
	return llmFailurePolicy{}
}

type contentPolicyRejectionError struct {
	cause error
}

func (e contentPolicyRejectionError) Error() string {
	if e.cause == nil {
		return errContentPolicyRejection.Error()
	}
	return e.cause.Error()
}

func (e contentPolicyRejectionError) Unwrap() error {
	return e.cause
}

func (e contentPolicyRejectionError) Is(target error) bool {
	return target == errContentPolicyRejection
}

func classifyLLMError(err error) error {
	if err == nil || errors.Is(err, errContentPolicyRejection) || !isContentPolicyRejection(err) {
		return err
	}
	return contentPolicyRejectionError{cause: err}
}

func isContentPolicyRejection(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, llm.ErrContentBlocked) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"content_policy_violation",
		"content policy violation",
		"content filter",
		"content_filter",
		"safety policy",
		"request was rejected because it was considered high risk",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return strings.Contains(text, "high risk") &&
		(strings.Contains(text, "reject") || strings.Contains(text, "refus"))
}

// isLLMUpstreamOutage 认出「网关整体不可用」这类与请求内容无关的失败：502/503、
// 并发或频率限制的 429。超时、空响应、4xx 参数错误都不算，那些可能跟这次请求本身
// 有关，重来多少次都一样。
func isLLMUpstreamOutage(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"502 bad gateway", "503 service unavailable",
		"429 too many requests", "rate_limit_error", "concurrency limit exceeded",
		"service temporarily unavailable", "upstream service temporarily unavailable",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isModelUnavailableLLMError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"model_not_found",
		"unsupported_model",
		"model not found",
		"model is not available",
		"model is unavailable",
		"does not support model",
		"doesn't support model",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return strings.Contains(text, "model") &&
		strings.Contains(text, "not supported by any configured account")
}

// llmProviderAttemptError 记住这次上游调用是发生在哪个配置档、哪个 provider、
// 哪个模型上的。
//
// 「出错了：llm: provider request failed: POST ...: 403 Forbidden」只说明有个上游
// 拒绝了请求，配了好几个配置档时根本看不出该去改哪一个。把身份标在错误里，报错
// 自己就能回答「哪个模型、哪个 provider」。
//
// 只标真的打到 provider 的失败：内容安全拒绝、调用方取消这些不是上游坏了，标上
// 配置档名只会让人以为是配置问题。
type llmProviderAttemptError struct {
	profile  string
	provider llm.Provider
	model    string
	cause    error
}

func (e llmProviderAttemptError) Error() string {
	if e.cause == nil {
		return e.label()
	}
	label := e.label()
	if label == "" {
		return e.cause.Error()
	}
	return label + " 调用失败：" + e.cause.Error()
}

func (e llmProviderAttemptError) Unwrap() error { return e.cause }

// label 拼出人能直接拿去改配置的那串身份。
func (e llmProviderAttemptError) label() string {
	detail := make([]string, 0, 2)
	if provider := strings.TrimSpace(string(e.provider)); provider != "" {
		detail = append(detail, provider)
	}
	if model := strings.TrimSpace(e.model); model != "" {
		detail = append(detail, model)
	}
	profile := strings.TrimSpace(e.profile)
	switch {
	case profile != "" && len(detail) > 0:
		return fmt.Sprintf("配置档「%s」(%s)", profile, strings.Join(detail, " · "))
	case profile != "":
		return fmt.Sprintf("配置档「%s」", profile)
	default:
		return strings.Join(detail, " · ")
	}
}

// annotateLLMProviderAttempt 给一次配置档尝试的失败标上身份。
func annotateLLMProviderAttempt(err error, profile llm.Profile, req llm.GenerateRequest) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, errContentPolicyRejection) || isContentPolicyRejection(err) {
		return err
	}
	var existing llmProviderAttemptError
	if errors.As(err, &existing) {
		// 已经标过了：降级链里第一个失败的配置档才是这条错误的出处。
		return err
	}
	attempt := llmProviderAttemptError{
		profile:  strings.TrimSpace(profile.Name),
		provider: profile.Config.Provider,
		model:    firstNonEmpty(strings.TrimSpace(req.Model), strings.TrimSpace(profile.Config.Model)),
		cause:    err,
	}
	if attempt.label() == "" {
		return err
	}
	return attempt
}

// llmProviderAttemptLabel 取出错误里的配置档身份，供那些改写了正文的用户提示补回来。
func llmProviderAttemptLabel(err error) string {
	var attempt llmProviderAttemptError
	if errors.As(err, &attempt) {
		return attempt.label()
	}
	return ""
}
