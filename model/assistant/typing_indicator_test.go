// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeChatActionChannel struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
	err   error
	// errCount > 0 时只让前几次调用失败，之后恢复成功：由假通道自己决定失败次数，
	// 测试线程不必掐时机去清错误标记。
	errCount int
}

func (c *fakeChatActionChannel) SendChatAction(ctx context.Context, _ OutgoingMessage, _ string) error {
	c.mu.Lock()
	delay, err := c.delay, c.err
	if c.errCount > 0 {
		c.errCount--
		err = errors.New("uid is empty")
	}
	c.calls++
	c.mu.Unlock()
	if delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return err
}

func (c *fakeChatActionChannel) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func newTestTypingIndicator(ctx context.Context, channel ChatActionChannel, interval time.Duration, maxFailures int) *typingIndicator {
	indicator := &typingIndicator{
		channel:     channel,
		interval:    interval,
		callTimeout: 20 * time.Millisecond,
		maxFailures: maxFailures,
		kick:        make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
	go indicator.run(ctx)
	return indicator
}

func waitForCalls(t *testing.T, channel *fakeChatActionChannel, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if channel.callCount() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("expected at least %d typing refreshes, got %d", want, channel.callCount())
}

func TestTypingIndicatorRenewsUntilStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	channel := &fakeChatActionChannel{}
	indicator := newTestTypingIndicator(ctx, channel, 10*time.Millisecond, oneBotTypingMaxFailures)
	waitForCalls(t, channel, 3)
	indicator.stop()
	settled := channel.callCount()
	time.Sleep(60 * time.Millisecond)
	if got := channel.callCount(); got > settled+1 {
		t.Fatalf("stopped indicator kept refreshing: %d -> %d", settled, got)
	}
}

// 消息发出后要静音：「正在输入」只覆盖到回复发出为止，再点亮就会在最后一条回复
// 之后继续闪。还有下一条要发时，resume 必须立刻补一次，不等下一个间隔。
func TestTypingIndicatorPauseAndResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	channel := &fakeChatActionChannel{}
	indicator := newTestTypingIndicator(ctx, channel, 5*time.Millisecond, oneBotTypingMaxFailures)
	defer indicator.stop()
	waitForCalls(t, channel, 1)
	indicator.pause()
	time.Sleep(40 * time.Millisecond)
	paused := channel.callCount()
	indicator.resume()
	waitForCalls(t, channel, paused+1)
}

// 单次刷新超时只说明连接这会儿忙，不能把整轮输入状态判死。
func TestTypingIndicatorSurvivesSlowRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	channel := &fakeChatActionChannel{delay: time.Second}
	indicator := newTestTypingIndicator(ctx, channel, time.Millisecond, oneBotTypingMaxFailures)
	defer indicator.stop()
	waitForCalls(t, channel, 5)
}

// 偶发失败不能把整轮输入状态判死：接入端查不到 uid、重连瞬间调用失败都属于这类，
// 停掉的话用户看到的就是「正在输入」先没了、回复还没出来。
func TestTypingIndicatorSurvivesTransientFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 连续失败上限之内失败几次就恢复，之后必须一直刷下去。
	channel := &fakeChatActionChannel{errCount: oneBotTypingMaxFailures - 1}
	indicator := newTestTypingIndicator(ctx, channel, time.Millisecond, oneBotTypingMaxFailures)
	defer indicator.stop()
	waitForCalls(t, channel, oneBotTypingMaxFailures+5)
}

// 接入端根本没有 set_input_status 时，连续失败到上限就收手，不再一直空转。
func TestTypingIndicatorStopsAfterRepeatedFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	channel := &fakeChatActionChannel{err: errors.New("unsupported action")}
	indicator := newTestTypingIndicator(ctx, channel, time.Millisecond, oneBotTypingMaxFailures)
	defer indicator.stop()
	waitForCalls(t, channel, oneBotTypingMaxFailures)
	time.Sleep(30 * time.Millisecond)
	if got := channel.callCount(); got != oneBotTypingMaxFailures {
		t.Fatalf("expected %d attempts before giving up, got %d", oneBotTypingMaxFailures, got)
	}
}

