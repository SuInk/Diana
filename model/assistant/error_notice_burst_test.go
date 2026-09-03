// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newErrorNoticeBurstRuntime(t *testing.T) (*Runtime, *recordingChannel) {
	t.Helper()
	withFastSendTiming(t)
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{ResponseMode: ResponseModeStandard}, channel, NewPluginManager(), nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		runtime.clearErrorNoticeBursts()
	})
	runtime.mu.Lock()
	runtime.running = true
	runtime.runCtx = ctx
	runtime.mu.Unlock()
	runtime.errorNoticeQuiet = 30 * time.Millisecond
	runtime.errorNoticeMaxWait = 2 * time.Second
	runtime.errorNoticeFreshWindow = 30 * time.Minute
	return runtime, channel
}

func waitForSentText(t *testing.T, channel *recordingChannel, want string) OutgoingMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, msg := range channel.sentSnapshot() {
			if strings.Contains(msg.Text, want) {
				return msg
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no sent message containing %q; sent=%v", want, channel.sentSnapshot())
	return OutgoingMessage{}
}

// 一次爆发只该刷一条即时提示：第一条立刻发，后面的并进爆发结束后的那条汇总。
func TestErrorNoticeBurstSendsFirstThenSummarizesTheRest(t *testing.T) {
	runtime, channel := newErrorNoticeBurstRuntime(t)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "m1", Time: time.Now().Unix()}

	if !runtime.claimErrorNotice(event, "上游模型服务请求超时") {
		t.Fatalf("first failure must be announced immediately")
	}
	for i := range 3 {
		if runtime.claimErrorNotice(event, "上游模型服务请求超时") {
			t.Fatalf("failure %d during the burst must be merged, not announced", i+2)
		}
	}

	summary := waitForSentText(t, channel, "另外还有 3 条消息也没能回复")
	if !strings.Contains(summary.Text, "上游模型服务请求超时") {
		t.Fatalf("summary must carry the reason, got %q", summary.Text)
	}
	// 汇总说的是一批消息，引用其中任何一条都会让人以为只有那条出了问题。
	if summary.ReplyMessageID != "" {
		t.Fatalf("summary must not quote a single message, got reply_message_id=%q", summary.ReplyMessageID)
	}
	if got := len(channel.sentSnapshot()); got != 1 {
		t.Fatalf("burst must produce exactly one aggregated notice, got %d", got)
	}
}

// 回补的旧消息不值得为它单独打断当前的群聊，但也不能一条都不说：只进汇总。
func TestErrorNoticeBurstSummarizesStaleBackfillWithoutImmediateNotice(t *testing.T) {
	runtime, channel := newErrorNoticeBurstRuntime(t)
	stale := MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "123456",
		UserID:    "10001",
		MessageID: "old-1",
		Time:      time.Now().Add(-45 * time.Minute).Unix(),
	}

	for i := range 5 {
		if runtime.claimErrorNotice(stale, "上游模型服务请求超时") {
			t.Fatalf("stale backfill failure %d must never be announced on its own", i+1)
		}
	}

	summary := waitForSentText(t, channel, "有 5 条消息没能回复")
	if strings.Contains(summary.Text, "另外还有") {
		t.Fatalf("nothing was announced before, so the summary must not say 另外还有: %q", summary.Text)
	}
	if got := len(channel.sentSnapshot()); got != 1 {
		t.Fatalf("stale burst must produce exactly one notice, got %d", got)
	}
}

// 即时提示自己也没发出去时，这轮不能算已经交代过，得由汇总兜底。
func TestErrorNoticeBurstFallsBackToSummaryWhenTheFirstNoticeFails(t *testing.T) {
	runtime, channel := newErrorNoticeBurstRuntime(t)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "m1", Time: time.Now().Unix()}

	if !runtime.claimErrorNotice(event, "上游模型服务请求超时") {
		t.Fatalf("first failure must be announced immediately")
	}
	runtime.noteErrorNoticeSendFailed(event, "上游模型服务请求超时")

	waitForSentText(t, channel, "有 1 条消息没能回复")
}

// 安静下来之后再出故障，是新的一轮：要重新立刻发，而不是继续闷在汇总里。
func TestErrorNoticeBurstReopensAfterQuietPeriod(t *testing.T) {
	runtime, _ := newErrorNoticeBurstRuntime(t)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "m1", Time: time.Now().Unix()}

	if !runtime.claimErrorNotice(event, "上游模型服务请求超时") {
		t.Fatalf("first failure must be announced immediately")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		runtime.errorNoticeMu.Lock()
		_, tracked := runtime.errorNoticeBursts[sessionKey(event)]
		runtime.errorNoticeMu.Unlock()
		if !tracked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("burst state was never released after the quiet period")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !runtime.claimErrorNotice(event, "上游模型服务请求超时") {
		t.Fatalf("a failure after the burst ended must be announced immediately again")
	}
}

// 群聊和私聊各算各的爆发：一个会话在刷错，不该让另一个会话的第一条也被咽掉。
func TestErrorNoticeBurstIsPerSession(t *testing.T) {
	runtime, _ := newErrorNoticeBurstRuntime(t)
	group := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "m1", Time: time.Now().Unix()}
	private := MessageEvent{Kind: EventKindPrivate, UserID: "20002", MessageID: "m2", Time: time.Now().Unix()}

	if !runtime.claimErrorNotice(group, "上游模型服务请求超时") {
		t.Fatalf("group failure must be announced immediately")
	}
	if runtime.claimErrorNotice(group, "上游模型服务请求超时") {
		t.Fatalf("second group failure must be merged")
	}
	if !runtime.claimErrorNotice(private, "上游模型服务请求超时") {
		t.Fatalf("the private session has its own first failure and must be announced")
	}
}

// 运行时没在跑就没有能兜底发汇总的地方，退回「每条都发」而不是合并成谁也收不到。
func TestErrorNoticeBurstFallsBackToPerMessageWhenRuntimeIsNotRunning(t *testing.T) {
	withFastSendTiming(t)
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", Time: time.Now().Unix()}
	for i := range 3 {
		if !runtime.claimErrorNotice(event, "上游模型服务请求超时") {
			t.Fatalf("failure %d must still be announced when the runtime is not running", i+1)
		}
	}
}

func TestErrorNoticeSummaryText(t *testing.T) {
	if got := errorNoticeSummaryText(3, true, "上游模型服务请求超时"); got != "另外还有 3 条消息也没能回复：上游模型服务请求超时" {
		t.Fatalf("announced summary = %q", got)
	}
	if got := errorNoticeSummaryText(2, false, "上游模型服务请求超时"); got != "有 2 条消息没能回复：上游模型服务请求超时" {
		t.Fatalf("unannounced summary = %q", got)
	}
	if got := errorNoticeSummaryText(1, false, "   "); got != "有 1 条消息没能回复" {
		t.Fatalf("summary without a reason = %q", got)
	}
}
