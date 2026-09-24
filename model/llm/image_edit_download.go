// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

// 改图时源图可以是一条 http(s) 链接，链接来自用户或模型，指向哪里都有可能。
//
// 提供商的 HTTP 客户端在传输层自动带上 OAuth 令牌（见 credentials.go），拿它去下载
// 任意链接，令牌就跟着发给了链接背后的那台主机；它也不拦内网地址，一条指向
// 127.0.0.1、云厂商元数据服务的链接照样能取回来。所以分两种：
//
//   - 链接和配置档里的服务地址同源：这是提供商自己的文件（上一轮生成的图），用带
//     凭据的客户端取，但不跟随跳到别处的重定向——令牌是在每一跳上重新注入的。
//   - 其余一律走 netguard 的公网客户端：不带任何凭据，拦私有地址、每一跳重定向都
//     重新校验，和其他插件下载图片用的是同一道防线。

// imageEditDownloadTimeout 是下载一张源图的上限，和其他下载器的量级一致；调用方的
// ctx 期限更短时以 ctx 为准。
const imageEditDownloadTimeout = 30 * time.Second

// imageEditSource 决定一次源图下载用哪个客户端。
type imageEditSource struct {
	// credentialed 是提供商自己的客户端，只用于同源链接。
	credentialed *http.Client
	// origins 是配置档里服务地址的源（协议、主机、端口）。
	origins []*url.URL
}

func newImageEditSource(cfg ProviderConfig, credentialed *http.Client) imageEditSource {
	source := imageEditSource{credentialed: credentialed}
	for _, raw := range []string{cfg.BaseURL, cfg.ImageBaseURL, defaultProviderBaseURL(cfg.Provider)} {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			continue
		}
		source.origins = append(source.origins, parsed)
	}
	return source
}

// defaultProviderBaseURL 是没填服务地址时各家实际请求的地址。
func defaultProviderBaseURL(provider Provider) string {
	switch provider {
	case ProviderOpenAICompatible:
		return "https://api.openai.com/v1"
	case ProviderGemini:
		return "https://generativelanguage.googleapis.com"
	}
	return ""
}

func (s imageEditSource) sameOrigin(target *url.URL) bool {
	for _, origin := range s.origins {
		if sameURLOrigin(origin, target) {
			return true
		}
	}
	return false
}

// clientFor 给一条源图链接挑客户端。
func (s imageEditSource) clientFor(target *url.URL) *http.Client {
	if s.credentialed != nil && s.sameOrigin(target) {
		client := *s.credentialed
		if client.Timeout == 0 || client.Timeout > imageEditDownloadTimeout {
			client.Timeout = imageEditDownloadTimeout
		}
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("llm: image download stopped after too many redirects")
			}
			if !s.sameOrigin(req.URL) {
				return errors.New("llm: image download redirected off the provider host; credentials are not forwarded")
			}
			return nil
		}
		return &client
	}
	return netguard.NewPublicHTTPClient(imageEditDownloadTimeout)
}

func sameURLOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectiveURLPort(left) == effectiveURLPort(right)
}

func effectiveURLPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	return "80"
}
