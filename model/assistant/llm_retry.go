// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type transientRetryLLMProvider struct {
	provider LLMProvider
}

type llmAttemptTimeoutManager interface {
	ManagesAttemptTimeout() bool
}

func withTransientLLMRetry(provider LLMProvider, enabled bool) LLMProvider {
	if provider == nil || !enabled {
		return provider
	}
	return &transientRetryLLMProvider{provider: provider}
}

// registryLLMProvider keeps Registry-backed calls on the same retry policy as
// legacy profile providers. Callers such as one-shot routing can still opt out.
func registryLLMProvider(registry *llm.ProviderRegistry, selection llm.AgentModelConfig, retryTransient bool) LLMProvider {
	return withTransientLLMRetry(llm.RegistryClient{Registry: registry, Selection: selection}, retryTransient)
}

func unwrapTransientLLMRetry(provider LLMProvider) LLMProvider {
	if wrapped, ok := provider.(*transientRetryLLMProvider); ok && wrapped != nil && wrapped.provider != nil {
		return wrapped.provider
	}
	return provider
}

func (p *transientRetryLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return generateWithTransientRetry(ctx, p.provider, req, true)
}

func generateWithTransientRetry(ctx context.Context, provider LLMProvider, req llm.GenerateRequest, enabled bool) (*llm.GenerateResponse, error) {
	return generateWithTransientRetryTimeout(ctx, provider, req, enabled, 0)
}

func generateWithTransientRetryTimeout(ctx context.Context, provider LLMProvider, req llm.GenerateRequest, enabled bool, requestTimeout time.Duration) (*llm.GenerateResponse, error) {
	return generateWithTransientRetryPolicy(ctx, provider, req, enabled, requestTimeout, llmTransientMaxRetries, llmTransientRetryDelay)
}

