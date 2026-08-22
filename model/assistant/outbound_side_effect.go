// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"sync/atomic"
)

// 同一个人连着追问时，运行时会丢掉还没送出的旧回复，让新一轮一并作答
// （见 reply_interrupt.go）。这对纯聊天是对的，但一旦这一轮已经在外部系统里
// 留下了不可撤销的痕迹——比如已经在 GitHub 上建好了 Issue——丢掉回复就等于
// 把「已经做完了」这件事咽了回去：用户看不到成功，随后那一轮又因为草稿已被
// 消费而报告失败，最后呈现为「创建成功却提示失败」。
//
// 所以这里给每一轮回复挂一个账本：谁做了外部写入就打个标记，投递前的打断
// 检查看到标记就不再丢弃这条回复。标记只影响「要不要丢」，不改变回复内容。

type externalSideEffectLedger struct {
	marked atomic.Bool
}

type externalSideEffectContextKey struct{}

// withExternalSideEffectLedger 给这一轮回复挂上账本。
func withExternalSideEffectLedger(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	if _, ok := ctx.Value(externalSideEffectContextKey{}).(*externalSideEffectLedger); ok {
		return ctx
	}
	return context.WithValue(ctx, externalSideEffectContextKey{}, &externalSideEffectLedger{})
}

// markExternalSideEffect 记录本轮已经产生了不可撤销的外部副作用。
func markExternalSideEffect(ctx context.Context) {
	if ctx == nil {
		return
	}
	if ledger, ok := ctx.Value(externalSideEffectContextKey{}).(*externalSideEffectLedger); ok {
		ledger.marked.Store(true)
	}
}

// hasExternalSideEffect 报告本轮是否已经产生外部副作用。
func hasExternalSideEffect(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	ledger, ok := ctx.Value(externalSideEffectContextKey{}).(*externalSideEffectLedger)
	return ok && ledger.marked.Load()
}
