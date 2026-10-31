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

// 改图时源图可以是用户或模型给出的任意 http(s) 链接。
// 提供商自己的同源文件使用配置的 API Key 和请求头，并拒绝跳出服务地址的重定向；
// 其余链接使用不带授权的公网客户端，逐跳校验并阻止访问私有地址。

// imageEditDownloadTimeout 是下载一张源图的上限，和其他下载器的量级一致；调用方的
// ctx 期限更短时以 ctx 为准。
const imageEditDownloadTimeout = 30 * time.Second

// imageEditSource 决定一次源图下载用哪个客户端。
type imageEditSource struct {
	// providerClient 只用于提供商的同源链接。
	providerClient *http.Client
	headers        http.Header
	// origins 是配置档里服务地址的源（协议、主机、端口）。
	origins []*url.URL
}

func newImageEditSource(cfg ProviderConfig, providerClient *http.Client) imageEditSource {
	if providerClient == nil {
		providerClient = http.DefaultClient
	}
	source := imageEditSource{providerClient: providerClient, headers: make(http.Header)}
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		switch cfg.Provider {
		case ProviderGemini:
			source.headers.Set("X-Goog-Api-Key", key)
		case ProviderOpenAICompatible:
			source.headers.Set("Authorization", "Bearer "+key)
		}
	}
	for name, value := range cfg.NormalizedHeaders() {
		source.headers.Set(name, value)
	}
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
	if s.providerClient != nil && s.sameOrigin(target) {
		client := *s.providerClient
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
