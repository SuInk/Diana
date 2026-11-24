// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// 网易云的 MUSIC_U 也会过期。客户端和 NeteaseCloudMusicApi 的 /login/refresh 都是调
// /api/login/token/refresh 续：带着 MUSIC_U 请求，code 200 时新的 MUSIC_U 在 Set-Cookie
// 里，没登录回 301。这个接口不加密也认，和 Diana 其它网易云请求一样走明文 /api/。
//
// MUSIC_U 本身看不出生成时间，所以按「这份 Cookie 上次续是什么时候」来：同一份每天
// 最多续一次，续失败的过一阵再试。

const (
	neteaseRefreshAPI = "https://music.163.com/api/login/token/refresh"
	// neteaseCredentialRenewInterval 是同一份 MUSIC_U 多久续一次。
	neteaseCredentialRenewInterval = 24 * time.Hour
	// neteaseCredentialRetryInterval 是续期失败后多久再试，网络抖动不该等一整天。
	neteaseCredentialRetryInterval = 30 * time.Minute
)

// freshCredentials 到点就续一次 MUSIC_U，续出新值就写回设置，返回这次请求该用的 cfg。
// 续不上不算错：MUSIC_U 多半还能用，真失效了凭据检查和点歌失败会照常报。
func (s *neteaseSource) freshCredentials(ctx context.Context, f *musicFetcher, cfg musicConfig) musicConfig {
	cookie := cfg.sourceOptions(s.Key()).Cookie
	if cookie == "" || s.refreshAPI == "" {
		return cfg
	}
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	if s.refreshedFrom == cookie && s.refreshedTo != "" {
		return cfg.withSourceCookie(s.Key(), s.refreshedTo)
	}
	if s.renewedCookie == cookie && time.Now().Before(s.nextRenew) {
		return cfg
	}
	fresh, err := s.refreshCredential(ctx, f, cfg)
	if err != nil {
		log.Printf("music netease credential refresh failed: %v", err)
		s.renewedCookie, s.nextRenew = cookie, time.Now().Add(neteaseCredentialRetryInterval)
		return cfg
	}
	s.renewedCookie, s.nextRenew = fresh, time.Now().Add(neteaseCredentialRenewInterval)
	if fresh == cookie {
		return cfg
	}
	s.refreshedFrom, s.refreshedTo = cookie, fresh
	if f != nil && f.saveCookie != nil {
		if err := f.saveCookie(ctx, s.Key(), fresh); err != nil {
			log.Printf("music netease refreshed cookie not saved: %v", err)
		}
	}
	return cfg.withSourceCookie(s.Key(), fresh)
}

// refreshCredential 返回续期后的 MUSIC_U；服务端没下发新值时原样返回旧的。
func (s *neteaseSource) refreshCredential(ctx context.Context, f *musicFetcher, cfg musicConfig) (string, error) {
	ctx, diagnostics := withMusicDiagnostics(ctx)
	headers := s.songURLHeaders(cfg)
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	var payload struct {
		Code int `json:"code"`
	}
	header, ok := f.postJSON(ctx, cfg, s.refreshAPI, nil, true, headers, &payload)
	if !ok {
		if diagnostics.failed() {
			return "", errors.New(diagnostics.failures[len(diagnostics.failures)-1])
		}
		return "", errors.New("请求续期接口失败")
	}
	if payload.Code != http.StatusOK {
		return "", fmt.Errorf("续期接口返回 code %d", payload.Code)
	}
	for _, cookie := range (&http.Response{Header: header}).Cookies() {
		if cookie.Name == "MUSIC_U" && strings.TrimSpace(cookie.Value) != "" {
			return strings.TrimSpace(cookie.Value), nil
		}
	}
	return cfg.sourceOptions(s.Key()).Cookie, nil
}
