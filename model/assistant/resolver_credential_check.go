// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
)

const (
	bilibiliNavURL       = "https://api.bilibili.com/x/web-interface/nav"
	douyinAccountInfoURL = "https://www.douyin.com/passport/account/info/v2/?aid=6383"
	xiaohongshuHomeURL   = "https://www.xiaohongshu.com/explore"
)

var (
	// 小红书首页的 __INITIAL_STATE__ 里混着 undefined，整段当 JSON 解不开，
	// 这里只抠登录标记和昵称两个字段。
	xiaohongshuLoggedInRegex = regexp.MustCompile(`"user":\{"loggedIn":(true|false)`)
	xiaohongshuNicknameRegex = regexp.MustCompile(`"userInfo":\{[^{}]*?"nickname":"([^"]*)"`)
)

// TestCredentials 逐个实测链接解析的登录凭据。请求头和真实解析走同一套拼法
// （SESSDATA 补字段名、Cookie 清洗、抖音的 uifid 头），测出来好就是解析时真的好。
func (p *ResolverPlugin) TestCredentials(ctx context.Context, settings SettingValues) []CredentialCheck {
	creds := resolverCredentialsFromSettings(settings)
	ctx = withResolverCredentials(ctx, creds)
	return []CredentialCheck{
		p.checkBilibiliSessdata(ctx, creds.BiliSessdata),
		p.checkDouyinCookie(ctx),
		p.checkXiaohongshuCookie(ctx),
		checkYTDLPCookieFile(creds.YTDLPCookies),
	}
}

func (p *ResolverPlugin) checkBilibiliSessdata(ctx context.Context, sessdata string) CredentialCheck {
	const key, label = resolverSettingBiliSessdata, "B 站 SESSDATA"
	if sessdata == "" {
		return unconfiguredCredential(key, label)
	}
	result := CredentialCheck{Key: key, Label: label, Configured: true}
	headers := resolverCommonHeaders()
	headers["Referer"] = "https://www.bilibili.com/"
	headers["Cookie"] = "SESSDATA=" + sessdata
	status, body, err := fetchCredentialProbe(ctx, p.credentialClient, bilibiliNavURL, headers)
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			IsLogin   bool   `json:"isLogin"`
			Uname     string `json:"uname"`
			VIPStatus int    `json:"vipStatus"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal([]byte(body), &payload) != nil {
		return credentialProbeFailed(result, "B 站", status, err)
	}
	if !payload.Data.IsLogin {
		result.State = CredentialInvalid
		result.Message = withCredentialHint("B 站说这份 SESSDATA 没有登录（"+firstNonEmpty(payload.Message, fmt.Sprint(payload.Code))+"），需要重新复制。", pastedCookieNameHint(sessdata, "SESSDATA"))
		return result
	}
	result.State = CredentialValid
	result.Account = payload.Data.Uname
	result.Message = "已登录"
	if payload.Data.VIPStatus == 1 {
		result.Message = "已登录，大会员"
	}
	return result
}

func (p *ResolverPlugin) checkDouyinCookie(ctx context.Context) CredentialCheck {
	const key, label = resolverSettingDouyinCookie, "抖音 Cookie"
	cookie := resolverDouyinCookie(ctx)
	if cookie == "" {
		return unconfiguredCredential(key, label)
	}
	result := CredentialCheck{Key: key, Label: label, Configured: true}
	status, body, err := fetchCredentialProbe(ctx, p.credentialClient, douyinAccountInfoURL, douyinWebHeaders(ctx, "https://www.douyin.com/"))
	var payload struct {
		Message string `json:"message"`
		Data    struct {
			UserID      int64  `json:"user_id"`
			ScreenName  string `json:"screen_name"`
			ErrorCode   int    `json:"error_code"`
			Description string `json:"description"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal([]byte(body), &payload) != nil {
		return credentialProbeFailed(result, "抖音", status, err)
	}
	if payload.Data.UserID == 0 {
		result.State = CredentialInvalid
		result.Message = "抖音说这份 Cookie 没有登录（" + firstNonEmpty(payload.Data.Description, payload.Message) + "），需要重新复制；Cookie 里应当有 sessionid。"
		return result
	}
	result.State = CredentialValid
	result.Account = payload.Data.ScreenName
	result.Message = "已登录"
	return result
}

