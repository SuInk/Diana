// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type transientRetryLLMProvider struct {
	provider     LLMProvider
	disableRetry bool
}

var errModelRequestTimeout = errors.New("diana: single model request timeout")

type modelRequestTimeoutKey struct{}

type modelRequestTimeoutLLMProvider struct {
	provider LLMProvider
	timeout  time.Duration
}

func (p *modelRequestTimeoutLLMProvider) innerLLMProvider() LLMProvider { return p.provider }

func (p *modelRequestTimeoutLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	ctx = context.WithValue(ctx, modelRequestTimeoutKey{}, p.timeout)
	provider := p.provider
	if streamingRequested(ctx) {
		provider = &streamingLLMProvider{provider: provider}
	}
	return provider.Generate(ctx, req)
}

type llmAttemptTimeoutManager interface {
	ManagesAttemptTimeout() bool
}

func withTransientLLMRetry(provider LLMProvider, enabled bool) LLMProvider {
	if provider == nil {
		return provider
	}
	return &transientRetryLLMProvider{provider: provider, disableRetry: !enabled}
}

// registryLLMProvider keeps Registry-backed calls on the same retry policy as
// legacy profile providers. Callers such as one-shot routing can still opt out.
func registryLLMProvider(registry *llm.ProviderRegistry, selection llm.AgentModelConfig, retryTransient bool) LLMProvider {
	return withTransientLLMRetry(llm.RegistryClient{Registry: registry, Selection: selection}, retryTransient)
}

// wrappedLLMProvider 是只做装饰、不改变选中模型的 provider。要读模型身份时顺着它
// 一层层往里找，直到碰到真正握着选择的那一层。
type wrappedLLMProvider interface {
	innerLLMProvider() LLMProvider
}

func (p *transientRetryLLMProvider) innerLLMProvider() LLMProvider   { return p.provider }
func (p *streamingLLMProvider) innerLLMProvider() LLMProvider        { return p.provider }
func (p *promptCacheProbeLLMProvider) innerLLMProvider() LLMProvider { return p.provider }
func (p *usageAccountingLLMProvider) innerLLMProvider() LLMProvider  { return p.provider }

// unwrapLLMDecorators 剥掉所有装饰层。层数有限，上限只防意外的自引用。
func unwrapLLMDecorators(provider LLMProvider) LLMProvider {
	for range 16 {
		wrapped, ok := provider.(wrappedLLMProvider)
		if !ok {
			return provider
		}
		inner := wrapped.innerLLMProvider()
		if inner == nil {
			return provider
		}
		provider = inner
	}
	return provider
}

func unwrapTransientLLMRetry(provider LLMProvider) LLMProvider {
	if wrapped, ok := provider.(*transientRetryLLMProvider); ok && wrapped != nil && wrapped.provider != nil {
		return wrapped.provider
	}
	return provider
}

