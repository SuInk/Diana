// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// 平台不会一直显示「正在输入」，过了自己的窗口就自动清掉，所以要在过期前
	// 反复刷新。刷新间隔按平台窗口取：Telegram 的 chat action 约 5 秒，QQ 没有
	// 公开时长，取更保守的间隔，代价只有几秒一次扩展调用。
	telegramTypingRenewInterval = 4 * time.Second
	oneBotTypingRenewInterval   = 2500 * time.Millisecond
	// 单次刷新的超时。状态刷新和消息发送、取历史挤在同一条连接上，没有上限时
	// 一次慢调用就能把刷新拖过平台的过期窗口，界面上就是「正在输入」一闪一闪。
	typingCallTimeout = 2 * time.Second
	// 连续失败多少次才认定这个接入端没有 set_input_status。只失败一次就收手的话，
	// NapCat 偶发的 uid 查不到、重连瞬间的调用失败，都会让整轮输入状态提前停掉：
	// 用户看到的就是「正在输入」没了、回复却还没出来。
	oneBotTypingMaxFailures = 3
)

type typingIndicatorContextKey struct{}

// typingIndicator 是一轮回复期间的「正在输入」会话：自己按平台窗口刷新，
// 只覆盖「开始准备回复」到「回复发出」这一段——消息发出后就静音，多条回复之间
// 重新点亮，最后一条发完不再点。
//
// 零值不可用，nil 可以安全调用所有方法：平台不支持或开关关掉时直接返回 nil，
// 调用方不必到处判空。
type typingIndicator struct {
	channel     ChatActionChannel
	message     OutgoingMessage
	interval    time.Duration
	callTimeout time.Duration
	// 连续失败上限，0 表示永不放弃。
	maxFailures int

	muted    atomic.Bool
	kick     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// startTypingIndicator 在准备回复期间持续显示「正在输入」：Telegram 群聊私聊都支持，
// OneBot 只支持私聊且可在机器人设置里关闭。
func (r *Runtime) startTypingIndicator(ctx context.Context, event MessageEvent, cfg BotConfig) *typingIndicator {
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
		// 不是所有 OneBot 实现都有 set_input_status，连续失败到上限就不再调用。
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
		channel:     channel,
		message:     routeOutgoingToEvent(event, OutgoingMessage{GroupID: event.GroupID, UserID: event.UserID, MessageThreadID: event.MessageThreadID}),
		interval:    interval,
		callTimeout: typingCallTimeout,
		maxFailures: maxFailures,
		kick:        make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
	go func() {
		defer recoverGoroutinePanic("typing_indicator.go:startTypingIndicator")
		indicator.run(ctx)
	}()
	return indicator
}

func (t *typingIndicator) run(ctx context.Context) {
	// 第一次立刻点亮，之后每次都从「上一次真的发出去」算间隔：用 Ticker 的话
	// 慢调用期间的 tick 会被直接丢掉，刷新节奏跟着漂，窗口一过就断一次。
	timer := time.NewTimer(0)
	defer timer.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.done:
			return
		case <-t.kick:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		if t.muted.Load() {
			timer.Reset(t.interval)
			continue
		}
		switch t.refresh(ctx) {
		case typingRefreshOK:
			failures = 0
		case typingRefreshAborted:
			return
		case typingRefreshFailed:
			failures++
			if t.maxFailures > 0 && failures >= t.maxFailures {
				return
			}
		}
		timer.Reset(t.interval)
	}
}

type typingRefreshResult int

const (
	typingRefreshOK typingRefreshResult = iota
	typingRefreshFailed
	typingRefreshAborted
)

// refresh 刷新一次输入状态。
func (t *typingIndicator) refresh(ctx context.Context) typingRefreshResult {
	timeout := t.callTimeout
	if timeout <= 0 {
		timeout = typingCallTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := t.channel.SendChatAction(callCtx, t.message, "typing")
	if err == nil {
		return typingRefreshOK
	}
	if ctx.Err() != nil {
		return typingRefreshAborted
	}
	// 超时只说明这条连接这一刻很忙，不代表对端没有这个接口，不计入失败次数。
	if errors.Is(err, context.DeadlineExceeded) {
		return typingRefreshOK
	}
	return typingRefreshFailed
}

// pause 在刚发出一条消息后静音。「正在输入」只表示这一轮还在憋回复，消息一发出
// 就该收尾：平台自己会清掉状态，定时刷新再点亮一次，只会让最后一条回复之后继续闪。
func (t *typingIndicator) pause() {
	if t == nil {
		return
	}
	t.muted.Store(true)
}

// resume 在「还有下一条要发」时立刻重新点亮，不等下一个间隔，填住多条回复之间
// 的那段空档。
func (t *typingIndicator) resume() {
	if t == nil {
		return
	}
	t.muted.Store(false)
	select {
	case t.kick <- struct{}{}:
	default:
	}
}

// stop 结束这一轮的输入状态，只在整轮处理收尾时调用。
func (t *typingIndicator) stop() {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() { close(t.done) })
}

func withTypingIndicator(ctx context.Context, indicator *typingIndicator) context.Context {
	if indicator == nil {
		return ctx
	}
	return context.WithValue(ctx, typingIndicatorContextKey{}, indicator)
}

func typingIndicatorFromContext(ctx context.Context) *typingIndicator {
	if ctx == nil {
		return nil
	}
	indicator, _ := ctx.Value(typingIndicatorContextKey{}).(*typingIndicator)
	return indicator
}
