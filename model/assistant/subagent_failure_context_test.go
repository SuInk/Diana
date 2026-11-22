// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 生图失败、提示也没发进聊天（错误提示关着）时，下一轮回复的尾部要告诉模型这张图
// 没画出来，不能让它照着受理时的「画完自动发」继续说还在画。
func TestFailedImageTaskReachesReplyTailWhenNoticeSilent(t *testing.T) {
	channel := &concurrentRecordingChannel{}
	cfg := BotConfig{BotAccount: "42", ErrorNotifyEnabled: boolPtr(false)}
	runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "12345", UserID: "10001", MessageID: "message-1"}
	fail := true
	task := func() PluginTask {
		return PluginTask{
			Kind: "image", Name: "图片生成", Key: "image:" + time.Now().String(),
			Run: func(context.Context, PluginTaskServices) (PluginTaskResult, error) {
				if fail {
					return PluginTaskResult{}, errors.New("llm: openai-compatible request failed: 503 Service Unavailable: type=api_error; message=No available compatible accounts")
				}
				return PluginTaskResult{Delivered: true}, nil
			},
		}
	}
	if _, handled, err := runtime.launchPluginTasks(context.Background(), event, []PluginTask{task()}); err != nil || !handled {
		t.Fatalf("launchPluginTasks() handled=%v err=%v", handled, err)
	}
	var tail string
	waitForCondition(t, time.Second, func() bool {
		tail = runtime.failedSubagentTasksPrompt(event, cfg)
		return tail != ""
	})
	if !strings.HasPrefix(tail, "【后台任务失败】") || !strings.Contains(tail, "图片生成（task_id=") || !strings.Contains(tail, "上游服务暂时不可用") {
		t.Fatalf("tail = %q", tail)
	}
	for _, message := range channel.messages() {
		if strings.Contains(message.Text, "失败") {
			t.Fatalf("notice should stay silent, got %q", message.Text)
		}
	}
	other := event
	other.GroupID = "54321"
	if got := runtime.failedSubagentTasksPrompt(other, cfg); got != "" {
		t.Fatalf("other session tail = %q", got)
	}

	// 同类任务之后成功了，旧的失败就不再提。
	fail = false
	if _, handled, err := runtime.launchPluginTasks(context.Background(), event, []PluginTask{task()}); err != nil || !handled {
		t.Fatalf("launchPluginTasks() handled=%v err=%v", handled, err)
	}
	waitForCondition(t, time.Second, func() bool {
		return runtime.failedSubagentTasksPrompt(event, cfg) == ""
	})
}

func TestFailedSubagentTaskLogExpiresAndCaps(t *testing.T) {
	var log failedSubagentTaskLog
	event := MessageEvent{Kind: EventKindGroup, GroupID: "12345"}
	now := time.Now()
	log.record(event, failedSubagentTask{id: "old", kind: "image", at: now.Add(-failedSubagentTaskTTL - time.Minute)})
	for i := range failedSubagentTaskMax + 2 {
		log.record(event, failedSubagentTask{id: string(rune('a' + i)), kind: "image", at: now})
	}
	items := log.recent(event, now)
	if len(items) != failedSubagentTaskMax {
		t.Fatalf("len = %d, want %d", len(items), failedSubagentTaskMax)
	}
	for _, item := range items {
		if item.id == "old" || item.id == "a" {
			t.Fatalf("kept %q", item.id)
		}
	}
	if got := log.recent(event, now.Add(failedSubagentTaskTTL)); len(got) != 0 {
		t.Fatalf("expired items kept: %v", got)
	}
}