func (p *transientRetryLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if timeout, _ := ctx.Value(modelRequestTimeoutKey{}).(time.Duration); p.disableRetry && timeout <= 0 {
		return p.provider.Generate(ctx, req)
	}
	return generateWithTransientRetry(ctx, p.provider, req, !p.disableRetry)
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attemptCtx := ctx
		cancel := func() {}
		if attemptTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, attemptTimeout)
		}
		hardCtx := attemptCtx
		hardCancel := func() {}
		if hardTimeout, _ := ctx.Value(modelRequestTimeoutKey{}).(time.Duration); hardTimeout > 0 {
			hardCtx, hardCancel = context.WithTimeoutCause(attemptCtx, hardTimeout, errModelRequestTimeout)
		}
		resp, err := provider.Generate(hardCtx, req)
		hardExpired := errors.Is(context.Cause(hardCtx), errModelRequestTimeout)
		hardCancel()
		cancel()
		if hardExpired && ctx.Err() == nil {
			if errors.Is(err, errLLMOutputCommitted) {
				err = committedLLMError{cause: errModelRequestTimeout}
			} else {
				err = errModelRequestTimeout
			}
			resp = nil
		}
		if err == nil && resp != nil && len(resp.ToolCalls) == 0 {
			err = llm.RejectionNoticeError(resp.Text)
			if err != nil {
				resp = nil
			}
		}
		err = classifyLLMError(err)
		if errors.Is(err, errLLMOutputCommitted) {
			return nil, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		// 上下文窗口在目录没给出时是按模型名推断的，推断偏大就会被供应商判为
		// 超限。这类错误重发原请求没有意义，收缩预算再试一次才有：逐次减半，
		// 直到落回保守窗口为止。
		if llm.IsContextOverflowError(err) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			shrunk, ok := shrinkContextForRetry(req, err)
			if !ok || shrinkAttempts >= maxContextShrinkRetries {
				return resp, err
			}
			log.Printf("diana llm context overflow, retrying with max_context_tokens=%d", shrunk.MaxContextTokens)
			reportLLMEventFromContext(ctx, llmEvent{Kind: llmEventContextShrink, Model: req.Model, MaxContextTokens: shrunk.MaxContextTokens, Err: err})
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
	if errors.Is(err, llm.ErrUnverifiedRejection) {
		return true
	}
	if errors.Is(err, errContentPolicyRejection) || isContentPolicyRejection(err) {
		return true
	}
	// 额度用完、账号池全被限流这类错误，等 700ms 再发一次只会拿到同一句话，还白白
	// 多打一个请求、多等一两秒；直接交给降级链切下一个候选。只有一个候选时也不必
	// 原地重试。它们常以 503 的形式出现（「Error 503 ... All accounts limited」），
	// 所以得在瞬时重试之前先拦下。
	if policy := llmErrorPolicy(err); policy.failover && !policy.retry {
		return true
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
	report         llmEventReporter
	cooldowns      *llmCooldownTable
	clients        []LLMProvider
	clientErrors   []error
	clientLoaded   []bool
}

type registryFailoverCandidate struct {
	profile   llm.Profile
	selection llm.AgentModelConfig
}

// registryFailoverLLMProvider keeps the ProviderRegistry routing contract while
// applying the same ordered profile failover used by the legacy client path.
type registryFailoverLLMProvider struct {
	mu             sync.Mutex
	registry       *llm.ProviderRegistry
	candidates     []registryFailoverCandidate
	retryTransient bool
	wrapGroupError bool
	group          string
	current        int
	report         llmEventReporter
	// cooldowns 是 Runtime 上跨请求保留的冷却表；provider 自己每次调用都新建，
	// 记不住上一次谁刚被限流。
	cooldowns *llmCooldownTable
}

func newRegistryFailoverLLMProvider(registry *llm.ProviderRegistry, profiles []llm.Profile, retryTransient bool, wrapGroupError bool) (*registryFailoverLLMProvider, error) {
	if registry == nil {
		return nil, fmt.Errorf("diana: llm provider registry is not configured")
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("diana: no llm profile is configured")
	}
	candidates := make([]registryFailoverCandidate, 0, len(profiles))
	for _, profile := range profiles {
		selection := profileRegistrySelection(registry, profile)
		model, ok := registry.Model(selection.ModelID)
		if !ok || model.ProviderID != selection.ProviderID {
			continue
		}
		candidates = append(candidates, registryFailoverCandidate{profile: profile, selection: selection})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("diana: no registered llm model matches the configured profiles")
	}
	return &registryFailoverLLMProvider{
		registry:       registry,
		candidates:     candidates,
		retryTransient: retryTransient,
		wrapGroupError: wrapGroupError,
		group:          llm.NormalizeProfileGroup(candidates[0].profile.Group),
	}, nil
}

func (p *registryFailoverLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx = withLLMEventReporter(ctx, p.report)

	order := p.attemptOrder(p.current)
	var lastErr error
	for position, index := range order {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		candidate := p.candidates[index]
		var client LLMProvider = llm.RegistryClient{Registry: p.registry, Selection: candidate.selection}
		if streamingRequested(ctx) {
			client = &streamingLLMProvider{provider: client}
		}
		resp, err := generateWithTransientRetryTimeout(ctx, client, candidateReasoningRequest(req, candidate.profile), p.retryTransient, candidate.profile.Config.Timeout)
		if err == nil {
			p.current = index
			p.cooldowns.clear(candidate.profile)
			return resp, nil
		}
		failover := shouldFailoverLLMError(err)
		lastErr = annotateLLMProviderAttempt(err, candidate.profile, req)
		if errors.Is(err, errLLMOutputCommitted) {
			noteLLMCandidateFailure(p.cooldowns, p.group, candidate.profile, healthFailureCause(err))
			return nil, lastErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		noteLLMCandidateFailure(p.cooldowns, p.group, candidate.profile, healthFailureCause(err))
		if !failover {
			return nil, lastErr
		}
		if position+1 < len(order) {
			next := p.candidates[order[position+1]]
			logLLMFailover(p.group, false, candidate.profile, next.profile, lastErr)
			p.reportFailover(candidate.profile, next.profile, false, lastErr)
		}
	}
	if p.wrapGroupError && len(p.candidates) > 1 {
		return nil, fmt.Errorf("diana: llm profiles in group %q are unavailable: %w", p.group, lastErr)
	}
	return nil, lastErr
}

func (p *registryFailoverLLMProvider) attemptOrder(start int) []int {
	return p.cooldowns.order(p.group, len(p.candidates), start, func(index int) llm.Profile { return p.candidates[index].profile })
}

// reportFailover 把一次切档交给运行时写进运行日志。
func (p *registryFailoverLLMProvider) reportFailover(from, to llm.Profile, stream bool, err error) {
	if p.report != nil {
		p.report(newLLMFailoverEvent(p.group, from, to, stream, err))
	}
}

// candidateReasoningRequest 把候选配置档上的思考强度带进请求。注册表里的客户端
// 用的是存盘的提供商配置，模型分配按用途改的思考强度只落在候选的 profile 上，
// 不借请求这一层就发不出去。调用方已经指定的以调用方为准。
func candidateReasoningRequest(req llm.GenerateRequest, profile llm.Profile) llm.GenerateRequest {
	if req.ReasoningEffort == "" {
		req.ReasoningEffort = profile.Config.ReasoningEffort
	}
	return req
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
	ctx = withLLMEventReporter(ctx, p.report)

	order := p.cooldowns.order(p.group, len(p.profiles), p.current, func(index int) llm.Profile {
		return p.profiles[index]
	})
	var lastErr error
	for position, index := range order {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		client, err := p.client(index)
		if err == nil {
			var resp *llm.GenerateResponse
			if streamingRequested(ctx) {
				client = &streamingLLMProvider{provider: client}
			}
			resp, err = generateWithTransientRetryTimeout(ctx, client, req, p.retryTransient, p.profiles[index].Config.Timeout)
			if err == nil {
				p.current = index
				p.cooldowns.clear(p.profiles[index])
				if p.activate != nil {
					p.activate(p.profiles[index].ID)
				}
				return resp, nil
			}
		}
		// 降级判定看的是原始错误：身份标注只是给人看的，不该影响该不该换配置档。
		failover := shouldFailoverLLMError(err)
		rawErr := err
		err = annotateLLMProviderAttempt(err, p.profiles[index], req)
		lastErr = err
		if errors.Is(err, errLLMOutputCommitted) {
			noteLLMCandidateFailure(p.cooldowns, p.group, p.profiles[index], healthFailureCause(rawErr))
			return nil, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		noteLLMCandidateFailure(p.cooldowns, p.group, p.profiles[index], healthFailureCause(rawErr))
		if !failover {
			return nil, err
		}
		// 这条路径以前连终端日志都没有：换了档、用的是备用模型，哪里都看不出来。
		if position+1 < len(order) {
			from, to := p.profiles[index], p.profiles[order[position+1]]
			logLLMFailover(p.group, false, from, to, err)
			if p.report != nil {
				p.report(newLLMFailoverEvent(p.group, from, to, false, err))
			}
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

// shrinkContextForRetry 收缩请求上下文上限，供上下文超限后重试使用。报错里写明了
// 真实窗口（如智谱的 `max_new_tokens` must be <= 16384）就直接缩到它；读不出来才
// 减半：首次从配置的窗口起算，已经收缩过就在上次的基础上继续减半。
func shrinkContextForRetry(req llm.GenerateRequest, err error) (llm.GenerateRequest, bool) {
	current := req.MaxContextTokens
	if current <= 0 {
		current = llm.DefaultMaxContextTokens
	}
	if limit, ok := llm.ContextOverflowLimit(err); ok && limit < current {
		req.MaxContextTokens = limit
		return req, true
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
