// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "github.com/SuInk/diana/internal/secretmask"

// RegisterProviderSecrets 把一份 provider 配置里的凭据登记给 secretmask：API Key、
// 自定义请求头的值、base URL 里嵌着的 userinfo 和查询参数。
//
// 这些值不该出现在任何交给模型或聊天对象的文本里，但它们会从别的路子漏进去：
// SDK 的 HTTP 报错带着整条请求地址和响应体，中转网关的报错回显请求头，owner
// 通过 config、llm_config 工具看到的上次失败原因。登记之后，工具结果、报错和
// 外发消息里再出现原文就会被换成掩码，见 internal/secretmask。
func RegisterProviderSecrets(cfg ProviderConfig) {
	secretmask.Register(cfg.APIKey)
	// 自定义头本来就是为了塞鉴权信息才配的，普通头（User-Agent 之类）有专门的字段，
	// 所以不看头名，值一律登记。
	for _, value := range cfg.Headers {
		secretmask.Register(value)
	}
	secretmask.RegisterURL(cfg.BaseURL, cfg.ImageBaseURL)
}
