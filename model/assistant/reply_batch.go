// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"sync"
)

// replyBatchGate 让同一会话的一批气泡连续发完。生成任务仍可并发；只有投递阶段
// 串行，避免两份回复在群里排成 A1、B1、A2。refs 由 Runtime.replyBatchMu 保护。
type replyBatchGate struct {
	mu   sync.Mutex
	refs int
}

func (r *Runtime) lockReplyBatch(event MessageEvent) func() {
	if r == nil || (strings.TrimSpace(event.GroupID) == "" && strings.TrimSpace(event.UserID) == "") {
		return func() {}
	}
	key := strings.TrimSpace(sessionKey(event))
	if key == "" {
		return func() {}
	}

	r.replyBatchMu.Lock()
	if r.replyBatches == nil {
		r.replyBatches = make(map[string]*replyBatchGate)
	}
	gate := r.replyBatches[key]
	if gate == nil {
		gate = &replyBatchGate{}
		r.replyBatches[key] = gate
	}
	gate.refs++
	r.replyBatchMu.Unlock()

	gate.mu.Lock()
	return func() {
		gate.mu.Unlock()
		r.replyBatchMu.Lock()
		gate.refs--
		if gate.refs == 0 && r.replyBatches[key] == gate {
			delete(r.replyBatches, key)
		}
		r.replyBatchMu.Unlock()
	}
}
