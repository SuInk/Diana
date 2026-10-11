// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// Telegram 的 chat action 约 5 秒过期；QQ 未公开时长，缩短续刷间隔以减少显示空隙。
	telegramTypingRenewInterval = 4 * time.Second
	oneBotTypingRenewInterval   = 500 * time.Millisecond
	// 单次刷新的超时。状态刷新和消息发送、取历史挤在同一条连接上，没有上限时
	// 一次慢调用就能把刷新拖过平台的过期窗口，界面上就是「正在输入」一闪一闪。
	typingCallTimeout = 2 * time.Second
	// 连续失败多少次才认定这个接入端没有 set_input_status。只失败一次就收手的话，
	// 接入端偶发的 uid 查不到、重连瞬间的调用失败，都会让整轮输入状态提前停掉：
	// 用户看到的就是「正在输入」没了、回复却还没出来。
	oneBotTypingMaxFailures = 3
	// 连续失败达到上限后进入冷却，冷却结束只给一次试探机会。
	typingFailureCooldown = 15 * time.Second
)

type typingIndicatorContextKey struct{}

// typingIndicator 覆盖准备回复和分条等待，实际发送时暂停，后处理不再点亮。
// 零值不可用；nil 可以安全调用所有方法。
type typingIndicator struct {
	channel     ChatActionChannel
	message     OutgoingMessage
	interval    time.Duration
	callTimeout time.Duration
	// 连续错误达到阈值后低频重试，不把临时故障误认为永久不支持。
	maxFailures      int
	retryInterval    time.Duration
	cooldownInterval time.Duration

	mu           sync.Mutex
	muted        bool
	stopped      bool
	generation   uint64
	activeCancel context.CancelFunc
	next         time.Time
	cooldown     time.Time
	kick         chan struct{}
	done         chan struct{}
}

// startTypingIndicator 在准备回复期间持续显示「正在输入」：Telegram 群聊私聊都支持，
// OneBot 只支持私聊且可在机器人设置里关闭。
func (r *Runtime) startTypingIndicator(ctx context.Context, event MessageEvent, cfg BotConfig) *typingIndicator {
	return r.startTypingIndicatorState(ctx, event, cfg, false)
}

// startTypingIndicatorMuted 启动但不点亮，由调用方在确定要发内容时 resume。
func (r *Runtime) startTypingIndicatorMuted(ctx context.Context, event MessageEvent, cfg BotConfig) *typingIndicator {
	return r.startTypingIndicatorState(ctx, event, cfg, true)
}

func (r *Runtime) startTypingIndicatorState(ctx context.Context, event MessageEvent, cfg BotConfig, muted bool) *typingIndicator {
	platform := NormalizePlatformID(event.Platform)
	interval := telegramTypingRenewInterval
	maxFailures := 0
	switch platform {
	case PlatformTelegram:
	case PlatformOneBotV11:
		if event.Kind != EventKindPrivate || !boolValue(cfg.QQTypingEnabled, true) {
			return nil
		}
		interval = oneBotTypingRenewInterval
		maxFailures = oneBotTypingMaxFailures
	default:
		return nil
	}
	r.mu.RLock()
	channel, ok := r.channel.(ChatActionChannel)
	r.mu.RUnlock()
	if !ok {
		return nil
	}
	indicator := &typingIndicator{
		channel:       channel,
		message:       routeOutgoingToEvent(event, OutgoingMessage{GroupID: event.GroupID, UserID: event.UserID, MessageThreadID: event.MessageThreadID}),
		interval:      interval,
		callTimeout:   typingCallTimeout,
		maxFailures:   maxFailures,
		retryInterval: 2500 * time.Millisecond,
		muted:         muted,
		kick:          make(chan struct{}, 1),
		done:          make(chan struct{}),
	}
	go func() {
		defer recoverGoroutinePanic("typing_indicator.go:startTypingIndicator")
		indicator.run(ctx)
	}()
	return indicator
}

func (t *typingIndicator) run(ctx context.Context) {
	defer t.stop()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	failures := 0
	for {
		t.mu.Lock()
		if t.stopped || ctx.Err() != nil {
			t.mu.Unlock()
			return
		}
		next := t.next
		if t.cooldown.After(next) {
			next = t.cooldown
		}
		wait := time.Until(next)
		if t.muted || wait > 0 {
			muted := t.muted
			t.mu.Unlock()
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			var tick <-chan time.Time
			if !muted {
				timer.Reset(wait)
				tick = timer.C
			}
			select {
			case <-ctx.Done():
				return
			case <-t.done:
				return
			case <-t.kick:
			case <-tick:
			}
			// 唤醒只是通知；旧 kick 不得绕过暂停、停止或错误冷却。
			continue
		}
		timeout := t.callTimeout
		if timeout <= 0 {
			timeout = typingCallTimeout
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		generation := t.generation
		t.activeCancel = cancel
		started := time.Now()
		t.mu.Unlock()

		// 单 worker 同步调用，不为超时启动可能堆积的请求 goroutine。
		err := t.channel.SendChatAction(callCtx, t.message, "typing")
		cancel()
		t.mu.Lock()
		t.activeCancel = nil
		if generation == t.generation && !t.stopped && !t.muted {
			// 从开始时间算周期，慢调用不再额外叠加完整间隔，也不追补旧 tick。
			t.next = started.Add(t.interval)
			switch {
			case err == nil:
				failures = 0
				t.cooldown = time.Time{}
			case errors.Is(err, context.DeadlineExceeded):
				// 忙连接不代表不支持，也不代表成功，保留之前的失败计数。
			default:
				failures++
				t.next = time.Now().Add(t.retryDelay(typingRefreshFailed))
			}
			if t.maxFailures > 0 && failures >= t.maxFailures {
				retry := t.cooldownInterval
				if retry <= 0 {
					retry = typingFailureCooldown
				}
				t.cooldown = time.Now().Add(max(t.interval, retry))
				// 冷却后只给一次试探机会：真实错误立即再冷却，
				// 超时不计数，避免忙连接让冷却连环触发。
				failures = t.maxFailures - 1
			}
		}
		t.mu.Unlock()
	}
}

type typingRefreshResult int

const (
	typingRefreshOK typingRefreshResult = iota
	typingRefreshFailed
)

func (t *typingIndicator) retryDelay(result typingRefreshResult) time.Duration {
	if result == typingRefreshFailed && t.maxFailures > 0 {
		return max(t.interval, t.retryInterval)
	}
	return t.interval
}

// pause 与刷新注册共用锁，取消尚未完成的请求；已送达平台的状态无法撤回。
func (t *typingIndicator) pause() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.muted {
		return
	}
	t.muted = true
	t.generation++
	if t.activeCancel != nil {
		t.activeCancel()
	}
}

func (t *typingIndicator) resume() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || !t.muted {
		return
	}
	t.muted = false
	t.next = time.Time{}
	select {
	case t.kick <- struct{}{}:
	default:
	}
}

func (t *typingIndicator) stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	t.stopped = true
	if t.activeCancel != nil {
		t.activeCancel()
	}
	close(t.done)
}

func withTypingIndicator(ctx context.Context, indicator *typingIndicator) context.Context {
	// typed nil 也要写入，遮蔽独立跟评从父 context 继承的旧会话。
	return context.WithValue(ctx, typingIndicatorContextKey{}, indicator)
}

func typingIndicatorFromContext(ctx context.Context) *typingIndicator {
	if ctx == nil {
		return nil
	}
	indicator, _ := ctx.Value(typingIndicatorContextKey{}).(*typingIndicator)
	return indicator
}
