// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
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
	Kind  llmEventKind
	Group string
	Model string
	From  string
	To    string
	// FromName、ToName 是给人看的配置档名，没有名字时是 ID；两端是同一个配置档时
	// 带上模型。From、To 仍是 ID，留在元数据里供检索。
	FromName string
	ToName   string
	// ToModel 是切过去的那一档的模型，同一个配置档下换模型时和 Model 不同。
	ToModel          string
	Stream           bool
	MaxContextTokens int64
	Err              error
	// Mark 是切走的那一档这次失败的记账：冷却到什么时候，或者连续失败到第几次。
	Mark llmFailureMark
}

// llmEventReporter 由运行时挂到 provider 上：provider 本身是每次调用临时建的，
// 拿不到运行日志，也存不住节流状态。
type llmEventReporter func(llmEvent)

// newLLMFailoverEvent 组一条切档事件。
func newLLMFailoverEvent(group string, from, to llm.Profile, stream bool, err error, mark llmFailureMark) llmEvent {
	fromName, toName := llmFailoverDisplayNames(from, to)
	return llmEvent{
		Mark:     mark,
		Kind:     llmEventFailover,
		Group:    group,
		Model:    from.Config.Model,
		ToModel:  to.Config.Model,
		From:     from.ID,
		To:       to.ID,
		FromName: fromName,
		ToName:   toName,
		Stream:   stream,
		Err:      err,
	}
}

// llmFailoverDisplayNames 给运行日志取两端的名字：配置档名，没有名字退回 ID；
// 同一个配置档下换模型时补上模型，不然界面上是「从 A 切到 A」。
func llmFailoverDisplayNames(from, to llm.Profile) (string, string) {
	name := func(profile llm.Profile, withModel bool) string {
		label := firstNonEmpty(strings.TrimSpace(profile.Name), strings.TrimSpace(profile.ID))
		if model := strings.TrimSpace(profile.Config.Model); withModel && model != "" {
			label += " / " + model
		}
		return label
	}
	sameProfile := strings.TrimSpace(from.ID) == strings.TrimSpace(to.ID)
	return name(from, sameProfile), name(to, sameProfile)
}

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
	// 冷却与否分开节流：同一分钟里先「第 1 次失败」后「已冷却」，后一条不能被吞掉。
	key := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%t", event.Kind, event.Group, event.Model, event.From, event.To, event.ToModel, event.Mark.cooldown > 0)
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
		entry.Message = llmFailoverMessage(event, r.configForContext(context.Background()).Location())
		if event.Mark.counted {
			entry.Metadata["failures"] = event.Mark.failures
			entry.Metadata["failures_before_cooldown"] = event.Mark.threshold
		}
		if event.Mark.cooldown > 0 {
			entry.Metadata["cooldown_until"] = event.Mark.until.UTC().Format(time.RFC3339)
		}
		entry.Metadata["from"] = event.From
		entry.Metadata["to"] = event.To
		if event.ToModel != "" {
			entry.Metadata["to_model"] = event.ToModel
		}
		entry.Metadata["stream"] = event.Stream
	case llmEventContextShrink:
		entry.Message = fmt.Sprintf("模型「%s」报上下文超限，已收小到 %d tokens 重发", event.Model, event.MaxContextTokens)
		entry.Metadata["max_context_tokens"] = event.MaxContextTokens
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = writer.AppendLog(ctx, entry)
}

// llmFailoverMessage 写清切走的那一档什么时候再用：冷却到几点，或者还没攒够连续
// 失败、下一次照样先试它。以前只写「调用失败，已切到」，看不出是临时让一下还是
// 停用了一段时间。
func llmFailoverMessage(event llmEvent, location *time.Location) string {
	from, to := firstNonEmpty(event.FromName, event.From), firstNonEmpty(event.ToName, event.To)
	reason := llmFailureReasonLabel(event.Err)
	mark := event.Mark
	switch {
	case mark.cooldown > 0:
		if location == nil {
			location = time.Local
		}
		return fmt.Sprintf("模型配置「%s」%s，冷却到 %s 再试，已切到「%s」", from, reason, mark.until.In(location).Format("15:04:05"), to)
	case mark.counted && mark.failures < mark.threshold:
		return fmt.Sprintf("模型配置「%s」%s（连续第 %d/%d 次），这次先切到「%s」，下次仍先用它", from, reason, mark.failures, mark.threshold, to)
	default:
		return fmt.Sprintf("模型配置「%s」%s，已切到「%s」", from, reason, to)
	}
}

func llmFailureReasonLabel(err error) string {
	if err == nil {
		return "调用失败"
	}
	if errors.Is(err, errLLMCandidateTimeout) || errors.Is(err, errModelRequestTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return "超时"
	}
	if policy := llmErrorPolicy(err); policy.retry && policy.cooldown == llmCooldownDefault {
		return "被限流"
	}
	return "调用失败"
}
