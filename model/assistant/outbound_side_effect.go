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
	// onMark 在第一次打标记时调用一次。回复入口用它把「写过外部系统」立刻同步给
	// 连发交接的登记（见 sender_burst.go），不必等到这一轮去发送时才知道。
	onMark func()
}

// onExternalSideEffect 给这一轮的账本挂上第一次打标记时的回调。账本是外层传进来
// 的（已经有回调）就不覆盖。回调要在账本交给任何工具之前挂好。
func onExternalSideEffect(ctx context.Context, fn func()) {
	if ctx == nil || fn == nil {
		return
	}
	if ledger, ok := ctx.Value(externalSideEffectContextKey{}).(*externalSideEffectLedger); ok && ledger.onMark == nil {
		ledger.onMark = fn
	}
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
		if !ledger.marked.Swap(true) && ledger.onMark != nil {
			ledger.onMark()
		}
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
