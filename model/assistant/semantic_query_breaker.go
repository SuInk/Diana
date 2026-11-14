// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 查询向量的熔断。
//
// 语义检索排在回复的关键路径上：跨群参考、历史检索都要先把查询文本转成向量，限时
// semanticQueryTimeout。embedding 服务连不上时这一步不会快速失败，而是每次都等满
// 超时——生产上 embedding 主机下线三天，开了语义检索的群每条回复都在调模型之前白等
// 3 秒多，占一条短回复总耗时的三分之一。
//
// 连着失败 semanticQueryBreakerThreshold 次就认定服务不可用，接下来
// semanticQueryBreakerCooldown 内直接返回错误，调用方照旧只用词面结果。冷却过后放一次
// 请求去探，成功就恢复，失败再冷却一轮。后台建索引不走这里：它不在关键路径上，等得起。
const (
	semanticQueryBreakerThreshold = 2
	semanticQueryBreakerCooldown  = time.Minute
)

var errSemanticQueryBreakerOpen = errors.New("embedding 服务连续失败，暂时跳过语义检索")

type semanticQueryBreakerState struct {
	failures  int
	openUntil time.Time
	probing   bool
}

// semanticQueryBreaker 按 embedding 服务地址和模型分别计数，零值可用。
// maxCooldown 大于零时冷却按失败次数翻倍（1m、2m、4m……封顶 maxCooldown），
// 给后台向量索引用：embedding 主机长期下线时不必每分钟都等满一次超时。
type semanticQueryBreaker struct {
	mu          sync.Mutex
	states      map[string]*semanticQueryBreakerState
	maxCooldown time.Duration
}

func semanticBreakerKey(cfg llm.ProviderConfig) string {
	return string(cfg.Provider) + "\x00" + cfg.BaseURL + "\x00" + cfg.Model
}

// isCoolingDown 报告是否处在冷却或探测中，不占探测名额。
func (b *semanticQueryBreaker) isCoolingDown(key string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.states[key]
	return state != nil && state.failures >= semanticQueryBreakerThreshold && (now.Before(state.openUntil) || state.probing)
}

// allow 报告这次能不能去请求。冷却结束后只放一个请求去探，其余的继续跳过。
func (b *semanticQueryBreaker) allow(key string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.states[key]
	if state == nil || state.failures < semanticQueryBreakerThreshold {
		return true
	}
	if now.Before(state.openUntil) || state.probing {
		return false
	}
	state.probing = true
	return true
}

func (b *semanticQueryBreaker) record(key string, ok bool, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		delete(b.states, key)
		return
	}
	if b.states == nil {
		b.states = make(map[string]*semanticQueryBreakerState)
	}
	state := b.states[key]
	if state == nil {
		state = &semanticQueryBreakerState{}
		b.states[key] = state
	}
	state.failures++
	state.probing = false
	if state.failures >= semanticQueryBreakerThreshold {
		wait := semanticQueryBreakerCooldown
		for step := semanticQueryBreakerThreshold; b.maxCooldown > 0 && step < state.failures && wait < b.maxCooldown; step++ {
			wait = min(wait*2, b.maxCooldown)
		}
		state.openUntil = now.Add(wait)
	}
}

// embedQueryText 把一段查询文本转成向量，带熔断。调用方取消（整轮回复被打断）不算
// 服务失败。
func (r *Runtime) embedQueryText(ctx context.Context, cfg llm.ProviderConfig, query string) ([]float32, error) {
	key := semanticBreakerKey(cfg)
	if !r.semanticQueryBreaker.allow(key, time.Now()) {
		return nil, errSemanticQueryBreakerOpen
	}
	vectors, err := r.embedTextsFunc()(ctx, cfg, []string{query})
	if err == nil && (len(vectors) != 1 || len(vectors[0]) == 0) {
		err = errors.New("embedding 返回了空向量")
	}
	if err != nil && errors.Is(ctx.Err(), context.Canceled) {
		// 没探出结果：把探测名额还回去，不计失败。
		r.semanticQueryBreaker.mu.Lock()
		if state := r.semanticQueryBreaker.states[key]; state != nil {
			state.probing = false
		}
		r.semanticQueryBreaker.mu.Unlock()
		return nil, err
	}
	r.semanticQueryBreaker.record(key, err == nil, time.Now())
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}
