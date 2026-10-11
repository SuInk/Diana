// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"errors"
	"regexp"
	"strconv"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"google.golang.org/genai"
)

// ErrOutputCommitted prevents a new generation after visible output was sent.
var ErrOutputCommitted = errors.New("llm visible output already committed")

// ErrorStatusCode prefers structured provider errors over legacy error text.
func ErrorStatusCode(err error) int {
	if err == nil {
		return 0
	}
	var requestErr *openAIRequestError
	if errors.As(err, &requestErr) && requestErr.statusCode != 0 {
		return requestErr.statusCode
	}
	var mediaErr *MediaAPIError
	if errors.As(err, &mediaErr) && mediaErr.StatusCode != 0 {
		return mediaErr.StatusCode
	}
	var openAIErr *openai.Error
	if errors.As(err, &openAIErr) {
		if openAIErr.StatusCode != 0 {
			return openAIErr.StatusCode
		}
		if openAIErr.Response != nil {
			return openAIErr.Response.StatusCode
		}
	}
	var anthropicErr *anthropic.Error
	if errors.As(err, &anthropicErr) {
		if anthropicErr.StatusCode != 0 {
			return anthropicErr.StatusCode
		}
		if anthropicErr.Response != nil {
			return anthropicErr.Response.StatusCode
		}
	}
	var geminiErr genai.APIError
	if errors.As(err, &geminiErr) && geminiErr.Code != 0 {
		return geminiErr.Code
	}
	var geminiPtr *genai.APIError
	if errors.As(err, &geminiPtr) && geminiPtr.Code != 0 {
		return geminiPtr.Code
	}
	match := errorStatusPattern.FindStringSubmatch(err.Error())
	if match == nil {
		return 0
	}
	code, _ := strconv.Atoi(match[1])
	return code
}

// Require a status label or a standalone leading status, not digits in IDs,
// token counts, URLs or a quoted model name.
var errorStatusPattern = regexp.MustCompile(`(?i)(?:^\s*|\b(?:http(?:/\d(?:\.\d)?)?|status(?:\s+code)?|status_code|error|code)["'\s:=]+)([45][0-9]{2})(?:\b)`)
