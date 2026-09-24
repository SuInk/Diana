// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/genai"
)

// 用 OAuth 登录的配置档，登录令牌能调哪些接口由服务商说了算。一部分服务商只给
// 对话接口授权，生图、改图接口对这把令牌回 401/403，或者干脆没有这个路由（404）。
// image 用途没单独配置时默认沿用 chat 的配置档，这种失败会以「鉴权失败」「接口不存在」
// 的原文出现，看上去像 Key 填错了。所以在这里补一句：原因多半是这家的登录令牌不开放
// 生图，换一个用 API Key 的配置档给 image 用途。

// OAuthImageUnsupportedError 表示 OAuth 配置档的生图或改图请求被上游拒绝，
// 而拒绝的方式看起来是「这把令牌不能调这个接口」。
type OAuthImageUnsupportedError struct {
	// OAuthProvider 是配置档绑定的 OAuth 提供商标识。
	OAuthProvider string
	// Action 是「生图」或「改图」。
	Action     string
	StatusCode int
	Err        error
}

func (e *OAuthImageUnsupportedError) Error() string {
	return fmt.Sprintf("llm: 这个配置档通过 OAuth 登录（%s），%s请求被上游拒绝（HTTP %d）：这家服务商的登录令牌可能不开放%s接口，请给 image 用途单独绑定一个用 API Key 的配置档。上游原文：%v",
		e.OAuthProvider, e.Action, e.StatusCode, e.Action, e.Err)
}

func (e *OAuthImageUnsupportedError) Unwrap() error { return e.Err }

// oauthImageError 只改写 OAuth 配置档上「像是接口不开放」的那几种失败，其余原样返回。
// 没绑 OAuth 的配置档不经过这里，报错和以前一字不差。
func oauthImageError(cfg ProviderConfig, action string, err error) error {
	provider := strings.TrimSpace(cfg.OAuthProvider)
	if err == nil || provider == "" {
		return err
	}
	var credentialErr *OAuthCredentialError
	if errors.As(err, &credentialErr) {
		// 没登录、登录过期已经说清楚了，别再猜成「不开放接口」。
		return err
	}
	status := imageErrorStatusCode(err)
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return &OAuthImageUnsupportedError{OAuthProvider: provider, Action: action, StatusCode: status, Err: err}
	default:
		return err
	}
}

func imageErrorStatusCode(err error) int {
	var requestErr *openAIRequestError
	if errors.As(err, &requestErr) {
		return requestErr.statusCode
	}
	var geminiErr genai.APIError
	if errors.As(err, &geminiErr) {
		return geminiErr.Code
	}
	var geminiPtrErr *genai.APIError
	if errors.As(err, &geminiPtrErr) && geminiPtrErr != nil {
		return geminiPtrErr.Code
	}
	return 0
}
