// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"time"
)

// 兼容旧版保存的发送退避字段。群发送现在固定最多三次、间隔 1s/2s，
// 这些分钟级参数不再参与运行时调度。入站处理失败的重跑上限仍可配置；
// 明确拒收、结果不明和发送预算耗尽不重新生成回复。
const (
	defaultSendBackoffInitialSeconds = int(defaultOutboundInitialDelay / time.Second)
	defaultSendBackoffMaxSeconds     = int(defaultOutboundMaximumDelay / time.Second)
	defaultSendFailureWindowMinutes  = int(defaultOutboundFailureWindow / time.Minute)
	defaultSendDropCooldownMinutes   = int(defaultOutboundDropCooldown / time.Minute)
	defaultInboundRetryMaxAttempts   = inboundMaxAttempts

	minSendBackoffSeconds         = 5
	maxSendBackoffSeconds         = 60 * 60
	maxSendFailureWindowMinutes   = 24 * 60
	maxSendDropCooldownMinutes    = 24 * 60
	maxInboundRetryMaxAttempts    = 20
	minimumInboundRetryMaxAttempt = 1
)

// sendRetrySettings 是两层慢重试的五个参数。BotConfig 和 GroupConfig 各嵌一份，
// 字段名和 JSON 键两边一致，界面和工具读写同一套名字。
type sendRetrySettings struct {
	SendBackoffInitialSeconds int `json:"send_backoff_initial_seconds,omitempty"`
	SendBackoffMaxSeconds     int `json:"send_backoff_max_seconds,omitempty"`
	SendFailureWindowMinutes  int `json:"send_failure_window_minutes,omitempty"`
	SendDropCooldownMinutes   int `json:"send_drop_cooldown_minutes,omitempty"`
	InboundRetryMaxAttempts   int `json:"inbound_retry_max_attempts,omitempty"`
}

func defaultSendRetrySettings() sendRetrySettings {
	return sendRetrySettings{
		SendBackoffInitialSeconds: defaultSendBackoffInitialSeconds,
		SendBackoffMaxSeconds:     defaultSendBackoffMaxSeconds,
		SendFailureWindowMinutes:  defaultSendFailureWindowMinutes,
		SendDropCooldownMinutes:   defaultSendDropCooldownMinutes,
		InboundRetryMaxAttempts:   defaultInboundRetryMaxAttempts,
	}
}

// clamped 把越界值收回合法范围，0 和负数保留为 0（「没填」）。
// 机器人级随后用默认值补空；分群级保留 0 表示跟随。
func (s sendRetrySettings) clamped() sendRetrySettings {
	s.SendBackoffInitialSeconds = clampOptionalInt(s.SendBackoffInitialSeconds, minSendBackoffSeconds, maxSendBackoffSeconds)
	s.SendBackoffMaxSeconds = clampOptionalInt(s.SendBackoffMaxSeconds, minSendBackoffSeconds, maxSendBackoffSeconds)
	s.SendFailureWindowMinutes = clampOptionalInt(s.SendFailureWindowMinutes, 1, maxSendFailureWindowMinutes)
	s.SendDropCooldownMinutes = clampOptionalInt(s.SendDropCooldownMinutes, 1, maxSendDropCooldownMinutes)
	s.InboundRetryMaxAttempts = clampOptionalInt(s.InboundRetryMaxAttempts, minimumInboundRetryMaxAttempt, maxInboundRetryMaxAttempts)
	return s
}

// withFallback 用 base 补齐没填的字段。
func (s sendRetrySettings) withFallback(base sendRetrySettings) sendRetrySettings {
	if s.SendBackoffInitialSeconds <= 0 {
		s.SendBackoffInitialSeconds = base.SendBackoffInitialSeconds
	}
	if s.SendBackoffMaxSeconds <= 0 {
		s.SendBackoffMaxSeconds = base.SendBackoffMaxSeconds
	}
	if s.SendFailureWindowMinutes <= 0 {
		s.SendFailureWindowMinutes = base.SendFailureWindowMinutes
	}
	if s.SendDropCooldownMinutes <= 0 {
		s.SendDropCooldownMinutes = base.SendDropCooldownMinutes
	}
	if s.InboundRetryMaxAttempts <= 0 {
		s.InboundRetryMaxAttempts = base.InboundRetryMaxAttempts
	}
	return s
}

// outboundDeliveryPolicy 换算成群退避闸门用的时长。
func (s sendRetrySettings) outboundDeliveryPolicy() outboundDeliveryPolicy {
	s = s.clamped().withFallback(defaultSendRetrySettings())
	return normalizeOutboundDeliveryPolicy(outboundDeliveryPolicy{
		InitialDelay:  time.Duration(s.SendBackoffInitialSeconds) * time.Second,
		MaximumDelay:  time.Duration(s.SendBackoffMaxSeconds) * time.Second,
		FailureWindow: time.Duration(s.SendFailureWindowMinutes) * time.Minute,
		DropCooldown:  time.Duration(s.SendDropCooldownMinutes) * time.Minute,
	})
}

func (s sendRetrySettings) inboundRetryMaxAttempts() int {
	return s.clamped().withFallback(defaultSendRetrySettings()).InboundRetryMaxAttempts
}

func clampOptionalInt(value, minimum, maximum int) int {
	switch {
	case value <= 0:
		return 0
	case value < minimum:
		return minimum
	case value > maximum:
		return maximum
	default:
		return value
	}
}

// outboundDeliveryPolicyForEvent 取这条消息所在会话的退避策略。测试或特殊调用方
// 在 ctx 里显式放了策略时以 ctx 为准。
func (r *Runtime) outboundDeliveryPolicyForEvent(ctx context.Context, event MessageEvent) outboundDeliveryPolicy {
	if policy, ok := ctx.Value(outboundDeliveryPolicyContextKey{}).(outboundDeliveryPolicy); ok {
		return normalizeOutboundDeliveryPolicy(policy)
	}
	// 旧版分钟级退避参数继续兼容反序列化，但不能再阻塞一个群。
	return outboundDeliveryPolicy{InitialDelay: time.Second, MaximumDelay: 2 * time.Second, FailureWindow: defaultOutboundFailureWindow}
}

// inboundRetryMaxAttemptsForEvent 是这条入站事件最多处理几轮。
func (r *Runtime) inboundRetryMaxAttemptsForEvent(event MessageEvent) int {
	return r.effectiveConfigForEvent(event).sendRetrySettings.inboundRetryMaxAttempts()
}