// Non-streaming providers keep the configured per-request timeout. Providers
// with activity-aware timeouts manage their own attempts. All attempts still
// share the caller deadline, so cancellation is respected and never reset.
func generateWithTransientRetryPolicy(ctx context.Context, provider LLMProvider, req llm.GenerateRequest, enabled bool, requestTimeout time.Duration, maxRetries int, retryDelay time.Duration) (*llm.GenerateResponse, error) {
	if provider == nil {
		return nil, fmt.Errorf("diana: llm provider is not configured")
	}
	if manager, ok := provider.(llmAttemptTimeoutManager); ok && manager.ManagesAttemptTimeout() {
		requestTimeout = 0
	}
	if !enabled {
		maxRetries = 0
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	attemptTimeout := boundedInitialRetryTimeout(ctx, requestTimeout, maxRetries, retryDelay)
	// 收缩重试与瞬时重试是两回事，各记各的：一次窗口推断失误不该把网络抖动的
	// 重试额度也用掉。
	shrinkAttempts := 0
	for attempt := 0; ; attempt++ {
		attemptCtx := ctx
		cancel := func() {}
		if attemptTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, attemptTimeout)
		}
		resp, err := provider.Generate(attemptCtx, req)
		cancel()
		err = classifyLLMError(err)
		// 上下文窗口在目录没给出时是按模型名推断的，推断偏大就会被供应商判为
		// 超限。这类错误重发原请求没有意义，收缩预算再试一次才有：逐次减半，
		// 直到落回保守窗口为止。
		if llm.IsContextOverflowError(err) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			shrunk, ok := shrinkContextForRetry(req)
			if !ok || shrinkAttempts >= maxContextShrinkRetries {
				return resp, err
			}
			log.Printf("diana llm context overflow, retrying with max_context_tokens=%d", shrunk.MaxContextTokens)
			req = shrunk
			shrinkAttempts++
			attempt--
			continue
		}
		if err == nil || !enabled || !shouldRetryTransientLLMError(err) {
			return resp, err
		}
		if shouldFailoverWithoutSameProfileRetry(err) {
			return resp, err
		}
		if attempt >= maxRetries {
			return resp, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if retryDelay <= 0 {
			continue
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func shouldFailoverWithoutSameProfileRetry(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"response header timeout",
		"response body idle timeout",
		"stream idle timeout",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func boundedInitialRetryTimeout(ctx context.Context, configured time.Duration, maxRetries int, retryDelay time.Duration) time.Duration {
	if configured <= 0 {
		return 0
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return configured
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Nanosecond
	}
	if llmRetryBudget(configured, maxRetries, retryDelay) <= remaining {
		return configured
	}
	available := remaining - time.Duration(maxRetries)*retryDelay
	if available <= 0 {
		return remaining
	}
	bounded := available / time.Duration(maxRetries+1)
	if bounded <= 0 {
		return time.Nanosecond
	}
	if bounded < configured {
		return bounded
	}
	return configured
}

func llmRetryBudget(requestTimeout time.Duration, maxRetries int, retryDelay time.Duration) time.Duration {
	total := time.Duration(maxRetries+1) * requestTimeout
	if maxRetries > 0 {
		total += time.Duration(maxRetries) * retryDelay
	}
	return total
}

// profileFailoverLLMProvider retries and switches profiles for one Generate
// call. The caller's Agent loop stays alive, so completed tool observations are
// preserved when a later model step is retried.
type profileFailoverLLMProvider struct {
	mu             sync.Mutex
	profiles       []llm.Profile
	factory        LLMProviderConfigFactory
	retryTransient bool
	activate       func(string)
	wrapGroupError bool
	group          string
	current        int
	clients        []LLMProvider
	clientErrors   []error
	clientLoaded   []bool
}

func newProfileFailoverLLMProvider(
	profiles []llm.Profile,
	factory LLMProviderConfigFactory,
	retryTransient bool,
	activate func(string),
	wrapGroupError bool,
) (*profileFailoverLLMProvider, error) {
	if len(profiles) == 0 {
		return nil, fmt.Errorf("diana: no llm profile is configured")
	}
	if factory == nil {
		return nil, fmt.Errorf("diana: llm provider factory is not configured")
	}
	group := llm.NormalizeProfileGroup(profiles[0].Group)
	return &profileFailoverLLMProvider{
		profiles:       append([]llm.Profile(nil), profiles...),
		factory:        factory,
		retryTransient: retryTransient,
		activate:       activate,
		wrapGroupError: wrapGroupError,
		group:          group,
		clients:        make([]LLMProvider, len(profiles)),
		clientErrors:   make([]error, len(profiles)),
		clientLoaded:   make([]bool, len(profiles)),
	}, nil
}

func (p *profileFailoverLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var lastErr error
	for offset := 0; offset < len(p.profiles); offset++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		index := (p.current + offset) % len(p.profiles)
		client, err := p.client(index)
		if err == nil {
			var resp *llm.GenerateResponse
			resp, err = generateWithTransientRetryTimeout(ctx, client, req, p.retryTransient, p.profiles[index].Config.Timeout)
			if err == nil {
				p.current = index
				if p.activate != nil {
					p.activate(p.profiles[index].ID)
				}
				return resp, nil
			}
		}
		lastErr = err
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if !shouldFailoverLLMError(err) {
			return nil, err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("unknown llm profile error")
	}
	if p.wrapGroupError && len(p.profiles) > 1 {
		return nil, fmt.Errorf("diana: llm profiles in group %q are unavailable: %w", p.group, lastErr)
	}
	return nil, lastErr
}

func (p *profileFailoverLLMProvider) client(index int) (LLMProvider, error) {
	if p.clientLoaded[index] {
		return p.clients[index], p.clientErrors[index]
	}
	p.clientLoaded[index] = true
	p.clients[index], p.clientErrors[index] = p.factory(p.profiles[index].Config)
	return p.clients[index], p.clientErrors[index]
}

// maxContextShrinkRetries 限制收缩次数：128K 起连续减半四次即落到下限，
// 再多只是拖长这一轮回复的耗时。
const maxContextShrinkRetries = 4

// contextOverflowFloorTokens 是收缩重试的下限。低于它继续减半只会把当前消息也
// 裁掉，那时应当如实报错，而不是发一条残缺的请求。
const contextOverflowFloorTokens int64 = 8192

// shrinkContextForRetry 把请求上下文上限减半，供上下文超限后重试使用。
// 首次收缩从推断出来的窗口起算；已经收缩过就在上次的基础上继续减半。
func shrinkContextForRetry(req llm.GenerateRequest) (llm.GenerateRequest, bool) {
	current := req.MaxContextTokens
	if current <= 0 {
		current = llm.DefaultMaxContextTokens
	}
	next := current / 2
	if next < contextOverflowFloorTokens {
		if current <= contextOverflowFloorTokens {
			return req, false
		}
		next = contextOverflowFloorTokens
	}
	req.MaxContextTokens = next
	return req, true
}
