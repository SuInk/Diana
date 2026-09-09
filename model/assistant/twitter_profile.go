package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

type twitterProfile struct {
	Name        string `json:"name"`
	Handle      string `json:"screen_name"`
	Description string `json:"description"`
	Followers   *int64 `json:"followers"`
	Following   *int64 `json:"following"`
	Statuses    *int64 `json:"statuses"`
	Protected   bool   `json:"protected"`
}

func twitterProfileHandle(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || !hostMatchesDomain(u.Hostname(), "x.com", "twitter.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 1 && !(len(parts) == 2 && (parts[1] == "media" || parts[1] == "with_replies" || parts[1] == "highlights" || parts[1] == "articles")) {
		return ""
	}
	handle := parts[0]
	if !twitterHandlePattern.MatchString(handle) {
		return ""
	}
	switch strings.ToLower(handle) {
	case "home", "explore", "search", "notifications", "messages", "settings", "i", "intent", "share", "compose", "login", "logout", "signup", "tos", "privacy", "about", "help", "jobs", "grok", "premium", "bookmarks", "communities":
		return ""
	}
	return handle
}

func fetchTwitterProfile(ctx context.Context, handle string) (twitterProfile, error) {
	return fetchTwitterProfileWithClient(ctx, handle, netguard.NewPublicHTTPClient(defaultPlatformTimeout))
}

func fetchTwitterProfileWithClient(ctx context.Context, handle string, client *http.Client) (twitterProfile, error) {
	if !twitterHandlePattern.MatchString(handle) {
		return twitterProfile{}, fmt.Errorf("用户名格式不正确")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.fxtwitter.com/2/profile/"+url.PathEscape(handle), nil)
	if err != nil {
		return twitterProfile{}, err
	}
	for key, value := range twitterResolverHeaders() {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return twitterProfile{}, fmt.Errorf("主页接口请求失败，请稍后重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return twitterProfile{}, twitterProfileStatusError(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, resolverReadLimit+1))
	if err != nil || len(data) > resolverReadLimit {
		return twitterProfile{}, fmt.Errorf("主页接口响应不完整或过大")
	}
	var body struct {
		Code int             `json:"code"`
		User *twitterProfile `json:"user"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return twitterProfile{}, fmt.Errorf("主页接口返回了无效数据")
	}
	if body.Code != http.StatusOK {
		return twitterProfile{}, twitterProfileStatusError(body.Code)
	}
	if body.User == nil || !strings.EqualFold(body.User.Handle, handle) || strings.TrimSpace(body.User.Name) == "" {
		return twitterProfile{}, fmt.Errorf("主页接口未返回匹配的用户资料")
	}
	return *body.User, nil
}

func twitterProfileStatusError(status int) error {
	switch status {
	case http.StatusNotFound:
		return fmt.Errorf("账号不存在、已停用或暂不可用")
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("主页资料访问受限")
	case http.StatusTooManyRequests:
		return fmt.Errorf("主页接口限流，请稍后重试")
	default:
		return fmt.Errorf("主页接口暂不可用（状态 %d）", status)
	}
}

func (p *ResolverPlugin) resolveTwitterProfile(ctx context.Context, req PluginRequest, raw, handle string) resolverSocialResult {
	timeout := time.Duration(req.Settings.Int(resolverSettingTimeoutSeconds, defaultResolverTimeoutSeconds)) * time.Second
	if timeout <= 0 || timeout > maxResolverTimeoutSeconds*time.Second {
		timeout = defaultPlatformTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fetch := p.twitterProfileFetcher
	if fetch == nil {
		fetch = fetchTwitterProfile
	}
	profile, err := fetch(ctx, handle)
	if err != nil {
		recordResolverMediaLog(ctx, req, raw, "X / Twitter 用户主页", false, err.Error())
		return resolverSocialResult{Handled: true, Failed: true, Context: fmt.Sprintf("[X / Twitter 用户主页] @%s\n无法读取用户资料：%s", handle, err)}
	}
	var text strings.Builder
	fmt.Fprintf(&text, "[X / Twitter 用户主页]\n%s（@%s）", compactWhitespace(profile.Name), profile.Handle)
	if bio := strings.TrimSpace(profile.Description); bio != "" {
		fmt.Fprintf(&text, "\n简介：%s", truncateRunesFromStart(bio, resolverDescriptionHardCap))
	}
	var counts []string
	for _, item := range []struct {
		label string
		value *int64
	}{{"粉丝", profile.Followers}, {"关注", profile.Following}, {"帖子", profile.Statuses}} {
		if item.value != nil && *item.value >= 0 {
			counts = append(counts, fmt.Sprintf("%s %d", item.label, *item.value))
		}
	}
	if len(counts) > 0 {
		text.WriteString("\n" + strings.Join(counts, " · "))
	}
	if profile.Protected {
		text.WriteString("\n受保护账号，仅展示可见资料")
	}
	fmt.Fprintf(&text, "\n主页：https://x.com/%s", handle)
	recordResolverMediaLog(ctx, req, raw, "X / Twitter 用户主页", true, "已读取用户主页资料")
	return resolverSocialResult{Handled: true, Context: text.String(), ResourceKeys: []string{"x:profile:" + strings.ToLower(handle)}}
}
