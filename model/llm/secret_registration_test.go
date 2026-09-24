// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/internal/secretmask"
)

// 建客户端时登记配置档里的凭据：SDK 报错会带出请求地址（base URL 里的 userinfo、
// 查询参数）和中转网关回显的请求头，这些报错会进 owner 能看的 config/llm_config
// 工具结果和聊天里的错误说明。
func TestNewClientRegistersProviderSecrets(t *testing.T) {
	apiKey := "sk-" + "provider0123456789abcdef"
	gatewayPassword := "gw" + "0123456789abcd"
	queryKey := "qk" + "0123456789abcdef"
	headerToken := "hdr" + "0123456789abcdef"
	_, err := NewClient(ProviderConfig{
		Provider: ProviderOpenAICompatible,
		APIKey:   apiKey,
		BaseURL:  "https://" + "relay:" + gatewayPassword + "@relay.example/v1?api_key=" + queryKey,
		Model:    "m",
		Headers:  map[string]string{"X-Relay-Auth": headerToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := `POST "https://relay.example/v1/chat/completions": 401 {"error":"bad key ` + apiKey + `","auth":"` + headerToken + `"} password ` + gatewayPassword + " query " + queryKey
	got := secretmask.Known(text)
	for _, secret := range []string{apiKey, gatewayPassword, queryKey, headerToken} {
		if strings.Contains(got, secret) {
			t.Fatalf("配置档凭据 %s 没登记：%s", secret, got)
		}
	}
	if !strings.Contains(got, "relay.example/v1/chat/completions") {
		t.Fatalf("地址本身不是秘密：%s", got)
	}
}
