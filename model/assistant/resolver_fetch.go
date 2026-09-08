// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 知乎、小红书这类站点对明显的机器人 UA 会直接返回登录墙或空壳页面，
// 但它们的 SSR HTML head 里仍带 og:title/og:description 元数据。
// 链接预览使用真实浏览器请求头是 IM 机器人（Slack/Discord unfurl 等）的通行做法；
// 配合结果缓存压低请求频率，本身就是最有效的防风控手段。

// resolverUserAgent 和无头浏览器那条路共用同一个身份，见 agent.BrowserUserAgent。
// 两条路各写一份的时候，浏览器那条报的是 HeadlessChrome，同一个站会看到两个访客。
var resolverUserAgent = agent.BrowserUserAgent

const (
	// 只读页面前 256KB，元数据都在 head 里，避免下载整页或大文件。
	resolverReadLimit = 256 * 1024

	resolverDescriptionHardCap = 400
)

type pageMeta struct {
	Title       string
	Description string
	FinalURL    string
}

// applyBrowserHeaders 为抓取请求设置常规浏览器头；ua 为空时使用内置 Chrome UA。
func applyBrowserHeaders(req *http.Request, ua string) {
	if ua == "" {
		ua = resolverUserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
}

// fetchPageMeta 带缓存地抓取网页元数据：先走直接 HTTP，仍拿不到内容且开启
// 浏览器渲染兜底时，再用本机 Chrome 渲染一次（可借用浏览器登录态过风控）。
func (p *ResolverPlugin) fetchPageMeta(ctx context.Context, raw string, opts resolveOptions) (pageMeta, bool) {
	key := sharedResultKey([]any{"page-meta-v1", resolverURLDedupeKey(raw), opts.userAgent, opts.httpTimeout, opts.browserRender, opts.browserCDPURL})
	// Browser sessions and cookie jars can change auth without changing settings.
	if opts.browserRender || (p.client != nil && p.client.Jar != nil) {
		key = ""
	}
	meta, err := p.sharedPages.load(ctx, key, opts.cacheTTL, 2*time.Minute, func(value pageMeta) bool { return value.Title != "" || value.Description != "" }, func(loadCtx context.Context) (pageMeta, error) {
		fresh := opts
		fresh.cacheTTL = 0
		value, ok := p.fetchPageMetaFresh(loadCtx, raw, fresh)
		if !ok {
			return pageMeta{}, fmt.Errorf("page metadata unavailable")
		}
		return value, nil
	})
	return meta, err == nil
}

func (p *ResolverPlugin) fetchPageMetaFresh(ctx context.Context, raw string, opts resolveOptions) (pageMeta, bool) {

	// 每个链接的直接抓取单独限时，避免慢站点吃满整条消息的处理时间。
	httpCtx, cancel := context.WithTimeout(ctx, opts.httpTimeout)
	meta, ok := fetchPageMetaDirect(httpCtx, p.client, raw, opts.userAgent)
	cancel()

	if !ok && opts.browserRender && p.browserFetch != nil {
		if page, err := p.browserFetch(ctx, opts.browserCDPURL, raw); err == nil {
			if page.URL != "" {
				meta.FinalURL = page.URL
			}
			title := compactWhitespace(page.Title)
			if looksLikeBlockedPage(title) {
				title = ""
			}
			description := compactWhitespace(page.Description)
			if description == "" && title != "" {
				// 只有拿到真实标题时才拿正文开头凑摘要，否则风控/验证页的提示语会被当成内容。
				description = compactWhitespace(page.Text)
			}
			if looksLikeBlockedPage(description) {
				description = ""
			}
			// 缓存里存 400 字硬上限的近全量摘要，输出时再按设置截断，
			// 这样调整摘要长度设置后不用等缓存过期。
			if runes := []rune(description); len(runes) > resolverDescriptionHardCap {
				description = string(runes[:resolverDescriptionHardCap]) + "…"
			}
			if title != "" {
				meta.Title = title
			}
			if description != "" {
				meta.Description = description
			}
			ok = meta.Title != "" || meta.Description != ""
		}
	}

	return meta, ok
}

// fetchPageMetaDirect 抓取网页并提取标题、摘要和跳转后的最终地址。
func fetchPageMetaDirect(ctx context.Context, client *http.Client, raw string, userAgent string) (pageMeta, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return pageMeta{}, false
	}
	applyBrowserHeaders(req, userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return pageMeta{}, false
	}
	defer resp.Body.Close()

	// 短链（b23.tv、xhslink 等）跳转后的最终地址用于平台识别。
	meta := pageMeta{}
	if resp.Request != nil && resp.Request.URL != nil {
		meta.FinalURL = resp.Request.URL.String()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return meta, false
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "html") && !strings.Contains(contentType, "text") {
		return meta, false
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, resolverReadLimit))
	if err != nil && len(data) == 0 {
		return meta, false
	}
	html := string(data)
	meta.Title = compactWhitespace(firstNonEmpty(metaTagContent(html, "og:title"), extractHTMLTitle(html)))
	description := compactWhitespace(firstNonEmpty(metaTagContent(html, "og:description"), metaTagContent(html, "description")))
	// 硬上限兜底；按设置的展示截断在 resolveURL 输出时做。
	if runes := []rune(description); len(runes) > resolverDescriptionHardCap {
		description = string(runes[:resolverDescriptionHardCap]) + "…"
	}
	meta.Description = description
	return meta, meta.Title != "" || meta.Description != ""
}

var (
	metaTagPattern  = regexp.MustCompile(`(?is)<meta\s[^>]*>`)
	metaAttrPattern = regexp.MustCompile(`(?is)([a-zA-Z-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
)

// metaTagContent 从 HTML 里提取 property/name 匹配的 meta content，属性顺序不限。
func metaTagContent(html string, key string) string {
	for _, tag := range metaTagPattern.FindAllString(html, -1) {
		var content, property, name string
		for _, attr := range metaAttrPattern.FindAllStringSubmatch(tag, -1) {
			value := attr[2]
			if value == "" {
				value = attr[3]
			}
			switch strings.ToLower(attr[1]) {
			case "content":
				content = value
			case "property":
				property = strings.ToLower(value)
			case "name":
				name = strings.ToLower(value)
			}
		}
		if property == key || name == key {
			return unescapeHTMLText(content)
		}
	}
	return ""
}

// 风控页/验证页的常见提示语；渲染结果命中时按抓取失败处理，不喂给模型。
var blockedPageMarkers = []string{
	"请求存在异常", "暂时限制", "安全验证", "访问验证", "访问异常", "人机验证", "验证码",
	"登录后查看", "登录后继续", "captcha", "access denied", "verify you are human",
}

// looksLikeBlockedPage 判断文本是否像风控或验证页提示。
func looksLikeBlockedPage(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, marker := range blockedPageMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

var htmlEntityReplacer = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&#x27;", "'", "&nbsp;", " ",
)

// unescapeHTMLText 还原常见 HTML 实体。
func unescapeHTMLText(text string) string {
	return htmlEntityReplacer.Replace(text)
}
