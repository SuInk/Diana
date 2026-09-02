// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 用量记账挂在 provider 装饰链上，而不是逐个调用点手写。
//
// 一条消息真正花掉的 token 分散在很多次调用里：主动回复路由、回复规则路由、
// 记忆抽取、图片描述、子任务（subagent）、最终生成。以前只有其中几处显式调了
// recordLLMUsage，事件详情里看到的用量就少算一大截，而且每加一条新的模型调用
// 路径都得记得补一行——漏了也没人发现。改成装饰器之后，凡是经过 Runtime 取到
// 的 provider，调用一次就记一次，新增调用路径不需要额外动作。

type llmUsageContextKey struct{}

type llmUsagePurposeKey struct{}

type llmUsageState struct {
	event MessageEvent
}

// withLLMUsageContext 记下这一轮的消息事件，供装饰器把用量归到这条消息名下。
// 和 debug trace 不同，它不受调试开关影响：用量统计任何时候都要准。
func withLLMUsageContext(ctx context.Context, event MessageEvent) context.Context {
	if ctx == nil || strings.TrimSpace(event.MessageID) == "" {
		return ctx
	}
	return context.WithValue(ctx, llmUsageContextKey{}, &llmUsageState{event: event})
}

func llmUsageFromContext(ctx context.Context) *llmUsageState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(llmUsageContextKey{}).(*llmUsageState)
	return state
}

// withLLMUsagePurpose 给接下来的模型调用打上用途标签。调用点知道自己在干什么，
// 比事后从请求内容猜要准。没打标签时退回按请求形状推断。
func withLLMUsagePurpose(ctx context.Context, purpose string) context.Context {
	purpose = strings.TrimSpace(purpose)
	if ctx == nil || purpose == "" {
		return ctx
	}
	return context.WithValue(ctx, llmUsagePurposeKey{}, purpose)
}

func llmUsagePurposeFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	purpose, _ := ctx.Value(llmUsagePurposeKey{}).(string)
	return purpose
}

// withLLMUsageAccountingRun 把用量记账装饰器接进 provider 链。
func (r *Runtime) withLLMUsageAccountingRun(ctx context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	state := llmUsageFromContext(ctx)
	if state == nil {
		return run
	}
	return func(provider LLMProvider) (string, error) {
		return run(&usageAccountingLLMProvider{runtime: r, state: state, provider: provider})
	}
}

type usageAccountingLLMProvider struct {
	runtime  *Runtime
	state    *llmUsageState
	provider LLMProvider
}

func (p *usageAccountingLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	// 墙钟时间在这里量而不是在各个调用点：装饰器已经包住了每一次调用，量的范围
	// 和记账的范围天然一致。放到调用点去量，新增一条调用路径就会漏一次。
	//
	// TTFT 由更内层的流式装饰器填进 collector：它才看得到 token 一个个到达。
	// 没开流式、或者底层退化成非流式时，collector 给 0，这一项就不记。
	ctx, collector := withTTFTCollector(ctx)
	started := time.Now()
	response, err := p.provider.Generate(ctx, req)
	elapsed := time.Since(started)
	ttft := collector.ttft(started)
	if response == nil {
		return response, err
	}
	purpose := llmUsagePurposeFromContext(ctx)
	if purpose == "" {
		purpose = debugModelPurpose(req)
	}
	p.runtime.recordLLMUsage(ctx, p.state.event, response.Provider, response.Model, response.Usage, purpose, elapsed, ttft)
	return response, err
}

// TokensPerSecond 是输出 token 的生成速率，保留两位小数。
//
// 用输出 token 而不是总 token：输入是一次性喂进去的，把它算进速率会让长上下文的
// 调用看起来「很快」，而那恰恰是慢的那一类。
//
// 聚合时必须先把 token 和耗时分别加总再算，不能把每次调用的速率平均——一次 2000
// token 的生成和一次 5 token 的分类，两个速率平均出来没有任何物理意义。
func TokensPerSecond(outputTokens int64, duration time.Duration) float64 {
	if outputTokens <= 0 || duration <= 0 {
		return 0
	}
	return math.Round(float64(outputTokens)/duration.Seconds()*100) / 100
}
