// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPublicQQErrorMessageHidesRelayURL(t *testing.T) {
	err := errors.New(`Post "https://relay.private.example/v1/responses": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`)
	got := publicQQErrorMessage(err)
	if got != "上游模型服务响应超时，请稍后重试。" {
		t.Fatalf("message = %q", got)
	}
	for _, leaked := range []string{"relay.private.example", "/v1/responses", "https://"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("message leaked %q: %q", leaked, got)
		}
	}
}

func TestPublicQQErrorMessageDoesNotExposeUnknownProviderError(t *testing.T) {
	err := errors.New(`request to https://relay.private.example/v1 failed: Authorization: Bearer example-secret-token`)
	got := publicQQErrorMessage(err)
	for _, useful := range []string{"request to", "failed", "[REDACTED_URL]", "[REDACTED]"} {
		if !strings.Contains(got, useful) {
			t.Fatalf("message %q does not contain diagnostic %q", got, useful)
		}
	}
	for _, leaked := range []string{"relay.private.example", "/v1", "example-secret-token"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("message leaked %q: %q", leaked, got)
		}
	}
}

func TestPublicQQErrorMessageRedactsBareHostCredentialsAndLocalPath(t *testing.T) {
	raw := `provider relay.private.example failed: api_key=test-value file=/private/config.json`
	got := publicQQErrorMessage(errors.New(raw))
	for _, useful := range []string{"provider", "failed", "[REDACTED_HOST]", "api_key=[REDACTED]", "[REDACTED_PATH]"} {
		if !strings.Contains(got, useful) {
			t.Fatalf("message %q does not contain %q", got, useful)
		}
	}
	for _, leaked := range []string{"relay.private.example", "test-value", "/private/config.json"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("message leaked %q: %q", leaked, got)
		}
	}
}

func TestPublicQQErrorMessageMapsEmptyModelOutput(t *testing.T) {
	err := errors.New("llm: openai-compatible chat completions output is empty")
	got := publicQQErrorMessage(err)
	if got != "上游模型服务暂时没有返回有效内容，请稍后重试。" {
		t.Fatalf("message = %q", got)
	}
	if strings.Contains(strings.ToLower(got), "openai") || strings.Contains(strings.ToLower(got), "output is empty") {
		t.Fatalf("message exposed provider details: %q", got)
	}
}

func TestPublicQQErrorMessageMapsUnavailableImage(t *testing.T) {
	err := newImageMediaUnavailableError([]error{errors.New("image download failed: status=400")})
	got := publicQQErrorMessage(err)
	if got != "图片读取失败：原图片地址不可用，OneBot v11 回退也没有取得可读取的本地文件或下载地址。请重新发送图片后再试。" {
		t.Fatalf("message = %q", got)
	}
}

func TestPublicQQErrorMessageGuidesGitHubRateLimitConfiguration(t *testing.T) {
	err := errors.New("读取 SuInk/Diana commits: GitHub API 匿名请求额度已耗尽（公开仓库同样受限）")
	got := publicQQErrorMessage(err)
	for _, text := range []string{"GitHub API", "公开仓库", "插件 → 仓库更新订阅 → 设置", "GitHub Token"} {
		if !strings.Contains(got, text) {
			t.Fatalf("message %q does not contain %q", got, text)
		}
	}
	if strings.Contains(got, "模型服务") {
		t.Fatalf("GitHub error was mislabeled as a model error: %q", got)
	}
}

func TestPublicQQErrorMessageGuidesInvalidGitHubTokenConfiguration(t *testing.T) {
	err := errors.New("读取 SuInk/Diana releases: GitHub Token 无效或已过期，请在插件设置中重新配置")
	got := publicQQErrorMessage(err)
	if !strings.Contains(got, "GitHub Token 无效或已过期") || !strings.Contains(got, "插件 → 仓库更新订阅 → 设置") {
		t.Fatalf("message = %q", got)
	}
}

func TestReplyAndRecordSendsSanitizedErrorButKeepsDiagnostic(t *testing.T) {
	channel := &recordingChannel{}
	rawErr := errors.New(`Post "https://relay.private.example/v1/responses": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`)
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return failingLLMProvider{err: rawErr}, nil
	})
	event := MessageEvent{Kind: EventKindPrivate, UserID: "user", MessageID: "redacted-error"}
	outcome, err := runtime.replyAndRecord(context.Background(), event, "测试", "replied")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "error_replied" || len(channel.sent) != 1 {
		t.Fatalf("outcome=%q sent=%#v", outcome, channel.sent)
	}
	if got := channel.sent[0].Text; got != "出错了：上游模型服务响应超时，请稍后重试。" {
		t.Fatalf("sent text = %q", got)
	}
	if !strings.Contains(runtime.Status().LastError, "relay.private.example") {
		t.Fatalf("diagnostic error was unexpectedly redacted: %q", runtime.Status().LastError)
	}
}

func TestReplyAndRecordClassifiesContentPolicyAndSanitizesDiagnostic(t *testing.T) {
	channel := &recordingChannel{}
	rawErr := errors.New(`content_policy_violation from https://relay.private.example/v1: Authorization: Bearer owner-token`)
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return failingLLMProvider{err: rawErr}, nil
	})
	outcome, err := runtime.replyAndRecord(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "user", MessageID: "policy-error"}, "测试", "replied")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "error_replied_content_policy" || len(channel.sent) != 1 {
		t.Fatalf("outcome=%q sent=%#v", outcome, channel.sent)
	}
	message := channel.sent[0].Text
	if !strings.Contains(message, "content_policy_violation") || !strings.Contains(message, "[REDACTED_URL]") {
		t.Fatalf("message lost useful diagnostic: %q", message)
	}
	for _, leaked := range []string{"relay.private.example", "owner-token"} {
		if strings.Contains(message, leaked) {
			t.Fatalf("message leaked %q: %q", leaked, message)
		}
	}
}

func TestReplyAndRecordDoesNotCountUnacknowledgedErrorNoticeAsReplied(t *testing.T) {
	channel := &scriptedChannel{}
	rawErr := errors.New("model failed")
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return failingLLMProvider{err: rawErr}, nil
	})
	event := MessageEvent{Kind: EventKindPrivate, UserID: "user", MessageID: "unconfirmed-error"}
	outcome, err := runtime.replyAndRecord(context.Background(), event, "测试", "replied")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "error_send_unconfirmed" || len(channel.sent) != 1 {
		t.Fatalf("outcome=%q sent=%#v", outcome, channel.sent)
	}
	recent := runtime.Status().RecentEvents
	if len(recent) == 0 || recent[0].Handled || recent[0].Decision != "error" {
		t.Fatalf("unconfirmed event = %#v", recent)
	}
}
