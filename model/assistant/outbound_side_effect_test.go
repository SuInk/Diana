// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"testing"
)

// 追发消息会打断还没送出的旧回复。但这一轮如果已经在外部系统留下不可撤销的
// 痕迹（比如 GitHub 上已经建好了 Issue），丢掉回复就等于把「已经做完了」咽
// 回去：用户看不到成功，随后那一轮又因为草稿已被消费而报告失败，最后呈现为
// 「创建成功却提示失败」。
func TestInterruptedReplySkipsSupersedeAfterExternalSideEffect(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "u1", MessageID: "m1"}
	// 同一个人随后又发来一条，旧回复本应被打断。
	runtime.noteDirectedInbound(event)
	runtime.noteDirectedInbound(MessageEvent{Kind: EventKindPrivate, UserID: "u1", MessageID: "m2"})

	ctx := withExternalSideEffectLedger(withReplyTriggerGate(context.Background()))
	if err := runtime.interruptedReplyError(ctx, event); !errors.Is(err, errReplyTriggerSuperseded) {
		t.Fatalf("没有外部副作用时应当照常打断，实际 err=%v", err)
	}

	markExternalSideEffect(ctx)
	if err := runtime.interruptedReplyError(ctx, event); err != nil {
		t.Fatalf("已经写到外部系统的这一轮不该被丢弃，实际 err=%v", err)
	}
}

func TestExternalSideEffectLedgerIsScopedToTheTurn(t *testing.T) {
	// 没挂账本的上下文里标记不该 panic，也不该影响别的轮次。
	markExternalSideEffect(context.Background())
	if hasExternalSideEffect(context.Background()) {
		t.Fatal("没有账本的上下文不应报告副作用")
	}
	first := withExternalSideEffectLedger(context.Background())
	second := withExternalSideEffectLedger(context.Background())
	markExternalSideEffect(first)
	if !hasExternalSideEffect(first) {
		t.Fatal("标记没有生效")
	}
	if hasExternalSideEffect(second) {
		t.Fatal("副作用标记串到了另一轮")
	}
	// 派生上下文共享同一本账本：工具拿到的是子上下文，标记必须能被投递前看到。
	child := context.WithValue(first, struct{}{}, "x")
	if !hasExternalSideEffect(child) {
		t.Fatal("派生上下文没有共享账本")
	}
}
