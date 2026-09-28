// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"strings"
	"sync/atomic"
)

// 输出上限的取法照搬 opencode（packages/opencode/src/provider/transform.ts 的
// maxOutputTokens）：min(models.dev 里这个模型的 limit.output, 32000)，查不到就是
// 32000。
//
//   - 不填并不等于「按模型最大」。Anthropic 必须给 max_tokens；Gemini 网关看到缺省
//     会自己补一个（antigravity 补成 9216）；Chat Completions 接口的缺省值也远小于
//     模型上限。模型把整份文件写进工具参数时，就在这些缺省值上被截断。
//   - 同步下来的模型清单靠不住：antigravity 给每个模型都报 8192，是占位值。所以只信
//     models.dev，不信网关自己报的数。
//   - 封顶是为了不无限要：DeepSeek 384K 这类上限要满了只会让 Anthropic SDK 这类按上限
//     估算耗时的客户端拒绝请求。

// DefaultOutputTokenCeiling 是代发值的默认封顶，也是 models.dev 查不到时的默认值，
// 和 opencode 的 OUTPUT_TOKEN_MAX 一致。config.yaml 的 llm_runtime.output_token_max
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
	// MaxOutputTokensSourceModelsDev 是 models.dev 里这个模型的上限（按 OutputTokenCeiling 封顶）。
	MaxOutputTokensSourceModelsDev MaxOutputTokensSource = "models_dev"
	// MaxOutputTokensSourceDefault 是 models.dev 查不到这个模型时的默认值。
	MaxOutputTokensSourceDefault MaxOutputTokensSource = "default"
	// MaxOutputTokensSourceProvider 表示不发这个字段，由服务端按模型处理。
	MaxOutputTokensSourceProvider MaxOutputTokensSource = "provider"
)

// ResolveMaxOutputTokens 返回调用方没覆盖时实际发出的输出上限和来源。返回 0 表示
// 不发这个字段。发出的值还会按上下文剩余空间收一次（Gemini 除外，它的输入输出
// 上限分开算），这里给的是收之前的值。
//
// 默认值要多了会被拒，各协议都有退路：Gemini 去掉字段重发，Anthropic 按报错里给的
// 上限重发，Chat Completions 由参数降级摘掉并记住。要少了则是悄悄截断，没有退路。
func (cfg ProviderConfig) ResolveMaxOutputTokens(model string) (int64, MaxOutputTokensSource) {
	if cfg.MaxOutputTokens > 0 {
		return cfg.MaxOutputTokens, MaxOutputTokensSourceUser
	}
	if strings.TrimSpace(model) == "" {
		model = cfg.Model
	}
	// Responses API 缺省就是模型上限，而 Codex 这类订阅网关会拒掉这个字段，不发。
	if cfg.Provider == ProviderOpenAICompatible && cfg.APIFormatWithDefault() != APIFormatChatCompletions {
		return 0, MaxOutputTokensSourceProvider
	}
	ceiling := OutputTokenCeiling()
	if limit, ok := outputLimitCatalog.OutputLimit(cfg, model); ok {
		return min(limit, ceiling), MaxOutputTokensSourceModelsDev
	}
	return ceiling, MaxOutputTokensSourceDefault
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
