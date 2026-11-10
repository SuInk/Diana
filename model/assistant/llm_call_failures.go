// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

// LLMCallFailedAction 是一次失败的模型请求在运行日志里的动作名。
//
// 以前只有成功的调用写 llm_usage，失败只进进程内的冷却表：重启就清零、十分钟没
// 新失败也清零，控制台上回答不了「今天这个模型错了几次」。现在降级链里每一档失败
// 都落一条，和 llm_usage 并排放在「LLM 调用」里，总览页按它数错误次数。
const LLMCallFailedAction = "llm_call_failed"

// llmAttemptFailure 是降级链里某一档的一次失败。
type llmAttemptFailure struct {
	profile llm.Profile
	model   string
	err     error
	elapsed time.Duration
}

type llmAttemptCollector struct {
	mu       sync.Mutex
	failures []llmAttemptFailure
}

type llmAttemptCollectorKey struct{}

// withLLMAttemptCollector 由记账装饰器挂上：失败发生在更内层的降级 provider 里，
// 那里拿不到消息事件和用途，只能先攒着，由外层统一写日志。
func withLLMAttemptCollector(ctx context.Context) (context.Context, *llmAttemptCollector) {
	collector := &llmAttemptCollector{}
	return context.WithValue(ctx, llmAttemptCollectorKey{}, collector), collector
}

// noteLLMAttemptFailure 记下一档失败。调用方取消（用户撤回、超时被上层掐掉）不算。
func noteLLMAttemptFailure(ctx context.Context, profile llm.Profile, req llm.GenerateRequest, err error, elapsed time.Duration) {
	if ctx == nil || err == nil || errors.Is(err, context.Canceled) {
		return
	}
	collector, _ := ctx.Value(llmAttemptCollectorKey{}).(*llmAttemptCollector)
	if collector == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.failures = append(collector.failures, llmAttemptFailure{
		profile: profile,
		model:   firstNonEmpty(strings.TrimSpace(req.Model), strings.TrimSpace(profile.Config.Model)),
		err:     err,
		elapsed: elapsed,
	})
}

func (c *llmAttemptCollector) snapshot() []llmAttemptFailure {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llmAttemptFailure(nil), c.failures...)
}

// recordLLMCallFailure 写一条失败的模型请求。字段和 llm_usage 对齐，「LLM 调用」
// 页签两种记录一起按时间排；recovered 表示降级到后面某一档最终成功了。
func (r *Runtime) recordLLMCallFailure(ctx context.Context, event MessageEvent, failure llmAttemptFailure, purpose string, recovered bool) {
	model := firstNonEmpty(failure.model, "unknown")
	r.llmUsage.observeFailure(string(failure.profile.Config.Provider), model, strings.TrimSpace(purpose), r.clock())
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	label := firstNonEmpty(strings.TrimSpace(failure.profile.Name), strings.TrimSpace(failure.profile.ID))
	message := "模型「" + model + "」调用失败"
	if label != "" {
		message = "模型「" + model + "」（" + label + "）调用失败"
	}
	if recovered {
		message += "，已由后备接上"
	}
	entry := applog.Entry{
		Kind:    applog.KindError,
		Level:   applog.LevelError,
		Action:  LLMCallFailedAction,
		Message: message,
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id":     event.GroupID,
			"platform":     r.currentPlatform(event),
			"session_kind": string(event.Kind),
			"profile_id":   event.ProfileID,
			"user_id":      event.UserID,
			"message_id":   event.MessageID,
			"provider":     string(failure.profile.Config.Provider),
			"llm_profile":  failure.profile.ID,
			"model":        model,
			"purpose":      strings.TrimSpace(purpose),
			"duration_ms":  failure.elapsed.Milliseconds(),
			"recovered":    recovered,
		},
	}
	if failure.err != nil {
		entry.Detail = truncateRunes(failure.err.Error(), 500)
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	_ = writer.AppendLog(writeCtx, entry)
}
