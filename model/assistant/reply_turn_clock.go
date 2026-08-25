// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"time"
)

// 本轮回复的起点。用它算「从收到消息到现在过了多久」，而不是用消息自带的
// 时间戳——后者来自平台时钟，和机器人所在机器有偏差时会把停顿算错。

type replyTurnStartContextKey struct{}

func withReplyTurnStart(ctx context.Context, at time.Time) context.Context {
	if ctx == nil || at.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, replyTurnStartContextKey{}, at)
}

// replyTurnStartFromContext 返回本轮回复的起点。第二个返回值为 false 时表示这一次
// 发送不在回复轮次里（通知、后台任务），调用方不能拿零值当时间用。
func replyTurnStartFromContext(ctx context.Context) (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	at, ok := ctx.Value(replyTurnStartContextKey{}).(time.Time)
	if !ok || at.IsZero() {
		return time.Time{}, false
	}
	return at, true
}

// replyTurnElapsed 返回本轮已经花掉的时间；拿不到起点时返回 0。
func replyTurnElapsed(ctx context.Context) time.Duration {
	if ctx == nil {
		return 0
	}
	at, ok := ctx.Value(replyTurnStartContextKey{}).(time.Time)
	if !ok || at.IsZero() {
		return 0
	}
	if elapsed := time.Since(at); elapsed > 0 {
		return elapsed
	}
	return 0
}
