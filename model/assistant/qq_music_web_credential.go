// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// 从 y.qq.com 复制的 QQ 登录 Cookie 也能续期。网页自己就是这么续的：拿 Cookie 里的
// psrf_qqrefresh_token / psrf_qqaccess_token 调 musicu 的 QQConnectLogin.LoginServer
// QQLogin，换回新的 musickey 和一对新 token。浏览器开着页面会定时续，复制给 Diana
// 的只是一张快照，没人续就会过期，表现是搜索回 code 2001、账号接口回 1000。
//
// 实测（2026-11）：接口回的 keyExpiresIn 是 259200，musickey 三天过期；过期十几天的
// Cookie 照样续得回来，作废的只是 musickey，refresh_token 还能用。

const (
	qqWebLoginModule = "QQConnectLogin.LoginServer"
	qqWebLoginMethod = "QQLogin"
	// qqWebCredentialRenewAge 是 musickey 生成后多久续一次，离三天的有效期留足余量。
	qqWebCredentialRenewAge = 24 * time.Hour
	// qqWebCredentialLifetime 是 musickey 的有效期。没到期时续期失败只记日志、
	// 照用旧 key，过了这个时间续不上才算凭据失效。
	qqWebCredentialLifetime = 72 * time.Hour
)

// qqWebRefreshableCookie 判断是不是网页 QQ 登录的 Cookie：续期要的几项都得在。
// 微信登录的网页 Cookie 走另一套接口，不在这里续。
func qqWebRefreshableCookie(raw string) bool {
	values := musicCookieValues(raw)
	if values["psrf_qqrefresh_token"] == "" || values["psrf_qqaccess_token"] == "" || values["psrf_qqopenid"] == "" {
		return false
	}
	return qqUINFromCookie(raw) != "0" && firstNonEmpty(values["qqmusic_key"], values["qm_keyst"]) != ""
}

// qqWebCredentialOlder 看 musickey 生成后是否已过了 age。没有生成时间的按已过处理：
// 续一次就有了。
func qqWebCredentialOlder(raw string, age time.Duration, now time.Time) bool {
	created, _ := strconv.ParseInt(musicCookieValues(raw)["psrf_musickey_createtime"], 10, 64)
	if created <= 0 {
		return true
	}
	return !now.Before(time.Unix(created, 0).Add(age))
}

type qqWebLoginData struct {
	MusicKey           string `json:"musickey"`
	MusicKeyCreateTime int64  `json:"musickeyCreateTime"`
	RefreshToken       string `json:"refresh_token"`
	AccessToken        string `json:"access_token"`
	ExpiredAt          int64  `json:"expired_at"`
	OpenID             string `json:"openid"`
	UnionID            string `json:"unionid"`
	EncryptUin         string `json:"encryptUin"`
}

func (s *qqSource) refreshWebCredential(ctx context.Context, f *musicFetcher, cfg musicConfig, cookie string) (string, error) {
	ctx, diagnostics := withMusicDiagnostics(ctx)
	values := musicCookieValues(cookie)
	uin := qqUINFromCookie(cookie)
	musicID, _ := strconv.ParseInt(uin, 10, 64)
	expiredAt, _ := strconv.ParseInt(values["psrf_access_token_expiresat"], 10, 64)
	createTime, _ := strconv.ParseInt(values["psrf_musickey_createtime"], 10, 64)
	comm := map[string]any{"uin": uin, "format": "json", "ct": 24, "cv": 0, "tmeLoginType": firstNonEmpty(values["tmelogintype"], "2")}
	body, err := json.Marshal(qqMusicuRequest(comm, qqWebLoginModule, qqWebLoginMethod, map[string]any{
		"onlyNeedAccessToken": 0,
		"forceRefreshToken":   0,
		"psrf_qqopenid":       values["psrf_qqopenid"],
		"refresh_token":       values["psrf_qqrefresh_token"],
		"access_token":        values["psrf_qqaccess_token"],
		"expired_at":          expiredAt,
		"musicid":             musicID,
		"musickey":            firstNonEmpty(values["qqmusic_key"], values["qm_keyst"]),
		"musickeyCreateTime":  createTime,
		"unionid":             values["psrf_qqunionid"],
		"str_musicid":         uin,
		"encryptUin":          values["euin"],
	}))
	if err != nil {
		return "", err
	}
	var payload struct {
		Req0 struct {
			Code int            `json:"code"`
			Data qqWebLoginData `json:"data"`
		} `json:"req_0"`
	}
	headers := s.headers(cfg)
	headers["Content-Type"] = "application/json"
	endpoint, _, _ := strings.Cut(s.musicuAPI, "?")
	if _, ok := f.postJSON(ctx, cfg, endpoint, body, true, headers, &payload); !ok {
		if diagnostics.failed() {
			return "", errors.New(diagnostics.failures[len(diagnostics.failures)-1])
		}
		return "", errors.New("请求续期接口失败")
	}
	if payload.Req0.Code != 0 {
		return "", fmt.Errorf("续期接口返回 code %d", payload.Req0.Code)
	}
	if strings.TrimSpace(payload.Req0.Data.MusicKey) == "" {
		return "", errors.New("续期接口没有返回新的 musickey")
	}
	return qqMergeWebCredential(cookie, payload.Req0.Data), nil
}

// qqMergeWebCredential 把续出来的凭据写回网页 Cookie，其余键原样留着。
func qqMergeWebCredential(raw string, data qqWebLoginData) string {
	key := strings.TrimSpace(data.MusicKey)
	updates := map[string]string{"qqmusic_key": key, "qm_keyst": key}
	set := func(name, value string) {
		if value = strings.TrimSpace(value); value != "" && value != "0" {
			updates[name] = value
		}
	}
	set("psrf_musickey_createtime", strconv.FormatInt(data.MusicKeyCreateTime, 10))
	set("psrf_qqrefresh_token", data.RefreshToken)
	set("psrf_qqaccess_token", data.AccessToken)
	set("psrf_access_token_expiresAt", strconv.FormatInt(data.ExpiredAt, 10))
	set("psrf_qqopenid", data.OpenID)
	set("psrf_qqunionid", data.UnionID)
	set("euin", data.EncryptUin)
	return mergeCookieValues(raw, updates, slices.Sorted(maps.Keys(updates)))
}