func (p *ResolverPlugin) checkXiaohongshuCookie(ctx context.Context) CredentialCheck {
	const key, label = resolverSettingXHSCookie, "小红书 Cookie"
	cookie := resolverXHSCookie(ctx)
	if cookie == "" {
		return unconfiguredCredential(key, label)
	}
	result := CredentialCheck{Key: key, Label: label, Configured: true}
	status, body, err := fetchCredentialProbe(ctx, p.credentialClient, xiaohongshuHomeURL, xiaohongshuPageHeaders(cookie))
	match := xiaohongshuLoggedInRegex.FindStringSubmatch(body)
	if err != nil || status < 200 || status >= 300 || match == nil {
		return credentialProbeFailed(result, "小红书", status, err)
	}
	if match[1] != "true" {
		result.State = CredentialInvalid
		result.Message = "小红书说这份 Cookie 没有登录，需要重新复制。"
		if !xiaohongshuCookieLoggedIn(cookie) {
			// web_session 是 HttpOnly，document.cookie 和一些插件导不出来，
			// 最常见的「明明登录了却测不过」就是它。
			result.Message += "Cookie 里没有 web_session：请从开发者工具 Network 面板任意请求的 Cookie 请求头整段复制，document.cookie 拿不到它。"
		}
		return result
	}
	result.State = CredentialValid
	if nickname := xiaohongshuNicknameRegex.FindStringSubmatch(body); nickname != nil {
		result.Account = nickname[1]
	}
	result.Message = "已登录"
	return result
}

// checkYTDLPCookieFile 只查文件：yt-dlp 的登录态分散在各站点，没有一个统一的
// 账号接口可问，能提前发现的只有「路径写错」和「格式不对」。
func checkYTDLPCookieFile(path string) CredentialCheck {
	const key, label = resolverSettingYTDLPCookies, "yt-dlp Cookie 文件"
	if path == "" {
		return unconfiguredCredential(key, label)
	}
	result := CredentialCheck{Key: key, Label: label, Configured: true, State: CredentialInvalid}
	file, err := os.Open(path)
	if err != nil {
		result.Message = "读不到这个文件：" + err.Error()
		return result
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || info.IsDir() {
		result.Message = "这个路径不是文件。"
		return result
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for lines := 0; scanner.Scan() && lines < 50; lines++ {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(line, "Netscape HTTP Cookie File") || strings.Count(line, "\t") >= 6 {
			result.State = CredentialUnverified
			result.Message = "文件可读，是 Netscape 格式；是否仍在登录状态要等 yt-dlp 实际下载时才知道。"
			return result
		}
	}
	result.Message = "文件不是 Netscape 格式的 Cookie，yt-dlp 读不了。请用浏览器扩展按 Netscape 格式导出。"
	return result
}

func credentialProbeFailed(result CredentialCheck, platform string, status int, err error) CredentialCheck {
	result.State = CredentialError
	switch {
	case err != nil:
		result.Message = platform + "账号接口请求失败，暂时判断不了凭据好坏：" + err.Error()
	case status == http.StatusForbidden || status == http.StatusTooManyRequests:
		result.Message = fmt.Sprintf("%s账号接口拒绝了请求（HTTP %d），可能是风控，暂时判断不了凭据好坏。", platform, status)
	default:
		result.Message = fmt.Sprintf("%s账号接口返回了看不懂的内容（HTTP %d），暂时判断不了凭据好坏。", platform, status)
	}
	return result
}
