// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// channelWatchInterval 是逐条连接查一次状态的间隔。状态本身已经在内存里，查一遍
// 只是读几个字段；间隔短一点，断线日志的时间才对得上群里「机器人没反应」的那一刻。
const channelWatchInterval = 5 * time.Second

// channelErrorLogInterval 是同一条连接两次错误日志之间的最短间隔。连不上的时候
// 通道会几秒重试一次，每次的错误文本还可能带着不同的地址或时间，按「文本变了才记」
// 仍会刷屏，所以再加一道按连接的节流。
const channelErrorLogInterval = time.Minute

// channelWatchState 是观察器上一次看到的一条连接的样子。
type channelWatchState struct {
	connected    bool
	accountDown  bool
	lastError    string
	lastErrorLog time.Time
}

// runChannelWatch 把每条连接的上线、掉线、账号异常和连接错误写进运行日志。
//
// 入站协调器里那套断线回补状态机只盯第一个 OneBot 连接（回补本身只对它有意义），
// 它记下的连接日志也就只有这一条。多开几个 OneBot 账号，或者接了 Telegram、钉钉
// 这些平台，其余连接掉线在界面上一个字都看不到。这里补上其余的连接；有入站队列时
// 第一个 OneBot 连接仍归协调器记，免得同一次掉线出现两条。
func (r *Runtime) runChannelWatch(ctx context.Context) {
	ticker := time.NewTicker(channelWatchInterval)
	defer ticker.Stop()
	states := map[string]*channelWatchState{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		r.observeChannels(ctx, states, time.Now())
	}
}

func (r *Runtime) observeChannels(ctx context.Context, states map[string]*channelWatchState, now time.Time) {
	statuses := r.connectionStatuses()
	skip := r.recoveryLoggedChannel()
	for _, status := range statuses {
		key := channelWatchKey(status)
		state, seen := states[key]
		if !seen {
			state = &channelWatchState{}
			states[key] = state
		}
		if key != skip {
			r.observeChannelConnection(ctx, status, state, seen)
		}
		r.observeChannelError(ctx, status, state, now)
	}
}

// observeChannelConnection 记连接和账号状态的转折。第一次看到时只记「已经连上」：
// 刚启动时还没连上是正常的，那一刻就报「断开」只会吓人。
func (r *Runtime) observeChannelConnection(ctx context.Context, status ChannelStatus, state *channelWatchState, seen bool) {
	accountDown := channelAccountDown(status)
	switch {
	case status.Connected && (!seen || !state.connected):
		r.recordChannelLog(ctx, status, applog.KindOperation, applog.LevelInfo, "channel_connected", "连接已建立", "")
	case !status.Connected && seen && state.connected:
		r.recordChannelLog(ctx, status, applog.KindError, applog.LevelError, "channel_disconnected", "连接已断开", "")
	}
	if status.Connected && seen && state.connected {
		switch {
		case accountDown && !state.accountDown:
			r.recordChannelLog(ctx, status, applog.KindError, applog.LevelError, "channel_account_offline",
				"账号已离线或状态异常（连接仍在）", status.AccountStatusMessage)
		case !accountDown && state.accountDown:
			r.recordChannelLog(ctx, status, applog.KindOperation, applog.LevelInfo, "channel_account_recovered", "账号已恢复在线", "")
		}
	}
	state.connected = status.Connected
	state.accountDown = accountDown
}

// observeChannelError 记连接上报的最新错误，包括第一个 OneBot 连接：协调器只管
// 连上断开，连接本身报的错（连不上、鉴权失败、接口报错）它不记。
func (r *Runtime) observeChannelError(ctx context.Context, status ChannelStatus, state *channelWatchState, now time.Time) {
	lastError := strings.TrimSpace(status.LastError)
	if lastError == "" || lastError == state.lastError {
		state.lastError = lastError
		return
	}
	if !state.lastErrorLog.IsZero() && now.Sub(state.lastErrorLog) < channelErrorLogInterval {
		return
	}
	state.lastError = lastError
	state.lastErrorLog = now
	r.recordChannelLog(ctx, status, applog.KindError, applog.LevelError, "channel_error", "连接报错", lastError)
}

// connectionStatuses 每条物理连接给一份状态。复用同一条连接的几台机器人共用
// 一份，不然一次掉线会按机器人数记好几遍。
func (r *Runtime) connectionStatuses() []ChannelStatus {
	r.mu.RLock()
	channel := r.channel
	r.mu.RUnlock()
	if channel == nil {
		return nil
	}
	if provider, ok := channel.(interface{ ConnectionStatuses() []ChannelStatus }); ok {
		return provider.ConnectionStatuses()
	}
	return []ChannelStatus{channel.Status()}
}

// recoveryLoggedChannel 返回协调器已经在记连接日志的那条连接；没有入站队列时
// 协调器不跑，返回空，所有连接都归观察器记。
func (r *Runtime) recoveryLoggedChannel() string {
	r.mu.RLock()
	store := r.inboundStore
	r.mu.RUnlock()
	if store == nil {
		return ""
	}
	return channelWatchKey(r.channelStatus())
}

func channelWatchKey(status ChannelStatus) string {
	return status.ProfileID + "|" + status.Platform
}

func (r *Runtime) recordChannelLog(ctx context.Context, status ChannelStatus, kind applog.Kind, level applog.Level, action, message, detail string) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	if label := firstNonEmpty(status.Name, status.ProfileID); label != "" {
		message = fmt.Sprintf("%s：%s", label, message)
	}
	metadata := map[string]any{}
	if status.ProfileID != "" {
		metadata["profile_id"] = status.ProfileID
	}
	if status.Platform != "" {
		metadata["platform"] = status.Platform
	}
	if status.SelfID != "" {
		metadata["self_id"] = status.SelfID
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:      kind,
		Level:     level,
		Action:    action,
		Message:   message,
		Detail:    detail,
		Target:    status.ProfileID,
		Metadata:  metadata,
		CreatedAt: time.Now(),
	})
}
