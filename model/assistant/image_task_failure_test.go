// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 这几条错误原文都是线上生图任务真实失败时记下来的。
func TestClassifyImageFailureCoversProductionErrors(t *testing.T) {
	cases := []struct {
		err      error
		category string
	}{
		{errors.New(`Post "https://images.example/v1/images/generations": net/http: TLS handshake timeout`), "timeout"},
		{errors.New(`Post "https://images.example/v1/images/edits": EOF`), "upstream_unavailable"},
		{errors.New(`Post "https://images.example/v1/images/edits": unexpected EOF`), "upstream_unavailable"},
		{errors.New("llm: openai-compatible request failed: 400 Bad Request: code=moderation_blocked; type=image_generation_user_error; message=Your request was rejected by the safety system."), "content_policy"},
		{errors.New("llm: openai-compatible request failed: 429 Too Many Requests: rate limit reached"), "rate_limited"},
		{errors.New("llm: openai-compatible request failed: 401 Unauthorized: invalid api key"), "auth"},
		{errors.New("llm: openai-compatible request failed: 404 Not Found: model_not_found"), "model_not_found"},
		{errors.New("图片接口没有返回图片"), "empty_output"},
		{errImageEditSourceNotFound, "source_missing"},
		{errors.New("diana: llm profile store is not configured"), "not_configured"},
	}
	for _, tc := range cases {
		got := classifyImageFailure(tc.err, false, 0)
		if got.Category != tc.category {
			t.Errorf("classifyImageFailure(%q).Category = %q, want %q", tc.err, got.Category, tc.category)
		}
		if got.Reason == "" || got.Advice == "" {
			t.Errorf("classifyImageFailure(%q) = %#v, want reason and advice", tc.err, got)
		}
		if strings.Contains(got.message("图片生成"), "https://") {
			t.Errorf("message leaks address: %q", got.message("图片生成"))
		}
	}
	timedOut := classifyImageFailure(context.DeadlineExceeded, true, 3*time.Minute)
	if timedOut.Category != "task_timeout" || !strings.Contains(timedOut.Reason, "任务时限") {
		t.Fatalf("timeout advice = %#v", timedOut)
	}
}

// 带了配置档身份的错误同样要能分出类，身份标签本身不该被当成原因。
func TestClassifyImageFailureSeesThroughProviderLabel(t *testing.T) {
	err := annotateImageProviderAttempt(errors.New(`Post "https://images.example/v1/images/edits": EOF`), llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, ImageModel: "gpt-image-test"})
	if label := llmProviderAttemptLabel(err); !strings.Contains(label, "gpt-image-test") {
		t.Fatalf("label = %q", label)
	}
	if got := classifyImageFailure(err, false, 0); got.Category != "upstream_unavailable" {
		t.Fatalf("category = %q", got.Category)
	}
}

// 错误提示关掉了，生图失败照样要告诉请求的人原因和建议；生图失败开关关掉才静默。
func TestImageTaskFailureNoticeIgnoresErrorNotifySwitch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		imageFlag  *bool
		wantNotice bool
	}{
		{name: "default", wantNotice: true},
		{name: "disabled", imageFlag: boolPtr(false), wantNotice: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &concurrentRecordingChannel{}
			runtime := NewRuntime(BotConfig{BotAccount: "42", ErrorNotifyEnabled: boolPtr(false), ImageFailureNoticeEnabled: tc.imageFlag}, channel, NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: EventKindGroup, GroupID: "12345", UserID: "10001", MessageID: "message-1"}
			task := PluginTask{
				Kind: "image", Name: "图片生成", Key: "image:failure",
				Run: func(context.Context, PluginTaskServices) (PluginTaskResult, error) {
					return PluginTaskResult{}, errors.New("llm: openai-compatible request failed: 400 Bad Request: code=moderation_blocked")
				},
			}
			if _, handled, err := runtime.launchPluginTasks(context.Background(), event, []PluginTask{task}); err != nil || !handled {
				t.Fatalf("launchPluginTasks() handled=%v err=%v", handled, err)
			}
			if !tc.wantNotice {
				time.Sleep(300 * time.Millisecond)
				for _, message := range channel.messages() {
					if strings.Contains(message.Text, "失败") {
						t.Fatalf("unexpected notice %q", message.Text)
					}
				}
				return
			}
			waitForCondition(t, time.Second, func() bool {
				for _, message := range channel.messages() {
					if strings.Contains(message.Text, "图片生成失败") {
						return true
					}
				}
				return false
			})
			for _, message := range channel.messages() {
				if strings.Contains(message.Text, "图片生成失败") && (!strings.Contains(message.Text, "安全审核") || !strings.Contains(message.Text, "处理建议")) {
					t.Fatalf("notice = %q", message.Text)
				}
			}
		})
	}
}
