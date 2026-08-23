// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// 订阅支持的平台。除了 rss（自定义 Feed 地址）之外，每个平台都有内置抓取实现，
// 不依赖 RSSHub 这类第三方中转；确实想走自建中转的人，可以给平台配一条自定义
// Feed 模板，或者直接用 rss 平台填完整地址。
const (
	rssWatchPlatformRSS         = "rss"
	rssWatchPlatformX           = "x"
	rssWatchPlatformBilibili    = "bilibili"
	rssWatchPlatformDouyin      = "douyin"
	rssWatchPlatformXiaohongshu = "xiaohongshu"
	rssWatchPlatformGitHub      = "github"
	// rssWatchPlatformLegacyTwitter 是旧订阅记录里 X 的来源写法。
	rssWatchPlatformLegacyTwitter = "twitter"
)

// rssWatchSource 是一条订阅的来源：平台 + 平台内目标 + 抓取地址。
type rssWatchSource struct {
	Platform string
	Target   string
	URL      string
}

type rssWatchPlatformSpec struct {
	ID          string
	Label       string
	TargetLabel string
	TargetHint  string
	// TemplateKey 是该平台的自定义 Feed 模板设置键，留空表示不支持模板。
	TemplateKey string
	// normalize 把用户填的各种写法（链接、@用户名、纯 ID）收敛成规范目标。
	normalize func(raw string) (string, error)
	// resolve 把规范目标转成抓取地址；原生抓取的平台返回主页地址即可。
	resolve func(target string) (string, error)
	// fetch 是内置抓取实现，为空表示 resolve 出来的地址本身就是 RSS/Atom。
	fetch func(context.Context, *RSSWatchPlugin, rssWatchSource, SettingValues) (parsedFeed, error)
}

var (
	twitterHandlePattern     = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)
	bilibiliUIDPattern       = regexp.MustCompile(`^[0-9]{1,20}$`)
	douyinSecUIDPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{16,120}$`)
	xiaohongshuUserIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{24}$`)
	githubNamePattern        = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
)

func rssWatchPlatformSpecs() []rssWatchPlatformSpec {
	return []rssWatchPlatformSpec{
		{
			ID: rssWatchPlatformX, Label: "X (Twitter)", TargetLabel: "X 用户",
			TargetHint:  "填 @handle、handle 或用户主页链接。",
			TemplateKey: rssWatchSettingTwitterTemplate,
			normalize:   normalizeTwitterHandle,
			resolve:     func(target string) (string, error) { return "https://x.com/" + target, nil },
			fetch: func(ctx context.Context, p *RSSWatchPlugin, source rssWatchSource, settings SettingValues) (parsedFeed, error) {
				return p.fetchXTimeline(ctx, source.Target, settings)
			},
		},
		{
			ID: rssWatchPlatformBilibili, Label: "哔哩哔哩", TargetLabel: "UP 主 UID",
			TargetHint: "填 UID 数字或 space.bilibili.com 主页链接。",
			normalize:  normalizeBilibiliUID,
			resolve:    func(target string) (string, error) { return "https://space.bilibili.com/" + target, nil },
			fetch: func(ctx context.Context, p *RSSWatchPlugin, source rssWatchSource, settings SettingValues) (parsedFeed, error) {
				return p.fetchBilibiliSpace(ctx, source.Target, settings)
			},
		},
		{
			ID: rssWatchPlatformDouyin, Label: "抖音", TargetLabel: "用户 sec_uid",
			TargetHint: "填用户主页链接，或链接里 MS4wLjABAAAA 开头的 sec_uid。",
			normalize:  normalizeDouyinSecUID,
			resolve:    func(target string) (string, error) { return "https://www.douyin.com/user/" + target, nil },
			fetch: func(ctx context.Context, p *RSSWatchPlugin, source rssWatchSource, settings SettingValues) (parsedFeed, error) {
				return p.fetchDouyinUser(ctx, source.Target, settings)
			},
		},
		{
			ID: rssWatchPlatformXiaohongshu, Label: "小红书", TargetLabel: "用户 ID",
			TargetHint: "填 24 位用户 ID 或用户主页链接。",
			normalize:  normalizeXiaohongshuUserID,
			resolve: func(target string) (string, error) {
				return "https://www.xiaohongshu.com/user/profile/" + target, nil
			},
			fetch: func(ctx context.Context, p *RSSWatchPlugin, source rssWatchSource, settings SettingValues) (parsedFeed, error) {
				return p.fetchXiaohongshuUser(ctx, source.Target, settings)
			},
		},
		{
			ID: rssWatchPlatformGitHub, Label: "GitHub", TargetLabel: "仓库或用户",
			TargetHint: "owner/repo 默认跟 Release；也可以写 owner/repo/commits[/分支]、owner/repo/tags 或单个用户名。",
			normalize:  normalizeGitHubTarget,
			resolve:    githubFeedURL,
		},
		{
			ID: rssWatchPlatformRSS, Label: "自定义 RSS / Atom", TargetLabel: "Feed URL",
			TargetHint: "任意 RSS 2.0 或 Atom 地址，只允许公网 http/https。",
			normalize:  normalizeRSSURL,
			resolve:    func(target string) (string, error) { return target, nil },
		},
	}
}

