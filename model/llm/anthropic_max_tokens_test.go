package llm

import "testing"

// TestAnthropicMaxTokensPrefersCatalogOverConstant 盯住一个删不掉的参数怎么取值。
//
// 别的适配器在没配置时干脆不发 max_tokens；Anthropic 的 Messages API 把它列为必填，
// 所以这里躲不掉，只能给一个数。给错的代价很具体：写死的 1024 会把回复截断，而会
// 思考的模型先写 reasoning 再写正文，额度可能在思考阶段就用光，返回里只剩 reasoning
// 没有正文——同一个失败模式在上下文摘要压缩那条路上已经出现过一次。
func TestAnthropicMaxTokensPrefersCatalogOverConstant(t *testing.T) {
	cfg := ProviderConfig{
		Model: "claude-sonnet-4-6",
		Models: []ModelInfo{
			{ID: "claude-sonnet-4-6", MaxOutputTokens: 64000},
			{ID: "claude-haiku-3", MaxOutputTokens: 4096},
		},
	}
	for _, item := range []struct {
		name string
		req  GenerateRequest
		want int64
	}{
		{"用户显式配置优先于一切", GenerateRequest{Model: "claude-sonnet-4-6", MaxOutputTokens: 2048}, 2048},
		{"没配置时取清单里这个模型报的上限", GenerateRequest{Model: "claude-sonnet-4-6"}, 64000},
		{"换个模型就换成那个模型的上限", GenerateRequest{Model: "claude-haiku-3"}, 4096},
		{"清单里没有这个模型才退回常量", GenerateRequest{Model: "claude-unknown"}, defaultAnthropicMaxTokens},
	} {
		t.Run(item.name, func(t *testing.T) {
			if got := anthropicMaxTokens(cfg, item.req); got != item.want {
				t.Fatalf("anthropicMaxTokens = %d, want %d", got, item.want)
			}
		})
	}
}

// TestAnthropicMaxTokensIgnoresCatalogWithoutLimit 清单里有这个模型但没写上限时，
// 不能把 0 当成有效值发出去——Anthropic 会直接拒掉。
func TestAnthropicMaxTokensIgnoresCatalogWithoutLimit(t *testing.T) {
	cfg := ProviderConfig{Models: []ModelInfo{{ID: "relay-model"}}}
	if got := anthropicMaxTokens(cfg, GenerateRequest{Model: "relay-model"}); got != defaultAnthropicMaxTokens {
		t.Fatalf("anthropicMaxTokens = %d, want %d", got, defaultAnthropicMaxTokens)
	}
}
