// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/openai/openai-go/v3/responses"
)

// 会思考的模型把 max_output_tokens 花在 reasoning 上时，Responses 返回
// status=incomplete 且输出里只有 reasoning。这和 Chat Completions 的截断是同一回事，
// 必须归到同一个哨兵，调用方才能放宽上限重试，而不是当成模型不可用去切配置。
func TestOpenAIResponsesEmptyOutputMarksTruncation(t *testing.T) {
	decode := func(t *testing.T, payload string) *responses.Response {
		t.Helper()
		var resp responses.Response
		if err := json.Unmarshal([]byte(payload), &resp); err != nil {
			t.Fatal(err)
		}
		return &resp
	}

	truncated := decode(t, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"reasoning","id":"rs_1","summary":[]}]}`)
	if err := openAIResponsesEmptyOutputError(truncated); !errors.Is(err, ErrCompletionTruncatedNoText) {
		t.Fatalf("截断的空输出没有归到 ErrCompletionTruncatedNoText: %v", err)
	}

	// 只有 reasoning、服务端没给 reason 的，也按截断处理。
	reasoningOnly := decode(t, `{"status":"incomplete","output":[{"type":"reasoning","id":"rs_1","summary":[]}]}`)
	if err := openAIResponsesEmptyOutputError(reasoningOnly); !errors.Is(err, ErrCompletionTruncatedNoText) {
		t.Fatalf("只有 reasoning 的空输出没有归到截断: %v", err)
	}

	// 正常结束却没有输出是另一回事，不能让调用方去掉上限重试。
	completed := decode(t, `{"status":"completed","output":[]}`)
	if err := openAIResponsesEmptyOutputError(completed); err == nil || errors.Is(err, ErrCompletionTruncatedNoText) {
		t.Fatalf("completed 的空输出不该算截断: %v", err)
	}
	if err := openAIResponsesEmptyOutputError(nil); err == nil {
		t.Fatal("nil 响应也要返回错误")
	}
}
