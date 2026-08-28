// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "context"

// CredentialResolver 由 OAuth 层实现，按提供商标识给出当前可用凭据。
//
// 定义在这里而不是直接依赖 model/llmauth，是为了不让 llm 反过来依赖上层：
// llm 只需要知道「有人能按 key 给我一份凭据」。
type CredentialResolver interface {
	Credential(ctx context.Context, providerKey string) (Credential, error)
}

// ClientOptionsFor 按配置档挑客户端选项。
//
// 配置档没绑 OAuth 提供商，或者根本没有 OAuth 层时，返回空——此时行为与
// 引入 OAuth 之前完全一致，连 HTTP 客户端都不会被包一层。
func ClientOptionsFor(cfg ProviderConfig, resolver CredentialResolver) []ClientOption {
	if resolver == nil || cfg.OAuthProvider == "" {
		return nil
	}
	key := cfg.OAuthProvider
	return []ClientOption{WithCredentialSource(CredentialSourceFunc(func(ctx context.Context) (Credential, error) {
		return resolver.Credential(ctx, key)
	}))}
}
