// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 后台任务的超时多半出在抓 Feed、查 GitHub 上，不能一律说成模型超时。
func TestPublicTaskErrorMessageDoesNotBlameModelForForeignTimeouts(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("抓取 Feed 失败: %w", context.DeadlineExceeded),
		fmt.Errorf(`Get "https://feeds.example/rss": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`),
		repositoryWatchStageFailure(repositoryWatchFailureStagePolling, fmt.Errorf("查询 releases: %w", context.DeadlineExceeded)),
	} {
		got := publicTaskErrorMessage(err)
		if strings.Contains(got, "模型") {
			t.Fatalf("%v was blamed on the model: %q", err, got)
		}
		if !strings.Contains(got, "超时") {
			t.Fatalf("%v lost the timeout reason: %q", err, got)
		}
		if strings.Contains(got, "feeds.example") {
			t.Fatalf("message leaked the URL: %q", got)
		}
	}
	_, _, reason := repositoryWatchFailureDetails(fmt.Errorf("查询 releases: %w", context.DeadlineExceeded))
	if strings.Contains(reason, "模型") {
		t.Fatalf("repository watch reason blamed the model: %q", reason)
	}
}

// 经过模型重试链、带着配置档身份的超时仍然归到模型头上，并说清是哪个配置档。
func TestPublicTaskErrorMessageKeepsModelAttributionForLLMAttempts(t *testing.T) {
	err := annotateLLMProviderAttempt(fmt.Errorf("generate: %w", context.DeadlineExceeded), llm.Profile{Name: "主力"}, llm.GenerateRequest{})
	got := publicTaskErrorMessage(err)
	if !strings.HasPrefix(got, "上游模型服务请求超时") || !strings.Contains(got, "主力") {
		t.Fatalf("message = %q", got)
	}
}

// 任务自己的时限到了，提示要说是时限，不能把里面某一步的报错当成原因。
func TestSubagentTaskTimeoutReportsTaskDeadline(t *testing.T) {
	channel := &concurrentRecordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "message-timeout"}
	task := PluginTask{
		Kind:    "test",
		Name:    "识别文档",
		Key:     "test:timeout",
		Timeout: 100 * time.Millisecond,
		Run: func(ctx context.Context, _ PluginTaskServices) (PluginTaskResult, error) {
			<-ctx.Done()
			return PluginTaskResult{}, fmt.Errorf("调用识别服务: %w", ctx.Err())
		},
	}
	if _, handled, err := runtime.launchPluginTasks(context.Background(), event, []PluginTask{task}); err != nil || !handled {
		t.Fatalf("launchPluginTasks() handled=%v err=%v", handled, err)
	}
	var failure string
	waitForCondition(t, 3*time.Second, func() bool {
		for _, message := range channel.messages() {
			if strings.Contains(message.Text, "执行失败") {
				failure = message.Text
				return true
			}
		}
		return false
	})
	if !strings.Contains(failure, "超过任务时限") || strings.Contains(failure, "模型") {
		t.Fatalf("failure notice = %q", failure)
	}
}
