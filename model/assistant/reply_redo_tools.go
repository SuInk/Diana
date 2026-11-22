// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"sync"

	"github.com/SuInk/diana/model/agent"
)

// 同一轮因为补充消息整轮重来时（见 replyAndRecordTurn 和 errDirectReplySupplemented），
// 上一版 Agent 执行过的工具原样交给下一版，下一版接着答、不用重查，见 agent.ExecutedTools。
// 只在一轮之内传：跨轮的事交给续接和历史，不走这里。

type replyRedoToolsContextKey struct{}

type replyRedoTools struct {
	mu       sync.Mutex
	executed *agent.ExecutedTools
}

func withReplyRedoTools(ctx context.Context) context.Context {
	return context.WithValue(ctx, replyRedoToolsContextKey{}, &replyRedoTools{})
}

func replyRedoToolsFromContext(ctx context.Context) *replyRedoTools {
	redo, _ := ctx.Value(replyRedoToolsContextKey{}).(*replyRedoTools)
	return redo
}

// previous 返回上一版执行过的工具；这是这一轮的第一版、或上一版一个工具都没调时为 nil。
func (redo *replyRedoTools) previous() *agent.ExecutedTools {
	if redo == nil {
		return nil
	}
	redo.mu.Lock()
	defer redo.mu.Unlock()
	return redo.executed
}

// remember 记下这一版执行过的工具，供这一轮再重来时用。
func (redo *replyRedoTools) remember(executed agent.ExecutedTools) {
	if redo == nil {
		return
	}
	redo.mu.Lock()
	defer redo.mu.Unlock()
	if len(executed.Messages) == 0 {
		redo.executed = nil
		return
	}
	redo.executed = &executed
}
