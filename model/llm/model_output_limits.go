// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "strings"

// 输出上限和上下文窗口不是一回事，所以这里敢建表，窗口那边不敢（见
// context_window_source.go）：
//
//   - 窗口猜大了，请求直接超限被拒；输出上限要的是「允许写到多长」，按模型上限要
//     并不会多花钱，只是不再替模型提前截断。
//   - 不填并不等于「按模型最大」。Anthropic 必须给 max_tokens；Gemini 网关看到缺省
//     会自己补一个（antigravity 补成 9216）；DeepSeek 这类 Chat Completions 接口的
//     缺省值也远小于模型上限。模型把整份文件写进工具参数时，就在这些缺省值上被截断。
//   - 同步下来的模型清单靠不住：antigravity 给每个模型都报 8192，是占位值。
//
// 数字取自各家官方文档，由 models.dev 汇总核对（2026-09）。只收主流厂商、且家族内
// 上限一致的条目；各家把 output 写成和 context 一样大的（多半是占位）不收。

// builtinMaxOutputTokens 按模型家族前缀给出输出上限，最长前缀优先。键已按
// normalizeModelFamily 归一：小写、去掉命名空间、点和下划线都换成连字符。
var builtinMaxOutputTokens = map[string]int64{
	// Anthropic
	"claude-fable-5":    128000,
	"claude-opus-5":     128000,
	"claude-sonnet-5":   128000,
	"claude-opus-4-8":   128000,
	"claude-opus-4-7":   128000,
	"claude-opus-4-6":   128000,
	"claude-opus-4-5":   64000,
	"claude-opus-4":     32000,
	"claude-sonnet-4-6": 128000,
	"claude-sonnet-4-5": 64000,
	"claude-sonnet-4":   64000,
	"claude-haiku-4-5":  64000,
	"claude-3-7-sonnet": 64000,
	"claude-3-5":        8192,

	// Google
	"gemini-3":                 65536,
	"gemini-3-pro-image":       32768,
	"gemini-2-5":               65536,
	"gemini-2-5-flash-image":   32768,
	"gemini-flash-latest":      65536,
	"gemini-flash-lite-latest": 65536,
	"gemini-2-0":               8192,
	"gemma-4":                  32768,

	// OpenAI
	"gpt-6":               128000,
	"gpt-5":               128000,
	"gpt-5-pro":           272000,
	"gpt-5-chat":          16384,
	"gpt-5-2-chat":        16384,
	"gpt-5-3-chat":        16384,
	"gpt-5-3-codex-spark": 32000,
	"gpt-4-1":             32768,
	"gpt-4o":              16384,
	"o3":                  100000,
	"o4-mini":             100000,

	// DeepSeek
	"deepseek-v4":    384000,
	"deepseek-flash": 384000,

	// 阿里 Qwen
	"qwen3-max":   65536,
	"qwen3-coder": 65536,
	"qwen3-5":     65536,
	"qwen3-6":     65536,
	"qwen3-7":     65536,
	"qwen3-8":     131072,
	"qwen-flash":  32768,

	// 月之暗面 Kimi
	"kimi-k2-6": 262144,
	"kimi-k2-7": 262144,
	"kimi-k3":   131072,

	// 智谱 GLM
	"glm-4-5":  98304,
	"glm-4-5v": 16384,
	"glm-4-6":  131072,
	"glm-4-6v": 32768,
	"glm-4-7":  131072,
	"glm-5":    131072,

	// MiniMax
	"minimax-m2": 131072,
}

// BuiltinMaxOutputTokens 返回内置表里这个模型的输出上限。网关给模型名加的后缀
// （-low、-thinking、日期版本）落在家族前缀之后，照样能匹配上。
func BuiltinMaxOutputTokens(model string) (int64, bool) {
	name := normalizeModelFamily(model)
	if name == "" {
		return 0, false
	}
	best, bestLen := int64(0), 0
	for prefix, limit := range builtinMaxOutputTokens {
		if len(prefix) <= bestLen || !strings.HasPrefix(name, prefix) {
			continue
		}
		// 前缀后面必须是分隔符或结尾：gpt-5 不能吃掉 gpt-50，o3 不能吃掉 o3x。
		if len(name) > len(prefix) && name[len(prefix)] != '-' {
			continue
		}
		best, bestLen = limit, len(prefix)
	}
	return best, bestLen > 0
}

func normalizeModelFamily(model string) string {
	name := bareModelName(model)
	return strings.NewReplacer(".", "-", "_", "-").Replace(name)
}

// MaxOutputTokensSource 说明没被调用方覆盖时，请求里的输出上限从哪来，供界面如实标注。
type MaxOutputTokensSource string

const (
	// MaxOutputTokensSourceUser 是配置档里显式填的值。
	MaxOutputTokensSourceUser MaxOutputTokensSource = "user"
	// MaxOutputTokensSourceBuiltin 是内置表里这个模型的上限。
	MaxOutputTokensSourceBuiltin MaxOutputTokensSource = "builtin"
	// MaxOutputTokensSourceCatalog 是同步下来的模型清单里记的上限。
	MaxOutputTokensSourceCatalog MaxOutputTokensSource = "catalog"
	// MaxOutputTokensSourceFallback 是协议级兜底值（Gemini 65536、Anthropic 1024）。
	MaxOutputTokensSourceFallback MaxOutputTokensSource = "fallback"
	// MaxOutputTokensSourceProvider 表示不发这个字段，由服务端按模型处理。
	MaxOutputTokensSourceProvider MaxOutputTokensSource = "provider"
)

// ResolveMaxOutputTokens 返回调用方没覆盖时实际发出的输出上限和来源。返回 0 表示
// 不发这个字段。发出的值还会按上下文剩余空间收一次（Gemini 除外，它的输入输出
// 上限分开算），这里给的是收之前的值。
func (cfg ProviderConfig) ResolveMaxOutputTokens(model string) (int64, MaxOutputTokensSource) {
	if cfg.MaxOutputTokens > 0 {
		return cfg.MaxOutputTokens, MaxOutputTokensSourceUser
	}
	if strings.TrimSpace(model) == "" {
		model = cfg.Model
	}
	switch cfg.Provider {
	case ProviderGemini:
		if limit, ok := BuiltinMaxOutputTokens(model); ok {
			return limit, MaxOutputTokensSourceBuiltin
		}
		// 不认识的名字按 Gemini 现行上限要；上限更低的模型会以 400 拒绝，届时去掉
		// 字段重发。宁可要多被拒一次，也不要被网关的缺省值悄悄截断。
		return int64(geminiFallbackMaxOutputTokens), MaxOutputTokensSourceFallback
	case ProviderAnthropic:
		if limit, ok := BuiltinMaxOutputTokens(model); ok {
			return limit, MaxOutputTokensSourceBuiltin
		}
		if info, ok := cfg.ModelInfoFor(model); ok && info.MaxOutputTokens > 0 {
			return info.MaxOutputTokens, MaxOutputTokensSourceCatalog
		}
		return defaultAnthropicMaxTokens, MaxOutputTokensSourceFallback
	case ProviderOpenAICompatible:
		// Responses API 缺省就是模型上限，而 Codex 这类订阅网关会拒掉这个字段，不发。
		// Chat Completions 的缺省值普遍偏小（DeepSeek 默认 4096），按表发；被拒时由
		// 参数降级摘掉并记住。
		if cfg.APIFormatWithDefault() == APIFormatChatCompletions {
			if limit, ok := BuiltinMaxOutputTokens(model); ok {
				return limit, MaxOutputTokensSourceBuiltin
			}
		}
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
