// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "sync/atomic"

// 输出上限能不发就不发：用户没填时交给服务端按模型处理。发一个猜的数没有好处——
// 猜大了撞上智谱 glm-4v-flash 这种只收 [1,1024] 的就是 400，猜小了悄悄截断。
// 唯一例外是 Anthropic，它的 max_tokens 是必填，只能按封顶发；超出模型上限时
// 按报错里写的上限重发（anthropic.go）。

// DefaultOutputTokenCeiling 是 Anthropic 必须代发时用的值，和 opencode 的
// OUTPUT_TOKEN_MAX 一致。config.yaml 的 llm_runtime.output_token_max
// 可以改它（opencode 对应的是环境变量 OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX）。
const DefaultOutputTokenCeiling int64 = 32000

var outputTokenCeiling atomic.Int64

// SetOutputTokenCeiling 设置代发值的封顶，0 或负数恢复默认值。启动时由 config.yaml 调用。
func SetOutputTokenCeiling(limit int64) {
	if limit <= 0 {
		limit = 0
	}
	outputTokenCeiling.Store(limit)
}

// OutputTokenCeiling 返回当前生效的封顶。
func OutputTokenCeiling() int64 {
	if limit := outputTokenCeiling.Load(); limit > 0 {
		return limit
	}
	return DefaultOutputTokenCeiling
}

// MaxOutputTokensSource 说明没被调用方覆盖时，请求里的输出上限从哪来，供界面如实标注。
type MaxOutputTokensSource string

const (
	// MaxOutputTokensSourceUser 是配置档里显式填的值。
	MaxOutputTokensSourceUser MaxOutputTokensSource = "user"
	// MaxOutputTokensSourceDefault 是 Anthropic 必须代发时按封顶发的值。
	MaxOutputTokensSourceDefault MaxOutputTokensSource = "default"
	// MaxOutputTokensSourceProvider 表示不发这个字段，由服务端按模型处理。
	MaxOutputTokensSourceProvider MaxOutputTokensSource = "provider"
)

// ResolveMaxOutputTokens 返回调用方没覆盖时实际发出的输出上限和来源。返回 0 表示
// 不发这个字段。发出的值还会按上下文剩余空间收一次，这里给的是收之前的值。model
// 留着给调用方传当前模型，取值本身不看模型。
func (cfg ProviderConfig) ResolveMaxOutputTokens(_ string) (int64, MaxOutputTokensSource) {
	if cfg.MaxOutputTokens > 0 {
		return cfg.MaxOutputTokens, MaxOutputTokensSourceUser
	}
	if cfg.Provider == ProviderAnthropic {
		return OutputTokenCeiling(), MaxOutputTokensSourceDefault
	}
	return 0, MaxOutputTokensSourceProvider
}

// withImplicitMaxOutputTokens 在调用方和配置档都没给上限时，把代发值写进请求。
// 必须在 applyContextBudget 之后调用：预算只为输出预留 DefaultMaxOutputTokens，
// 代发值不参与预算，否则 128K 的兜底窗口会被 128K 的输出上限整个吃掉。
//
// clampToContext 为 true 时按上下文剩余空间收紧：这些协议把输出算在窗口里，
// 输入加上限超过窗口会被拒。预算已经保证至少留出 DefaultMaxOutputTokens。
//
// provider 由适配器按自己的协议给出，不信配置里的 Provider：直接构造的配置可能没填。
func (cfg ProviderConfig) withImplicitMaxOutputTokens(provider Provider, req GenerateRequest, clampToContext bool) GenerateRequest {
	if req.MaxOutputTokens > 0 {
		return req
	}
	cfg.Provider = provider
	limit, _ := cfg.ResolveMaxOutputTokens(req.Model)
	if limit <= 0 {
		return req
	}
	if clampToContext && req.MaxContextTokens > 0 {
		room := req.MaxContextTokens - estimateRequestInputTokens(req) - contextBudgetSafetyReserve
		if room < DefaultMaxOutputTokens {
			room = DefaultMaxOutputTokens
		}
		if limit > room {
			limit = room
		}
	}
	req.MaxOutputTokens = limit
	req.implicitMaxOutputTokens = true
	return req
}

func estimateRequestInputTokens(req GenerateRequest) int64 {
	total := estimateToolDefinitionsTokens(req.Tools, req.ToolChoice)
	for _, message := range req.Messages {
		total += estimateMessageTokens(message)
	}
	return total
}
