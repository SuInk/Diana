// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

// 插件设置页「测试登录凭据」按钮的结果。每家平台各拿一个自己账号信息的接口实测，
// 不借道解析一条真实链接：链接解析失败有十几种原因，只有「登录态接口说没登录」
// 才能直接指到「换 Cookie」这一步。
const (
	CredentialValid        = "valid"
	CredentialInvalid      = "invalid"
	CredentialUnverified   = "unverified"
	CredentialUnconfigured = "unconfigured"
	CredentialError        = "error"
)

// CredentialCheck 可以原样回给设置页，只带昵称这类账号标识，从不回显凭据本身。
type CredentialCheck struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Configured bool   `json:"configured"`
	State      string `json:"state"`
	Account    string `json:"account,omitempty"`
	Message    string `json:"message"`
	// MembershipExpired 表示登录有效但会员已过期。只在服务端内部用：音乐连接测试
	// 靠它把「测试曲放不了」归因到会员，而不是笼统地说版权限制。
	MembershipExpired bool `json:"-"`
}

const credentialCheckTimeout = 15 * time.Second

func unconfiguredCredential(key, label string) CredentialCheck {
	return CredentialCheck{Key: key, Label: label, State: CredentialUnconfigured, Message: "未填写"}
}

// fetchCredentialProbe 取一次账号接口。非 2xx 也把正文读回来：有的平台把
// 「未登录」放在 4xx 里，只看状态码会把失效误报成网络问题。
func fetchCredentialProbe(ctx context.Context, client *http.Client, endpoint string, headers map[string]string) (int, string, error) {
	if client == nil {
		client = netguard.NewPublicHTTPClient(credentialCheckTimeout)
	}
	ctx, cancel := context.WithTimeout(ctx, credentialCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, "", err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	return resp.StatusCode, string(body), err
}

// pastedCookieNameHint 提示「只该填值却连字段名一起贴进来」。SESSDATA、MUSIC_U
// 这类设置在发送时会自己补上字段名，贴成 SESSDATA=xxx 就变成 SESSDATA=SESSDATA=xxx，
// 接口只会回一句「未登录」，看不出是填法错了。
func pastedCookieNameHint(value, name string) string {
	value = strings.TrimSpace(value)
	if strings.Contains(value, ";") || strings.HasPrefix(strings.ToUpper(value), strings.ToUpper(name)+"=") {
		return "这里只填 " + name + " 的值，不要带「" + name + "=」或其他 Cookie 字段。"
	}
	return ""
}

func withCredentialHint(message, hint string) string {
	if hint == "" {
		return message
	}
	return message + " " + hint
}
