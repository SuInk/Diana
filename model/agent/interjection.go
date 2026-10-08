// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"

	"github.com/SuInk/diana/model/llm"
)

// Interjections 把运行中途用户追加的补充接进同一轮：已经跑完的工具结果留着，
// 只重做当前这一步规划。调用方负责判断哪些消息算补充。
type Interjections interface {
	// Ready 在有新补充可取时可读。
	Ready() <-chan struct{}
	// Take 取出还没接入的补充；没有时返回空。
	Take() []llm.Message
}

var errInterjected = errors.New("agent: planning step interrupted by an interjection")

// generateWithInterjections 跑一步规划；途中来了补充就掐掉这一步，交给调用方带着补充重做。
func generateWithInterjections(ctx context.Context, interjections Interjections, generate func(context.Context) (*llm.GenerateResponse, error)) (*llm.GenerateResponse, bool, error) {
	if interjections == nil {
		resp, err := generate(ctx)
		return resp, false, err
	}
	stepCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		defer recoverGoroutinePanic("interjection_watch")
		select {
		case <-interjections.Ready():
			cancel(errInterjected)
		case <-stop:
		case <-stepCtx.Done():
		}
	}()
	resp, err := generate(stepCtx)
	if err != nil && ctx.Err() == nil && errors.Is(context.Cause(stepCtx), errInterjected) {
		return nil, true, nil
	}
	return resp, false, err
}
