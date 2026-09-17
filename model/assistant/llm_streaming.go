// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// Native streams share text, reasoning, tool, usage and completion events.
// Accumulate a complete response before the Agent executes tools or the send
// audit approves chat text. Only visible text deltas reach reply previews.
//
// 位置必须是最内层，紧挨真实客户端：身份脱敏要对完整文本做别名还原
// （restoreText），别名会跨 chunk 边界，在流式中途还原一定会切坏。

// streamingLLMClient 是具体客户端已经实现、但没暴露在 llm.LLMClient 接口上的能力。
// 三个 provider（OpenAI / Anthropic / Gemini）都有这个方法。
type streamingLLMClient interface {
	Stream(ctx context.Context, req llm.GenerateRequest) (<-chan llm.ChatEvent, error)
}

// ttftCollector 让内层把首 token 时间交给外层的记账装饰器。
//
// 用 context 传而不是改 Generate 的签名：签名是 LLMProvider 接口的一部分，改它
// 要动全部五层装饰器和所有 mock，而这里只是一个诊断量。
type ttftCollector struct {
	mu sync.Mutex
	// firstDelta 是首个文本增量到达的时刻，零值表示这次调用没有可信的 TTFT。
	firstDelta time.Time
	// deltas 是文本增量的条数。只有一条时说明底层退化成了非流式（见 observe）。
	deltas int
}

type ttftCollectorKey struct{}

type textDeltaObserverKey struct{}

type textDeltaObserver interface {
	ObserveTextDelta(ctx context.Context, text string)
}

func withTextDeltaObserver(ctx context.Context, observer textDeltaObserver) context.Context {
	if ctx == nil || observer == nil {
		return ctx
	}
	return context.WithValue(ctx, textDeltaObserverKey{}, observer)
}

func textDeltaObserverFromContext(ctx context.Context) textDeltaObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(textDeltaObserverKey{}).(textDeltaObserver)
	return observer
}

func withTTFTCollector(ctx context.Context) (context.Context, *ttftCollector) {
	collector := &ttftCollector{}
	return context.WithValue(ctx, ttftCollectorKey{}, collector), collector
}

func ttftCollectorFromContext(ctx context.Context) *ttftCollector {
	if ctx == nil {
		return nil
	}
	collector, _ := ctx.Value(ttftCollectorKey{}).(*ttftCollector)
	return collector
}

func (c *ttftCollector) observeDelta(at time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deltas++
	if c.firstDelta.IsZero() {
		c.firstDelta = at
	}
}

// ttft 返回相对 started 的首 token 时延；没有可信结论时返回 0。
//
// 只有一条文本增量时不报：部分不支持原生流式的适配器只会发送完整文本，
// 无法据此区分首 token 时间与总耗时。
func (c *ttftCollector) ttft(started time.Time) time.Duration {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deltas < 2 || c.firstDelta.IsZero() || !c.firstDelta.After(started) {
		return 0
	}
	return c.firstDelta.Sub(started)
}

// streamingLLMProvider 用流式跑一次调用，攒成完整响应后交出去。
type streamingLLMProvider struct {
	provider LLMProvider
}

func (p *streamingLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	streamer, ok := p.provider.(streamingLLMClient)
	if !ok {
		return p.provider.Generate(ctx, req)
	}
	events, err := streamer.Stream(ctx, req)
	if errors.Is(err, llm.ErrUnverifiedRejection) || isContentPolicyRejection(err) {
		return nil, err
	}
	if err != nil || events == nil {
		// Providers without a working streaming endpoint can still use Generate.
		return p.provider.Generate(ctx, req)
	}
	response, err := accumulateChatEvents(ctx, events)
	if errors.Is(err, llm.ErrUnverifiedRejection) || isContentPolicyRejection(err) {
		return p.retryAfterStreamedRejection(ctx, req, err)
	}
	if err != nil {
		return p.provider.Generate(ctx, req)
	}
	if response != nil && len(response.ToolCalls) == 0 {
		if notice := llm.RejectionNoticeError(response.Text); notice != nil {
			return p.retryAfterStreamedRejection(ctx, req, notice)
		}
	}
	return response, nil
}

// rejectedCandidateSkipper 由后备 provider 实现：流已经正常打开、正文却是拦截时，
// 让它把刚才那个候选往后挪一位。
type rejectedCandidateSkipper interface {
	skipRejectedCandidate(cause error) bool
}