func TestTypingIndicatorNilSafe(t *testing.T) {
	var indicator *typingIndicator
	indicator.pause()
	indicator.resume()
	indicator.stop()
	if typingIndicatorFromContext(withTypingIndicator(context.Background(), nil)) != nil {
		t.Fatal("nil indicator must not be stored in the context")
	}
	real := &typingIndicator{kick: make(chan struct{}, 1), done: make(chan struct{})}
	if typingIndicatorFromContext(withTypingIndicator(context.Background(), real)) != real {
		t.Fatal("indicator must round-trip through the context")
	}
}

// typingOrderChannel 记录「发消息」和「刷新输入状态」的先后顺序。
type typingOrderChannel struct {
	mu     sync.Mutex
	events []string
	// holdUntilTyping 里的文本发出前，先等上一条发送之后出现一次输入状态刷新。
	// resume 只是给看护协程发个信号，真正的刷新在另一个协程里异步发生；两条之间
	// 只隔 1ms 的话，CI 一卡刷新就落到第二条之后，用例会假报「没有补亮」。
	// 在发送侧等住它，断言的仍是「补亮发生在两条之间」，只是不再和调度赛跑。
	holdUntilTyping map[string]bool
}

func (*typingOrderChannel) Connect(context.Context, EventHandler) error { return nil }
func (*typingOrderChannel) Close() error                                { return nil }
func (*typingOrderChannel) Status() ChannelStatus                       { return ChannelStatus{} }
func (*typingOrderChannel) CallAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, nil
}

func (c *typingOrderChannel) Send(_ context.Context, msg OutgoingMessage) error {
	if c.holdUntilTyping[msg.Text] {
		deadline := time.Now().Add(2 * time.Second)
		for !c.typingSinceLastSend() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	c.record("send:" + msg.Text)
	return nil
}

// typingSinceLastSend 报告最近一次发送之后有没有刷新过输入状态。
func (c *typingOrderChannel) typingSinceLastSend() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := len(c.events) - 1; index >= 0; index-- {
		switch {
		case c.events[index] == "typing":
			return true
		case strings.HasPrefix(c.events[index], "send:"):
			return false
		}
	}
	return false
}

func (c *typingOrderChannel) SendChatAction(context.Context, OutgoingMessage, string) error {
	c.record("typing")
	return nil
}

func (c *typingOrderChannel) record(event string) {
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
}

func (c *typingOrderChannel) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.events...)
}

// 多条发送之间要立刻补上输入状态，最后一条发完之后不能再点：中间空着看上去就是
// 「正在输入」断了，而末尾多刷一次会让状态在回复发完后继续闪。
func TestDeliverChunksKeepsTypingUntilLastChunk(t *testing.T) {
	withFastSendTiming(t)
	channel := &typingOrderChannel{holdUntilTyping: map[string]bool{"B": true}}
	cfg := BotConfig{SendChunkIntervalMS: 1}
	runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, Platform: PlatformOneBotV11, UserID: "42"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 间隔远长于这个用例的发送节奏：中间那次点亮只能来自发送链路自己的 resume，
	// 末尾的等待也长于一个间隔，没静音的话就会再刷出来。
	interval := 200 * time.Millisecond
	indicator := newTestTypingIndicator(ctx, channel, interval, oneBotTypingMaxFailures)
	defer indicator.stop()
	waitForEvents(t, channel, 1)

	if _, err := runtime.deliverChunks(withTypingIndicator(ctx, indicator), event, []string{"A", "B"}, cfg.WithDefaults(), outboundDecoration{}); err != nil {
		t.Fatalf("deliverChunks() error = %v", err)
	}
	waitForEvents(t, channel, 4)
	time.Sleep(3 * interval)

	events := channel.snapshot()
	first, last := indexOfEvent(t, events, "send:A"), indexOfEvent(t, events, "send:B")
	if last < first {
		t.Fatalf("chunks sent out of order: %v", events)
	}
	betweenChunks := 0
	for _, event := range events[first:last] {
		if event == "typing" {
			betweenChunks++
		}
	}
	if betweenChunks == 0 {
		t.Fatalf("typing was not relit between chunks: %v", events)
	}
	for _, event := range events[last:] {
		if event == "typing" {
			t.Fatalf("typing kept refreshing after the last chunk: %v", events)
		}
	}
}

func indexOfEvent(t *testing.T, events []string, want string) int {
	t.Helper()
	for index, event := range events {
		if event == want {
			return index
		}
	}
	t.Fatalf("%q missing from %v", want, events)
	return -1
}

func waitForEvents(t *testing.T, channel *typingOrderChannel, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(channel.snapshot()) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("expected at least %d events, got %v", want, channel.snapshot())
}
