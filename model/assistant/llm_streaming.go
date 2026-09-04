// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 流式只为一件事：拿到真实的 TTFT（首 token 时间）。
//
// 回复本身仍然是攒齐了再发。聊天平台这边没有「边生成边改一条消息」的东西，而且
// 发送前审核审的是完整回复、splitChatReply 也要看完整文本——把 token 直接往群里
// 冒会把这两样一起拆掉。所以这里流式消费、原地攒成一个完整的 GenerateResponse
// 交出去，对上面四层装饰器（身份脱敏、预算封顶、调试追踪、缓存探针）完全透明。
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
// 只有一条文本增量时一律不报：OpenAI 的 chat/completions 在带工具时会直接退回
// 非流式，把整段回复当成一个 delta 吐出来（见 openai_compatible.go 的 Stream）。
// 那种情况下「首 token 时间」等于总耗时，是个看起来正常、实际全错的数——比没有
// 这个指标更糟。
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
	if err != nil || events == nil {
		// 起流失败就走老路。流式是为了一个诊断指标，不值得让它决定回复发不发得出去。
		return p.provider.Generate(ctx, req)
	}
	response, err := accumulateChatEvents(ctx, events)
	if err != nil {
		return p.provider.Generate(ctx, req)
	}
	return response, nil
}

// accumulateChatEvents 把事件流攒成一个完整响应，顺便记下首 token 时刻。
func accumulateChatEvents(ctx context.Context, events <-chan llm.ChatEvent) (*llm.GenerateResponse, error) {
	collector := ttftCollectorFromContext(ctx)
	observer := textDeltaObserverFromContext(ctx)
	var text strings.Builder
	var toolCalls []llm.ToolCall
	var usage llm.Usage
	streamErr := ""
	for event := range events {
		switch event.Type {
		case llm.ChatEventTextDelta:
			if event.Text == "" {
				continue
			}
			collector.observeDelta(time.Now())
			text.WriteString(event.Text)
			if observer != nil {
				observer.ObserveTextDelta(ctx, text.String())
			}
		case llm.ChatEventReasoning:
			// GenerateResponse 没有放推理内容的地方，非流式那条路同样丢掉它。
			// 这里跟着丢，保证两条路的返回值一模一样。
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
		}
	}
	if streamErr != "" {
		return nil, &streamingFailedError{reason: streamErr}
	}
	return &llm.GenerateResponse{
		Text:      text.String(),
		ToolCalls: toolCalls,
		Usage:     usage,
	}, nil
}

type streamingFailedError struct{ reason string }

func (e *streamingFailedError) Error() string {
	if e == nil || strings.TrimSpace(e.reason) == "" {
		return "diana: llm stream failed"
	}
	return "diana: llm stream failed: " + e.reason
}

// withLLMStreamingRun 把流式包在最内层。关掉时原样返回，不进链。
func (r *Runtime) withLLMStreamingRun(_ context.Context, run llmProviderRunFunc) llmProviderRunFunc {
	if run == nil || !boolValue(r.Config().LLMStreamingEnabled, false) {
		return run
	}
	return func(provider LLMProvider) (string, error) {
		return run(&streamingLLMProvider{provider: provider})
	}
}