// retryAfterStreamedRejection 处理「流正常打开、正文却是上游拦截文案」。
//
// 后备切换只发生在打开流那一刻：429、502 这类打开时就失败的错误会切到下一个候选，
// 可流一旦打开，候选就定下了。上游把拦截文案当成正常正文流回来时，这一层认出拦截
// 只能原样报错——于是非流式路径会切后备、流式路径却不会，同一句话开不开流式结果
// 不一样。这里把能切后备的拦截交回后备 provider，跳过刚被拦的候选再来一次，免得
// 从同一个候选开始、白白再被拦一遍。
//
// 不能切后备的照旧原样返回：内容策略拦截和 Gemini 结构化拦截码是有意设计成不换
// 模型重发的，判断复用 shouldFailoverLLMError，和非流式路径同一个口径。
func (p *streamingLLMProvider) retryAfterStreamedRejection(ctx context.Context, req llm.GenerateRequest, cause error) (*llm.GenerateResponse, error) {
	if !shouldFailoverLLMError(cause) {
		return nil, cause
	}
	skipper, ok := p.provider.(rejectedCandidateSkipper)
	if !ok || !skipper.skipRejectedCandidate(cause) {
		return nil, cause
	}
	return p.provider.Generate(ctx, req)
}

// accumulateChatEvents 把事件流攒成一个完整响应，顺便记下首 token 时刻。
func accumulateChatEvents(ctx context.Context, events <-chan llm.ChatEvent) (*llm.GenerateResponse, error) {
	collector := ttftCollectorFromContext(ctx)
	observer := textDeltaObserverFromContext(ctx)
	var text strings.Builder
	var visible llm.VisibleTextFilter
	var toolCalls []llm.ToolCall
	var usage llm.Usage
	var metadata llm.GenerateResponse
	completed := false
	streamErr := ""
	var streamCause error
	for event := range events {
		switch event.Type {
		case llm.ChatEventTextDelta:
			event.Text = visible.Push(event.Text)
			if event.Text == "" {
				continue
			}
			collector.observeDelta(time.Now())
			text.WriteString(event.Text)
			if observer != nil {
				observer.ObserveTextDelta(ctx, text.String())
			}
		case llm.ChatEventReasoning:
			// Reasoning never enters visible text. Private replay state arrives on done.
		case llm.ChatEventDone:
			completed = true
			if event.Response != nil {
				metadata = *event.Response
			}
		case llm.ChatEventToolCall:
			if event.ToolCall != nil {
				toolCalls = append(toolCalls, *event.ToolCall)
			}
		case llm.ChatEventUsage:
			if event.Usage != nil {
				usage = *event.Usage
			}
		case llm.ChatEventError:
			streamErr = event.Error
			streamCause = event.ErrorCause
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if streamErr == "" && !completed {
		streamErr = "stream ended before completion"
	}
	if streamErr != "" {
		return nil, &streamingFailedError{reason: streamErr, cause: streamCause}
	}
	// A completed reasoning-only stream is not a usable assistant response.
	// Report it as a failure so Generate can retry through the normal provider path.
	if strings.TrimSpace(text.String()) == "" && len(toolCalls) == 0 {
		return nil, &streamingFailedError{reason: "output is empty (no text or tool calls)"}
	}
	return &llm.GenerateResponse{
		Provider: metadata.Provider, Model: metadata.Model,
		AnthropicThinking: metadata.AnthropicThinking, ReasoningContent: metadata.ReasoningContent, ResponsesOutput: metadata.ResponsesOutput,
		Text:      text.String(),
		ToolCalls: toolCalls,
		Usage:     usage,
	}, nil
}

type streamingFailedError struct {
	reason string
	cause  error
}

func (e *streamingFailedError) Unwrap() error { return e.cause }

func (e *streamingFailedError) Error() string {
	if e == nil || strings.TrimSpace(e.reason) == "" {
		return "diana: llm stream failed"
	}
	return "diana: llm stream failed: " + e.reason
}

// withLLMStreamingRun 把流式包在最内层。关掉时原样返回，不进链。
func (r *Runtime) withLLMStreamingRun(_ context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	if run == nil || !boolValue(r.Config().LLMStreamingEnabled, true) {
		return run
	}
	return func(provider LLMProvider) (string, error) {
		return run(&streamingLLMProvider{provider: provider})
	}
}
