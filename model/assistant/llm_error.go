// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

var errContentPolicyRejection = errors.New("llm content policy rejection")

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
