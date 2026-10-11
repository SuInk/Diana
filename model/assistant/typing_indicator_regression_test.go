package assistant

import (
	"context"
	"errors"
	"testing"
	"time"
)

type controlledTypingChannel struct {
	started chan context.Context
	release chan struct{}
}

func (c *controlledTypingChannel) SendChatAction(ctx context.Context, _ OutgoingMessage, _ string) error {
	c.started <- ctx
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.release:
		return nil
	}
}
func nextTypingCall(t *testing.T, c *controlledTypingChannel) context.Context {
	t.Helper()
	select {
	case ctx := <-c.started:
		return ctx
	case <-time.After(2 * time.Second):
		t.Fatal("typing did not start")
		return nil
	}
}
func TestTypingIndicatorCancelsInFlight(t *testing.T) {
	for _, stop := range []bool{false, true} {
		name := "pause"
		if stop {
			name = "stop"
		}
		t.Run(name, func(t *testing.T) {
			c := &controlledTypingChannel{started: make(chan context.Context, 10), release: make(chan struct{})}
			indicator := &typingIndicator{channel: c, interval: time.Second, callTimeout: time.Minute, kick: make(chan struct{}, 1), done: make(chan struct{})}
			go indicator.run(context.Background())
			defer indicator.stop()
			callCtx := nextTypingCall(t, c)
			if stop {
				indicator.stop()
			} else {
				indicator.pause()
			}
			select {
			case <-callCtx.Done():
			case <-time.After(200 * time.Millisecond):
				t.Fatal("in-flight typing was not cancelled")
			}
			indicator.resume()
			if !stop {
				nextTypingCall(t, c)
				return
			}
			select {
			case <-c.started:
				t.Fatal("stopped indicator resumed")
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}
func TestTypingIndicatorSlowCallDoesNotAddFullInterval(t *testing.T) {
	c := &controlledTypingChannel{started: make(chan context.Context, 10), release: make(chan struct{})}
	indicator := &typingIndicator{channel: c, interval: 400 * time.Millisecond, callTimeout: time.Second, kick: make(chan struct{}, 1), done: make(chan struct{})}
	go indicator.run(context.Background())
	defer indicator.stop()
	nextTypingCall(t, c)
	time.Sleep(300 * time.Millisecond)
	c.release <- struct{}{}
	select {
	case <-c.started:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("refresh added a full interval after the slow call")
	}
}

type scriptedTypingChannel struct {
	started chan context.Context
	result  chan error
}

func (c *scriptedTypingChannel) SendChatAction(ctx context.Context, _ OutgoingMessage, _ string) error {
	c.started <- ctx
	// 模拟不能立即响应取消的底层写入，验证 worker 不会堆积请求。
	return <-c.result
}

func TestTypingIndicatorCooldownAndRecovery(t *testing.T) {
	c := &scriptedTypingChannel{started: make(chan context.Context, 10), result: make(chan error, 10)}
	indicator := &typingIndicator{channel: c, interval: 20 * time.Millisecond, maxFailures: 3, retryInterval: 200 * time.Millisecond, kick: make(chan struct{}, 1), done: make(chan struct{})}
	go indicator.run(context.Background())
	defer func() { indicator.stop(); c.result <- nil }()
	next := func() {
		t.Helper()
		select {
		case <-c.started:
		case <-time.After(2 * time.Second):
			t.Fatal("typing did not recover")
		}
	}
	// 超时不算成功，不能清掉前面的普通失败计数。
	for _, err := range []error{errors.New("temporary"), context.DeadlineExceeded, errors.New("temporary"), errors.New("temporary")} {
		next()
		c.result <- err
	}
	deadline := time.Now().Add(time.Second)
	for {
		indicator.mu.Lock()
		cooldown := indicator.cooldown
		indicator.mu.Unlock()
		if !cooldown.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("repeated failures did not enter cooldown")
		}
		time.Sleep(time.Millisecond)
	}
	for range 10 {
		indicator.pause()
		indicator.resume()
	}
	select {
	case <-c.started:
		t.Fatal("resume bypassed failure cooldown")
	case <-time.After(80 * time.Millisecond):
	}
	next()
	c.result <- nil
	next()
	indicator.mu.Lock()
	cooldown := indicator.cooldown
	indicator.mu.Unlock()
	if !cooldown.IsZero() {
		t.Fatal("successful retry did not clear cooldown")
	}
}

func TestTypingIndicatorSerialAndStaleResult(t *testing.T) {
	c := &scriptedTypingChannel{started: make(chan context.Context, 10), result: make(chan error, 10)}
	indicator := &typingIndicator{channel: c, interval: 200 * time.Millisecond, callTimeout: 20 * time.Millisecond, maxFailures: 1, retryInterval: time.Hour, kick: make(chan struct{}, 1), done: make(chan struct{})}
	go indicator.run(context.Background())
	defer func() { indicator.stop(); c.result <- nil }()
	select {
	case <-c.started:
	case <-time.After(time.Second):
		t.Fatal("typing did not start")
	}
	indicator.pause()
	indicator.resume()
	for range 10 {
		indicator.resume()
	}
	select {
	case <-c.started:
		t.Fatal("started concurrent typing request")
	case <-time.After(80 * time.Millisecond):
	}
	c.result <- errors.New("stale failure")
	select {
	case <-c.started:
	case <-time.After(time.Second):
		t.Fatal("stale failure put the resumed indicator into cooldown")
	}
	c.result <- nil
	select {
	case <-c.started:
		t.Fatal("queued kicks caused burst refreshes")
	case <-time.After(80 * time.Millisecond):
	}
}

func TestTypingIndicatorNilShadowsParent(t *testing.T) {
	parent := withTypingIndicator(context.Background(), &typingIndicator{})
	if typingIndicatorFromContext(withTypingIndicator(parent, nil)) != nil {
		t.Fatal("nil must shadow an inherited indicator")
	}
}

func TestTypingIndicatorMutedStartWaitsForResume(t *testing.T) {
	c := &controlledTypingChannel{started: make(chan context.Context, 10), release: make(chan struct{})}
	indicator := &typingIndicator{channel: c, interval: time.Second, callTimeout: time.Minute, muted: true, kick: make(chan struct{}, 1), done: make(chan struct{})}
	go indicator.run(context.Background())
	defer indicator.stop()
	select {
	case <-c.started:
		t.Fatal("muted indicator lit up before resume")
	case <-time.After(100 * time.Millisecond):
	}
	indicator.resume()
	nextTypingCall(t, c)
}