// rssWatchPlatform 按 ID 查平台，兼容旧记录里的 twitter 与空值。
func rssWatchPlatform(id string) (rssWatchPlatformSpec, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	switch id {
	case "":
		id = rssWatchPlatformRSS
	case rssWatchPlatformLegacyTwitter:
		id = rssWatchPlatformX
	}
	for _, spec := range rssWatchPlatformSpecs() {
		if spec.ID == id {
			return spec, true
		}
	}
	return rssWatchPlatformSpec{}, false
}

func rssWatchPlatformIDs() []string {
	specs := rssWatchPlatformSpecs()
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.ID)
	}
	return out
}

func rssWatchPlatformSummary() string {
	specs := rssWatchPlatformSpecs()
	parts := make([]string, 0, len(specs))
	for _, spec := range specs {
		parts = append(parts, spec.ID+"："+spec.Label+"，"+spec.TargetHint)
	}
	return strings.Join(parts, "；")
}

// resolveRSSWatchSource 把工具或 WebUI 传来的平台与目标收敛成可抓取的来源。
// legacyFeedURL / legacyHandle 是旧字段，只有在没填 platform+target 时才启用。
func resolveRSSWatchSource(platform, target, legacyFeedURL, legacyHandle string) (rssWatchSource, error) {
	platform, target = strings.ToLower(strings.TrimSpace(platform)), strings.TrimSpace(target)
	legacyFeedURL, legacyHandle = strings.TrimSpace(legacyFeedURL), strings.TrimSpace(legacyHandle)
	if target == "" {
		switch {
		case legacyHandle != "":
			if platform == "" {
				platform = rssWatchPlatformX
			}
			target = legacyHandle
		case legacyFeedURL != "":
			if platform == "" {
				platform = rssWatchPlatformRSS
			}
			target = legacyFeedURL
		}
	}
	if platform == "" {
		platform = rssWatchPlatformRSS
	}
	if target == "" {
		return rssWatchSource{}, fmt.Errorf("请填写订阅目标：%s", rssWatchPlatformSummary())
	}
	spec, ok := rssWatchPlatform(platform)
	if !ok {
		return rssWatchSource{}, fmt.Errorf("不支持的订阅平台 %q，可选：%s", platform, strings.Join(rssWatchPlatformIDs(), "、"))
	}
	normalized, err := spec.normalize(target)
	if err != nil {
		return rssWatchSource{}, err
	}
	feedURL, err := spec.resolve(normalized)
	if err != nil {
		return rssWatchSource{}, err
	}
	if feedURL, err = normalizeRSSURL(feedURL); err != nil {
		return rssWatchSource{}, err
	}
	return rssWatchSource{Platform: spec.ID, Target: normalized, URL: feedURL}, nil
}

// rssWatchSourceFromReminder 还原已保存订阅的来源，兼容只存了 feed_url 的旧记录。
func rssWatchSourceFromReminder(item Reminder) rssWatchSource {
	platform := strings.ToLower(strings.TrimSpace(item.FeedSource))
	if platform == rssWatchPlatformLegacyTwitter {
		platform = rssWatchPlatformX
	}
	if _, ok := rssWatchPlatform(platform); !ok || platform == "" {
		platform = rssWatchPlatformRSS
	}
	target := strings.TrimSpace(item.FeedHandle)
	if target == "" {
		target = strings.TrimSpace(item.FeedURL)
	}
	return rssWatchSource{Platform: platform, Target: target, URL: strings.TrimSpace(item.FeedURL)}
}

// rssWatchSourceLabel 是通知与任务列表里展示的来源名字。
func rssWatchSourceLabel(source rssWatchSource, feedName string) string {
	spec, ok := rssWatchPlatform(source.Platform)
	switch {
	case source.Platform == rssWatchPlatformX && source.Target != "":
		return "@" + source.Target
	case ok && source.Platform != rssWatchPlatformRSS && source.Target != "":
		if feedName != "" {
			return spec.Label + " " + feedName
		}
		return spec.Label + " " + source.Target
	case feedName != "":
		return feedName
	}
	return source.URL
}

func normalizeTwitterHandle(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
		if host == "x.com" || host == "twitter.com" {
			raw = strings.Split(strings.Trim(parsed.Path, "/"), "/")[0]
		}
	}
	raw = strings.TrimPrefix(raw, "@")
	if !twitterHandlePattern.MatchString(raw) {
		return "", fmt.Errorf("X 用户名不正确，请填写 @handle、handle 或用户主页链接")
	}
	return raw, nil
}

