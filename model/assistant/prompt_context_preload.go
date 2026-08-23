// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"sync"
)

// promptContextPreload 是提示词三个只读上下文层的并发预取结果。
//
// 会话线程便签、长期记忆检索、词典命中和窗口外媒体索引都要各自查一次存储层（每次各带 2 秒
// 超时），彼此没有依赖，却一直是串行执行的。它们全部发生在 event 被改写完之后，
// 所以并发是安全的：每条路径都只读，不碰 event。
//
// 预取在这里启动、在组装提示词时收取，中间还夹着建 Agent 工具表、意图路由等真正
// 的工作，等于把这几次查询的耗时藏进那段时间里。
type promptContextPreload struct {
	wg sync.WaitGroup

	sessionThread   string
	memoryContext   string
	glossaryContext string
	mediaIndex      string
}

// startPromptContextPreload 并发预取几层只读上下文。调用方必须在使用结果前调用
// wait，并且只能在 event 不会再被改写之后调用本函数。
func (r *Runtime) startPromptContextPreload(
	ctx context.Context,
	event MessageEvent,
	queryText string,
	profile UserMemoryProfile,
	policy RelationshipPolicy,
	wantMediaIndex bool,
) *promptContextPreload {
	preload := &promptContextPreload{}

	preload.wg.Add(3)
	go func() {
		defer preload.wg.Done()
		preload.sessionThread = r.sessionThreadNote(ctx, event)
	}()
	go func() {
		defer preload.wg.Done()
		preload.memoryContext = r.memoryContextWithProfile(ctx, event, queryText, profile, policy)
	}()
	go func() {
		defer preload.wg.Done()
		preload.glossaryContext = r.glossaryContext(ctx, event, queryText)
	}()
	if wantMediaIndex {
		preload.wg.Add(1)
		go func() {
			defer preload.wg.Done()
			preload.mediaIndex = r.durableMediaIndex(ctx, event)
		}()
	}
	return preload
}

func (p *promptContextPreload) wait() {
	if p == nil {
		return
	}
	p.wg.Wait()
}
