// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 主回复的缓存保活：一个会话最后一次主回复请求之后，每隔 replyKeepaliveInterval
// 把那次请求原样再发一遍（只要几个输出 token），让上游的前缀缓存别过期，直到
// replyKeepaliveWindow 内都没有新的主回复为止。
//
// 为什么要：swe-2 这类经 sub2api 的模型缓存 5 分钟就过期。11-16 生产 swe-2 主回复里，
// 43% 的第一步离同群上一次调用超过 5 分钟，整份请求全部未命中，占 PR 1582 之后剩下
// 未命中的约 85%。命中按价只有未命中的 1/50（DeepSeek Flash 每百万 0.006 对 0.30 美元），
// 一次保活约读 7 万命中 token，比一次过期整份重付便宜约 50 倍。
//
// 实测（11-17，devin/swe-2 经 sub2api，同一份 6.75 万 token 的生产请求）：4 分半时保活
// 一次，9 分钟后续接命中 100%；不保活的对照组 9 分钟后命中 0%。
//
// 照 Aider 的 --cache-keepalive-pings（每 295 秒一次、限次数）和 Pi 的 pi-keepwarm：
// 间隔贴着 5 分钟设，只在最后一次对话后保一段时间。11-16 隔了 5 分钟以上才来的
// 115 轮里，80 轮在 30 分钟内，再往后收益很小。
//
// 保活请求用的是这次实际发出去的请求（隐私代理换过别名、带着同一个
// PromptCacheKey），走同一条 provider 链，所以前缀逐字一样。记账用途是
// reply_keepalive，单独统计，不混进主回复。
const (
	replyKeepaliveInterval = 270 * time.Second
	replyKeepaliveWindow   = 30 * time.Minute
	// replyKeepaliveMaxOutput 只要上游走完一次预填充；有的接口不收太小的上限。
	replyKeepaliveMaxOutput = 16
	replyKeepaliveTimeout   = 2 * time.Minute
)

type replyKeepalives struct {
	mu       sync.Mutex
	sessions map[string]*replyKeepaliveSession
	// interval、window 为零时用 replyKeepaliveInterval、replyKeepaliveWindow，测试里改小。
	interval, window time.Duration
}

func (k *replyKeepalives) timing() (time.Duration, time.Duration) {
	interval, window := k.interval, k.window
	if interval <= 0 {
		interval = replyKeepaliveInterval
	}
	if window <= 0 {
		window = replyKeepaliveWindow
	}
	return interval, window
}

type replyKeepaliveSession struct {
	timer    *time.Timer
	request  llm.GenerateRequest
	provider LLMProvider
	event    MessageEvent
	// last 是最后一次真实主回复请求的时间，保活到 last + replyKeepaliveWindow 为止。
	last time.Time
}

// withReplyKeepaliveRun 记下主回复实际发出的请求，供空闲时保活。它包在记账层外面：
// 保活请求自己也走记账，用途换成 reply_keepalive。
func (r *Runtime) withReplyKeepaliveRun(ctx context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	state := llmUsageFromContext(ctx)
	if state == nil {
		return run
	}
	return func(provider LLMProvider) (string, error) {
		return run(&replyKeepaliveLLMProvider{runtime: r, event: state.event, provider: provider})
	}
}

type replyKeepaliveLLMProvider struct {
	runtime  *Runtime
	event    MessageEvent
	provider LLMProvider
}

func (p *replyKeepaliveLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	response, err := p.provider.Generate(ctx, req)
	if err == nil && llmCallPurpose(ctx) == PurposeReply {
		p.runtime.replyKeepalives.note(p.runtime, groupPromptSessionKey(p.event), p.event, req, p.provider)
	}
	return response, err
}

// note 换上这个会话最新的一次主回复请求，从现在起重新计时。
func (k *replyKeepalives) note(r *Runtime, key string, event MessageEvent, req llm.GenerateRequest, provider LLMProvider) {
	if key == "" {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.sessions == nil {
		k.sessions = make(map[string]*replyKeepaliveSession)
	}
	session := k.sessions[key]
	if session == nil {
		session = &replyKeepaliveSession{}
		k.sessions[key] = session
	}
	session.request, session.provider, session.event, session.last = req, provider, event, time.Now()
	if session.timer != nil {
		session.timer.Stop()
	}
	interval, _ := k.timing()
	session.timer = time.AfterFunc(interval, func() { k.fire(r, key, session) })
}

// fire 发一次保活，还在窗口内就排下一次；窗口过了就忘掉这个会话。
func (k *replyKeepalives) fire(r *Runtime, key string, session *replyKeepaliveSession) {
	k.mu.Lock()
	if k.sessions[key] != session {
		k.mu.Unlock()
		return
	}
	request, provider, event, last := session.request, session.provider, session.event, session.last
	k.mu.Unlock()

	request.MaxOutputTokens = replyKeepaliveMaxOutput
	ctx, cancel := context.WithTimeout(r.backgroundContext(), replyKeepaliveTimeout)
	ctx = withLLMUsagePurpose(withLLMUsageContext(ctx, event), PurposeReplyKeepalive)
	if _, err := provider.Generate(ctx, request); err != nil {
		log.Printf("diana reply keepalive failed: session=%s err=%v", key, err)
	}
	cancel()

	k.mu.Lock()
	defer k.mu.Unlock()
	if k.sessions[key] != session || session.last != last {
		// 保活途中来了新的主回复，note 已经重新计时。
		return
	}
	interval, window := k.timing()
	if time.Since(last)+interval > window {
		delete(k.sessions, key)
		return
	}
	session.timer = time.AfterFunc(interval, func() { k.fire(r, key, session) })
}