func normalizeBilibiliUID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
		if !strings.HasSuffix(host, "bilibili.com") {
			return "", fmt.Errorf("哔哩哔哩订阅只接受 UID 或 space.bilibili.com 主页链接")
		}
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		raw = segments[0]
	}
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "UID:")
	raw = strings.TrimSpace(raw)
	if !bilibiliUIDPattern.MatchString(raw) {
		return "", fmt.Errorf("哔哩哔哩 UID 不正确，请填写 UID 数字或 space.bilibili.com 主页链接")
	}
	return raw, nil
}

func normalizeDouyinSecUID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
		if !strings.HasSuffix(host, "douyin.com") {
			return "", fmt.Errorf("抖音订阅只接受用户主页链接或 sec_uid")
		}
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		raw = segments[len(segments)-1]
	}
	if !douyinSecUIDPattern.MatchString(raw) {
		return "", fmt.Errorf("抖音 sec_uid 不正确，请复制用户主页链接，或链接里 MS4wLjABAAAA 开头的那段")
	}
	return raw, nil
}

func normalizeXiaohongshuUserID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
		if !strings.HasSuffix(host, "xiaohongshu.com") && !strings.HasSuffix(host, "xhslink.com") {
			return "", fmt.Errorf("小红书订阅只接受用户主页链接或 24 位用户 ID")
		}
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		raw = segments[len(segments)-1]
	}
	if !xiaohongshuUserIDPattern.MatchString(raw) {
		return "", fmt.Errorf("小红书用户 ID 不正确，请填写主页链接末尾的 24 位 ID")
	}
	return strings.ToLower(raw), nil
}

// normalizeGitHubTarget 收敛成 user、owner/repo/releases、owner/repo/tags
// 或 owner/repo/commits[/分支] 这几种规范写法。
func normalizeGitHubTarget(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
		if host != "github.com" {
			return "", fmt.Errorf("GitHub 订阅只接受 github.com 链接、owner/repo 或用户名")
		}
		raw = strings.Trim(parsed.Path, "/")
	}
	raw = strings.TrimSuffix(strings.Trim(raw, "/"), ".git")
	raw = strings.TrimSuffix(raw, ".atom")
	segments := []string{}
	for _, segment := range strings.Split(raw, "/") {
		if segment = strings.TrimSpace(segment); segment != "" {
			segments = append(segments, segment)
		}
	}
	if len(segments) == 0 {
		return "", fmt.Errorf("GitHub 订阅目标不能为空")
	}
	for _, segment := range segments[:min(len(segments), 2)] {
		if !githubNamePattern.MatchString(segment) {
			return "", fmt.Errorf("GitHub 名称 %q 不合法", segment)
		}
	}
	if len(segments) == 1 {
		return segments[0], nil
	}
	owner, repo := segments[0], segments[1]
	if len(segments) == 2 {
		return owner + "/" + repo + "/releases", nil
	}
	switch strings.ToLower(segments[2]) {
	case "releases", "release":
		return owner + "/" + repo + "/releases", nil
	case "tags", "tag":
		return owner + "/" + repo + "/tags", nil
	case "commits", "commit":
		if len(segments) == 3 {
			return owner + "/" + repo + "/commits", nil
		}
		branch := strings.Join(segments[3:], "/")
		return owner + "/" + repo + "/commits/" + branch, nil
	default:
		return "", fmt.Errorf("GitHub 订阅只支持 releases、tags、commits 三种内容，收到 %q", segments[2])
	}
}

func githubFeedURL(target string) (string, error) {
	segments := strings.Split(target, "/")
	switch {
	case len(segments) == 1:
		return "https://github.com/" + url.PathEscape(segments[0]) + ".atom", nil
	case len(segments) < 3:
		return "", fmt.Errorf("GitHub 订阅目标不完整：%s", target)
	}
	owner, repo, kind := url.PathEscape(segments[0]), url.PathEscape(segments[1]), segments[2]
	base := "https://github.com/" + owner + "/" + repo + "/"
	switch kind {
	case "releases", "tags":
		return base + kind + ".atom", nil
	case "commits":
		if len(segments) == 3 {
			return base + "commits.atom", nil
		}
		branch := strings.Join(segments[3:], "/")
		escaped := make([]string, 0, len(segments)-3)
		for _, part := range strings.Split(branch, "/") {
			escaped = append(escaped, url.PathEscape(part))
		}
		return base + "commits/" + strings.Join(escaped, "/") + ".atom", nil
	}
	return "", fmt.Errorf("GitHub 订阅目标不合法：%s", target)
}

// applyFeedTemplate 把自定义模板里的占位符换成目标，用于自建 RSS 中转。
func applyFeedTemplate(template, target string) (string, error) {
	template = strings.TrimSpace(template)
	placeholders := []string{"{handle}", "{target}", "{id}", "{uid}"}
	replaced := false
	for _, placeholder := range placeholders {
		if strings.Contains(template, placeholder) {
			template = strings.ReplaceAll(template, placeholder, url.PathEscape(target))
			replaced = true
		}
	}
	if !replaced {
		return "", fmt.Errorf("自定义 Feed 模板必须包含 {handle} 或 {target} 占位符")
	}
	return normalizeRSSURL(template)
}
