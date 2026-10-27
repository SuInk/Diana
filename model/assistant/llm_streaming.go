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

// Native streams share text, reasoning, tool progress, usage and completion events.
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
//
// 首 token 指上游吐出的任何增量：可见文本、推理、工具名或工具参数片段。只认文本时，
// 只调工具的回复（send 也是工具）全都量不到，量到的那部分又偏向纯文本回复。
type ttftCollector struct {
	mu sync.Mutex
	// firstDelta 是首个增量到达的时刻，零值表示这次调用没有可信的 TTFT。
	firstDelta time.Time
	// deltas 是增量的条数。只有一条文本时说明底层可能退化成了非流式（见 ttft）。
	deltas int
	// streamed 表示收到过推理或工具进度事件：只有真流式的解析器才会发它们。
	streamed bool
	// parent 是外层装饰器（调试轨迹）的 collector，增量同样报给它。
	parent *ttftCollector
}

type ttftCollectorKey struct{}

type textDeltaObserverKey struct{}

type textDeltaObserver interface {
	// Returns true if text was sent, or delivery cannot be ruled out.
	ObserveTextDelta(ctx context.Context, text string) bool
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
	collector := &ttftCollector{parent: ttftCollectorFromContext(ctx)}
	return context.WithValue(ctx, ttftCollectorKey{}, collector), collector
}

func ttftCollectorFromContext(ctx context.Context) *ttftCollector {
	if ctx == nil {
		return nil
	}
	collector, _ := ctx.Value(ttftCollectorKey{}).(*ttftCollector)
	return collector
}

func (c *ttftCollector) observeDelta(at time.Time, streamed bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.deltas++
	c.streamed = c.streamed || streamed
	if c.firstDelta.IsZero() {
		c.firstDelta = at
	}
	c.mu.Unlock()
	c.parent.observeDelta(at, streamed)
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
	if (c.deltas < 2 && !c.streamed) || c.firstDelta.IsZero() || !c.firstDelta.After(started) {
		return 0
	}
	return c.firstDelta.Sub(started)
}

// streamingLLMProvider 用流式跑一次调用，攒成完整响应后交出去。
type streamingLLMProvider struct {
	provider LLMProvider
}

type streamingRequestKey struct{}

func streamingRequested(ctx context.Context) bool {
	enabled, _ := ctx.Value(streamingRequestKey{}).(bool)
	return enabled
}

func (p *streamingLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch provider := p.provider.(type) {
	case *registryFailoverLLMProvider, *profileFailoverLLMProvider, *modelRequestTimeoutLLMProvider:
		return p.provider.Generate(context.WithValue(ctx, streamingRequestKey{}, true), req)
	case *transientRetryLLMProvider:
		streaming := &streamingLLMProvider{provider: provider.provider}
		if timeout, _ := ctx.Value(modelRequestTimeoutKey{}).(time.Duration); provider.disableRetry && timeout <= 0 {
			return streaming.Generate(ctx, req)
		}
		return generateWithTransientRetry(ctx, streaming, req, !provider.disableRetry)
	}
	streamer, ok := p.provider.(streamingLLMClient)
	if !ok {
		return p.provider.Generate(ctx, req)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := streamer.Stream(streamCtx, req)
	if err != nil {
		if streamUnsupported(err) {
			return p.provider.Generate(ctx, req)
		}
		return nil, err
	}
	if events == nil {
		return nil, errors.New("diana: llm provider returned a nil stream")
	}
	return accumulateChatEvents(ctx, events)
}

func (p *streamingLLMProvider) ManagesAttemptTimeout() bool {
	manager, ok := p.provider.(llmAttemptTimeoutManager)
	return ok && manager.ManagesAttemptTimeout()
}

func streamUnsupported(err error) bool {
	if err == nil || errors.Is(err, errLLMOutputCommitted) {
		return false
	}
	switch llm.ErrorStatusCode(err) {
	case 0, 400, 404, 405, 422, 501:
	default:
		return false
	}
	return containsAnyMarker(strings.ToLower(err.Error()), []string{"streaming is not supported", "streaming not supported", "streaming unsupported", "does not support streaming", "stream is not supported"})
}

func healthFailureCause(err error) error {
	var committed committedLLMError
	if errors.As(err, &committed) {
		return committed.cause
	}
	return err
}

// accumulateChatEvents 把事件流攒成一个完整响应，顺便记下首 token 时刻。
func accumulateChatEvents(ctx context.Context, events <-chan llm.ChatEvent) (response *llm.GenerateResponse, err error) {
	committed := false
	defer func() {
		if err != nil && committed {
			err = committedLLMError{cause: err}
		}
	}()
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
readLoop:
	for {
		var event llm.ChatEvent
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case next, ok := <-events:
			if !ok {
				break readLoop
			}
			event = next
		}
		switch event.Type {
		case llm.ChatEventTextDelta:
			event.Text = visible.Push(event.Text)
			if event.Text == "" {
				continue
			}
			collector.observeDelta(time.Now(), false)
			text.WriteString(event.Text)
			if observer != nil {
				committed = observer.ObserveTextDelta(ctx, text.String()) || committed
			}
		case llm.ChatEventReasoning, llm.ChatEventProgress:
			// Reasoning never enters visible text. Private replay state arrives on done.
			collector.observeDelta(time.Now(), true)
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
			if streamErr == "" && streamCause != nil {
				streamErr = streamCause.Error()
			}
			if streamErr == "" {
				streamErr = "unknown stream error"
			}
			break readLoop
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
	// Keep the candidate retry budget; never restart the non-streaming chain.
	if strings.TrimSpace(text.String()) == "" && len(toolCalls) == 0 {
		return nil, &streamingFailedError{reason: "output is empty (no text or tool calls)", cause: llm.ErrCompletionEmpty}
	}
	if len(toolCalls) == 0 {
		if notice := llm.RejectionNoticeError(text.String()); notice != nil {
			return nil, notice
		}
	}
	return &llm.GenerateResponse{
		Provider: metadata.Provider, Model: metadata.Model,
		ContinuationScope: metadata.ContinuationScope, AnthropicThinking: metadata.AnthropicThinking, ReasoningContent: metadata.ReasoningContent, ResponsesOutput: metadata.ResponsesOutput,
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
func (r *Runtime) withLLMStreamingRun(ctx context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	if run == nil || !boolValue(r.configForContext(ctx).LLMStreamingEnabled, true) {
		return run
	}
	return func(provider LLMProvider) (string, error) {
		return run(&streamingLLMProvider{provider: provider})
	}
}
