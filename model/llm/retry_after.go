// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"errors"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
)

// retryAfterHinter 由记得上游 Retry-After 的错误实现。
type retryAfterHinter interface {
	retryAfterHint() time.Duration
}

// RetryAfterHint 取出上游用 HTTP Retry-After 头告诉的等待时间，没有时返回 0。
//
// 错误正文里写的「Wait 311000s」这类提示不在这里解析：那是各家网关自己拼的文案，
// 由调用方按需识别；这里只认协议层给的头。
func RetryAfterHint(err error) time.Duration {
	if err == nil {
		return 0
	}
	var hinter retryAfterHinter
	if errors.As(err, &hinter) {
		if wait := hinter.retryAfterHint(); wait > 0 {
			return wait
		}
	}
	var mediaErr *MediaAPIError
	if errors.As(err, &mediaErr) && mediaErr.RetryAfter > 0 {
		return mediaErr.RetryAfter
	}
	var openAIErr *openai.Error
	if errors.As(err, &openAIErr) && openAIErr.Response != nil {
		if wait := parseRetryAfter(openAIErr.Response.Header.Get("Retry-After")); wait > 0 {
			return wait
		}
	}
	var anthropicErr *anthropic.Error
	if errors.As(err, &anthropicErr) && anthropicErr.Response != nil {
		if wait := parseRetryAfter(anthropicErr.Response.Header.Get("Retry-After")); wait > 0 {
			return wait
		}
	}
	return 0
}
