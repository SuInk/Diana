// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// backgroundLogInterval 是同一件后台事件两次写进运行日志的最短间隔。上游挂了、数据库
// 忙的时候，每次调用、每条消息都会撞一次，不节流的话运行日志很快就被这一种刷满。
const backgroundLogInterval = time.Minute

// llmEventKind 是模型调用过程中值得让人看到的几件事。
type llmEventKind string

const (
	// llmEventFailover 这一档配置失败，换下一档接着试。
	llmEventFailover llmEventKind = "llm_failover"
	// llmEventContextShrink 上游判上下文超限，收小上下文重发。
	llmEventContextShrink llmEventKind = "llm_context_shrink"
)

// llmEvent 描述一次线路切换或收缩重试。
type llmEvent struct {
	Kind             llmEventKind
	Group            string
	Model            string
	From             string
	To               string
	Stream           bool
	MaxContextTokens int64
	Err              error
}

// llmEventReporter 由运行时挂到 provider 上：provider 本身是每次调用临时建的，
// 拿不到运行日志，也存不住节流状态。
type llmEventReporter func(llmEvent)

type llmEventReporterKey struct{}

// withLLMEventReporter 把回调带进一次调用的上下文。收缩重试发生在公共的重试函数里，
// 那里只有 ctx，provider 在调用它之前把自己的回调放进去。
func withLLMEventReporter(ctx context.Context, report llmEventReporter) context.Context {
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, llmEventReporterKey{}, report)
}

func reportLLMEventFromContext(ctx context.Context, event llmEvent) {
	if report, ok := ctx.Value(llmEventReporterKey{}).(llmEventReporter); ok && report != nil {
		report(event)
	}
}

// logThrottle 记每种后台事件上一次写日志的时间。
type logThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (t *logThrottle) allow(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		t.last = map[string]time.Time{}
	}
	if previous, ok := t.last[key]; ok && now.Sub(previous) < backgroundLogInterval {
		return false
	}
	t.last[key] = now
	return true
}

// reportLLMEvent 把线路切换和收缩重试写进运行日志。以前只打到终端，界面上回答不了
// 「这次为什么用了备用模型」「为什么回复里像是少了前面的聊天记录」。
func (r *Runtime) reportLLMEvent(event llmEvent) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	key := fmt.Sprintf("%s|%s|%s|%s|%s", event.Kind, event.Group, event.Model, event.From, event.To)
	if !r.backgroundLogThrottle.allow(key, time.Now()) {
		return
	}
	entry := applog.Entry{
		Kind:     applog.KindOperation,
		Level:    applog.LevelInfo,
		Action:   string(event.Kind),
		Target:   event.From,
		Metadata: map[string]any{"group": event.Group, "model": event.Model},
	}
	if event.Err != nil {
		entry.Detail = truncateRunes(event.Err.Error(), 500)
	}
	switch event.Kind {
	case llmEventFailover:
		entry.Kind = applog.KindError
		entry.Level = applog.LevelError
		entry.Message = fmt.Sprintf("模型配置「%s」调用失败，已切到「%s」", event.From, event.To)
		entry.Metadata["from"] = event.From
		entry.Metadata["to"] = event.To
		entry.Metadata["stream"] = event.Stream
	case llmEventContextShrink:
		entry.Message = fmt.Sprintf("模型「%s」报上下文超限，已收小到 %d tokens 重发", event.Model, event.MaxContextTokens)
		entry.Metadata["max_context_tokens"] = event.MaxContextTokens
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = writer.AppendLog(ctx, entry)
}
