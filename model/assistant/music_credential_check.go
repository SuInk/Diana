// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// musicLoginChecker 是能单独实测登录态的曲库。连接测试原来只能从「搜得到但
// 取不到播放地址」倒推凭据可能失效，可测试曲本来就可能是独家或下架，
// 倒推出来的结论两头都不可靠；问账号接口才说得准。
type musicLoginChecker interface {
	CheckLogin(ctx context.Context, f *musicFetcher, cfg musicConfig) CredentialCheck
}

func (s *neteaseSource) CheckLogin(ctx context.Context, f *musicFetcher, cfg musicConfig) CredentialCheck {
	cookie := cfg.sourceOptions(s.Key()).Cookie
	result := CredentialCheck{Key: musicSourceCookieSetting(s.Key()), Label: "网易云 MUSIC_U", Configured: true}
	var payload struct {
		Account *struct {
			VIPType int `json:"vipType"`
		} `json:"account"`
		Profile *struct {
			Nickname string `json:"nickname"`
			VIPType  int    `json:"vipType"`
		} `json:"profile"`
	}
	if !f.fetchJSON(ctx, cfg, s.accountAPI, true, s.headers(cfg), &payload) {
		result.State = CredentialError
		result.Message = "网易云账号接口请求失败，暂时判断不了 MUSIC_U 好坏。"
		return result
	}
	if payload.Account == nil || payload.Profile == nil {
		result.State = CredentialInvalid
		result.Message = withCredentialHint("网易云说这份 MUSIC_U 没有登录或已过期，需要重新复制。", pastedCookieNameHint(cookie, "MUSIC_U"))
		return result
	}
	result.State = CredentialValid
	result.Account = payload.Profile.Nickname
	result.Message = "已登录"
	if payload.Account.VIPType > 0 || payload.Profile.VIPType > 0 {
		result.Message = "已登录，会员账号"
	}
	return result
}

// qqLoginCodeNotLoggedIn 是 musicu 接口对游客和失效登录态统一返回的业务码。
const qqLoginCodeNotLoggedIn = 1000

func (s *qqSource) CheckLogin(ctx context.Context, f *musicFetcher, cfg musicConfig) CredentialCheck {
	cookie := cfg.sourceOptions(s.Key()).Cookie
	result := CredentialCheck{Key: musicSourceCookieSetting(s.Key()), Label: "QQ 音乐 Cookie", Configured: true}
	uin := qqUINFromCookie(cookie)
	if uin == "0" {
		result.State = CredentialInvalid
		result.Message = "Cookie 里找不到 uin 或 qqmusic_uin，QQ 音乐认不出是哪个账号。"
		return result
	}
	request, err := json.Marshal(map[string]any{
		"comm":  map[string]any{"uin": uin, "format": "json", "ct": 24, "cv": 0},
		"req_0": map[string]any{"module": "music.UserInfo.userInfoServer", "method": "GetLoginUserInfo", "param": map[string]any{}},
	})
	if err != nil {
		result.State = CredentialError
		result.Message = "组装 QQ 音乐账号请求失败：" + err.Error()
		return result
	}
	var payload struct {
		Req0 struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		} `json:"req_0"`
	}
	if !f.fetchJSON(ctx, cfg, fmt.Sprintf(s.vkeyAPI, url.QueryEscape(string(request))), true, s.headers(cfg), &payload) {
		result.State = CredentialError
		result.Message = "QQ 音乐账号接口请求失败，暂时判断不了 Cookie 好坏。"
		return result
	}
	switch payload.Req0.Code {
	case 0:
		result.State = CredentialValid
		result.Account = firstNonEmpty(anyString(payload.Req0.Data["nick"]), anyString(payload.Req0.Data["nickname"]))
		result.Message = "已登录"
	case qqLoginCodeNotLoggedIn:
		result.State = CredentialInvalid
		result.Message = "QQ 音乐说这份 Cookie 没有登录或已过期，需要重新复制；Cookie 里应当有 qqmusic_key 或 qm_keyst。"
	default:
		result.State = CredentialError
		result.Message = fmt.Sprintf("QQ 音乐账号接口返回 code %d，暂时判断不了 Cookie 好坏。", payload.Req0.Code)
	}
	return result
}

// 酷狗的账号接口要签名，官方没有能直接问的地方，只能查字段齐不齐。
func (s *kugouSource) CheckLogin(_ context.Context, _ *musicFetcher, cfg musicConfig) CredentialCheck {
	values := musicCookieValues(cfg.sourceOptions(s.Key()).Cookie)
	result := CredentialCheck{Key: musicSourceCookieSetting(s.Key()), Label: "酷狗 Cookie", Configured: true}
	missing := make([]string, 0, 2)
	for _, key := range []string{"token", "userid"} {
		if strings.TrimSpace(values[key]) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		result.State = CredentialInvalid
		result.Message = "Cookie 里缺少 " + strings.Join(missing, "、") + "，酷狗认不出登录账号。"
		return result
	}
	result.State = CredentialUnverified
	result.Message = "token、userid 都在；酷狗没有公开的账号接口可问，是否仍有效以能否取到会员歌曲播放地址为准。"
	return result
}
