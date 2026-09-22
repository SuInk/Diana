// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// resolverPlatformResult preserves the original platform-specific forwarding
// contract for injected resolvers and older integrations.
type resolverPlatformResult struct {
	Context         string
	ImageURLs       []string
	VideoURLs       []string
	ForwardMessages []OutgoingMessage
	// DeferToBrowser 表示这条链接按平台接口读不出来，但渲染一次页面多半能读到：
	// 交给沙盒浏览器，而不是回一句「解析失败」。沙盒浏览器没开时才退回文字说明。
	DeferToBrowser bool
}

func (p *ResolverPlugin) resolveKnownPlatform(ctx context.Context, req PluginRequest, raw string) resolverPlatformResult {
	raw = normalizeResolverURL(raw)
	switch {
	case isBilibiliURL(raw):
		return p.resolveBilibili(ctx, req, raw)
	case isDouyinURL(raw):
		return p.resolveDouyin(ctx, req, raw)
	case isXiaohongshuURL(raw):
		return p.resolveXiaohongshu(ctx, req, raw)
	case isTwitterURL(raw):
		return p.resolveTwitter(ctx, req, raw)
	case resolverPlatformIs(raw, "youtube"):
		return p.resolveYouTube(ctx, req, raw)
	default:
		return resolverPlatformTextResult(p.resolveURL(ctx, raw, legacyResolveOptions()))
	}
}

func resolverPlatformIs(raw, want string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	key, _ := platformKeyAndLabel(parsed.Hostname())
	return key == want
}

func legacyResolveOptions() resolveOptions {
	return resolveOptions{
		fetchTitle:      true,
		httpTimeout:     defaultResolverTimeoutSeconds * time.Second,
		summaryMaxRunes: defaultResolverSummaryMaxRunes,
	}
}

func (p *ResolverPlugin) legacyPageTitle(ctx context.Context, raw string) string {
	meta, _ := p.fetchPageMeta(ctx, raw, legacyResolveOptions())
	return strings.TrimSpace(meta.Title)
}

func resolverPlatformTextResult(text string) resolverPlatformResult {
	text = strings.TrimSpace(text)
	if text == "" {
		return resolverPlatformResult{}
	}
	return resolverPlatformResult{Context: text, ForwardMessages: []OutgoingMessage{{Text: text}}}
}

func (p *ResolverPlugin) resolveBilibili(ctx context.Context, req PluginRequest, raw string) resolverPlatformResult {
	nickname := resolverNickname()
	view, ok := fetchBilibiliView(ctx, raw)
	if !ok {
		return resolverPlatformTextResult(fmt.Sprintf("%s识别：B站，出错，无法获取数据！", nickname))
	}
	videoTitle := deleteResolverBoringCharacters(firstNonEmpty(view.Data.Title, p.legacyPageTitle(ctx, raw)))
	if videoTitle == "" {
		videoTitle = "出错，无法获取数据！"
	}
	text := fmt.Sprintf("\n%s识别：B站，%s", nickname, videoTitle)
	if extra := extraBiliInfoText(view); extra != "" {
		text += "\n" + extra
	}
	if description := strings.TrimSpace(view.Data.Desc); description != "" {
		text += "\n简介：" + description
	}
	cover := singleURL(view.Data.Pic)
	if view.Data.Duration > resolverVideoMaxDuration(ctx) {
		text += fmt.Sprintf("\n当前视频时长 %d 分钟，超过管理员设置的最长时间 %d 分钟。", view.Data.Duration/60, resolverVideoMaxDuration(ctx)/60)
		return resolverPlatformResult{Context: strings.TrimSpace(text), ImageURLs: cover, ForwardMessages: []OutgoingMessage{{Text: text, ImageURLs: cover, ImagesFirst: true}}}
	}
	videoPath := p.downloadResolverVideo(ctx, req, raw)
	nodes := []OutgoingMessage{{Text: text, ImageURLs: cover, ImagesFirst: true}}
	if videoPath == "" {
		nodes = append(nodes, OutgoingMessage{Text: fmt.Sprintf("%s识别：B站，媒体下载失败", nickname)})
		return resolverPlatformResult{Context: strings.TrimSpace(text), ImageURLs: cover, ForwardMessages: nodes}
	}
	nodes = append(nodes, OutgoingMessage{VideoURLs: []string{videoPath}})
	return resolverPlatformResult{Context: strings.TrimSpace(text), ImageURLs: cover, VideoURLs: []string{videoPath}, ForwardMessages: nodes}
}

func (p *ResolverPlugin) resolveDouyin(ctx context.Context, req PluginRequest, raw string) resolverPlatformResult {
	nickname := resolverNickname()
	detail, ok, _ := fetchDouyinMediaDetail(ctx, raw)
	if !ok {
		return resolverPlatformTextResult(fmt.Sprintf("%s识别：抖音，解析失败！", nickname))
	}
	text := fmt.Sprintf("%s识别：抖音，%s", nickname, strings.TrimSpace(detail.Desc))
	if strings.TrimSpace(text) == nickname+"识别：抖音，" {
		text = fmt.Sprintf("%s识别：抖音", nickname)
	}
	if douyinDetailIsImagePost(detail) {
		images := douyinMediaImageURLs(detail)
		nodes := []OutgoingMessage{{Text: text}}
		for _, imageURL := range images {
			nodes = append(nodes, OutgoingMessage{ImageURLs: []string{imageURL}})
		}
		return resolverPlatformResult{Context: strings.TrimSpace(text), ImageURLs: images, ForwardMessages: nodes}
	}
	cover := singleURL(firstNonEmptyString(detail.Video.Cover.URLList))
	metaText := "\n" + text
	nodes := []OutgoingMessage{{Text: metaText, ImageURLs: cover, ImagesFirst: true}}
	videoPath := p.downloadResolverVideo(ctx, req, raw)
	if videoPath == "" {
		nodes = append(nodes, OutgoingMessage{Text: fmt.Sprintf("%s识别：抖音，视频下载失败，已停止转发。", nickname)})
		return resolverPlatformResult{Context: strings.TrimSpace(metaText), ImageURLs: cover, ForwardMessages: nodes}
	}
	nodes = append(nodes, OutgoingMessage{VideoURLs: []string{videoPath}})
	return resolverPlatformResult{Context: strings.TrimSpace(metaText), ImageURLs: cover, VideoURLs: []string{videoPath}, ForwardMessages: nodes}
}

// xiaohongshuRenderers 按「不需要用户配置」的优先级给出可用的渲染方式：先是自己会拉起
// 浏览器的沙盒渲染器，再是需要外部 CDP 端口的那条。
func (p *ResolverPlugin) xiaohongshuRenderers() []func(context.Context, string) (agent.RenderedPage, error) {
	renderers := []func(context.Context, string) (agent.RenderedPage, error){}
	if p.pageRenderer != nil {
		renderers = append(renderers, p.pageRenderer.Render)
	} else {
		headless := true
		renderers = append(renderers, agent.NewSandboxedHeadlessBrowser(agent.SandboxedBrowserConfig{Headless: &headless}).Render)
	}
	if p.browserFetch != nil {
		renderers = append(renderers, func(ctx context.Context, raw string) (agent.RenderedPage, error) {
			return p.browserFetch(ctx, "", raw)
		})
	}
	return renderers
}

// xiaohongshuBrowserFallback 在抓不到笔记时自动改用浏览器渲染，不要求用户先去打开
// 什么开关：开了沙盒浏览器就交给它，否则直接用内置的无头浏览器渲染一次，拿标题和摘要。
// 两条都走不通才回文字，而且文字里说的是「这条路读不到」，不是「笔记不存在」。
func (p *ResolverPlugin) xiaohongshuBrowserFallback(ctx context.Context, req PluginRequest, raw, nickname, reason string) resolverPlatformResult {
	if req.SandboxedBrowserEnabled {
		return resolverPlatformResult{DeferToBrowser: true}
	}
	// 先用会自己拉起 Chrome/Chromium 的渲染器：它不需要外部调试端口，也不需要任何设置。
	// p.browserFetch 走的是 CDP（默认 127.0.0.1:9222），只有配过「交互式浏览器」的机器
	// 才连得上——生产机上实测 9222 没人监听，指望它兜底等于没有兜底。
	for _, render := range p.xiaohongshuRenderers() {
		if page, err := render(ctx, raw); err == nil {
			title := compactWhitespace(page.Title)
			if looksLikeBlockedPage(title) {
				title = ""
			}
			summary := compactWhitespace(firstNonEmpty(page.Description, page.Text))
			if looksLikeBlockedPage(summary) {
				summary = ""
			}
			if title != "" || summary != "" {
				text := fmt.Sprintf("%s识别内容来自：【小红书】\n%s", nickname, strings.TrimSpace(title+"\n"+truncateRunes(summary, defaultResolverSummaryMaxRunes)))
				return resolverPlatformTextResult(text)
			}
		}
	}
	return resolverPlatformTextResult(fmt.Sprintf("%s识别内容来自：【小红书】\n%s，用浏览器渲染也没读到内容。这台机器上可能没有可用的浏览器（容器基础版不预装 Chromium），或者页面本身打不开。", nickname, reason))
}

func (p *ResolverPlugin) resolveXiaohongshu(ctx context.Context, req PluginRequest, raw string) resolverPlatformResult {
	nickname := resolverNickname()
	note, status := fetchXiaohongshuNote(ctx, raw)
	switch status {
	case "missing_cookie":
		// 没配 Cookie 也先让浏览器试一次：实测未登录的浏览器照样读得到笔记，
		// 没理由因为少一份 Cookie 就直接回一句「没配 ck」。
		return p.xiaohongshuBrowserFallback(ctx, req, raw, nickname, "没有配置小红书 Cookie")
	case "expired_link":
		return resolverPlatformTextResult(fmt.Sprintf("%s识别内容来自：【小红书】\n分享链接已失效，或者对应直播已经结束。", nickname))
	case "live_link":
		return resolverPlatformTextResult(fmt.Sprintf("%s识别内容来自：【小红书】\n这是小红书直播链接，不是普通笔记；将继续尝试用沙盒浏览器读取直播页面。", nickname))
	case "unsupported_link":
		return resolverPlatformTextResult(fmt.Sprintf("%s识别内容来自：【小红书】\n该链接不是可识别的普通笔记链接。", nickname))
	case "login_required", "note_unavailable", "page_unavailable":
		// 小红书把分享链接甩到登录页、或者 HTML 里根本没带笔记数据时，抓页面这条路
		// 就到头了——但用浏览器打开同一条链接是能看到笔记的（09-22 实测：未登录的
		// 浏览器里 __INITIAL_STATE__ 有 noteDetailMap，而同一时刻直接抓 HTML 是空的）。
		// 所以这不是「笔记没了」，是这条抓取路径读不到，该换浏览器渲染去读。
		return p.xiaohongshuBrowserFallback(ctx, req, raw, nickname, "小红书没有在页面里直接给出笔记数据")
	case "request_failed":
		return resolverPlatformTextResult(fmt.Sprintf("%s识别内容来自：【小红书】\n页面暂时无法读取，不能据此判断ck已经失效。", nickname))
	}
	if len(note) == 0 {
		return resolverPlatformTextResult(fmt.Sprintf("%s识别内容来自：【小红书】\n没有读取到笔记内容，但不能据此判断ck已经失效。", nickname))
	}
	metaText := xiaohongshuMetaText(nickname, note)
	images := xiaohongshuMediaImageURLs(note)
	if strings.TrimSpace(anyString(note["type"])) == "normal" {
		nodes := []OutgoingMessage{{Text: metaText}}
		for _, imageURL := range images {
			nodes = append(nodes, OutgoingMessage{ImageURLs: []string{imageURL}})
		}
		return resolverPlatformResult{Context: metaText, ImageURLs: images, ForwardMessages: nodes}
	}
	if strings.TrimSpace(anyString(note["type"])) == "video" {
		cover := singleURL(firstNonEmptyString(images))
		videoPath := p.downloadResolverVideo(ctx, req, raw)
		if videoPath == "" {
			return resolverPlatformTextResult(fmt.Sprintf("%s识别内容来自：【小红书】\n视频直链均不可用，暂时无法发送视频。", nickname))
		}
		nodes := []OutgoingMessage{{Text: "\n" + metaText, ImageURLs: cover, ImagesFirst: true}, {VideoURLs: []string{videoPath}}}
		return resolverPlatformResult{Context: metaText, ImageURLs: cover, VideoURLs: []string{videoPath}, ForwardMessages: nodes}
	}
	return resolverPlatformTextResult(metaText)
}

func (p *ResolverPlugin) resolveTwitter(ctx context.Context, req PluginRequest, raw string) resolverPlatformResult {
	if id := twitterArticleID(raw); id != "" {
		result := p.resolveTwitterArticle(ctx, req, raw, id)
		return resolverPlatformResult{
			Context:         result.Context,
			ImageURLs:       result.ImageURLs,
			VideoURLs:       result.VideoURLs,
			ForwardMessages: resolverSocialForwardMessages(result),
		}
	}
	if handle := twitterProfileHandle(raw); handle != "" {
		result := p.resolveTwitterProfile(ctx, req, raw, handle)
		return resolverPlatformResult{Context: result.Context, ForwardMessages: []OutgoingMessage{{Text: result.Context}}}
	}
	if p.videoDownloader != nil && p.twitterPostFetcher == nil && p.twitterMediaDownloader == nil {
		return p.resolveTwitterLegacy(ctx, req, raw)
	}
	fetchPost := p.twitterPostFetcher
	if fetchPost == nil {
		fetchPost = fetchTwitterPost
	}
	post, ok := fetchPost(ctx, raw)
	if !ok {
		return p.resolveTwitterLegacy(ctx, req, raw)
	}
	metaText := twitterMetaText(resolverNickname(), post)
	nodes := []OutgoingMessage{{Text: metaText}}
	// 附带长文的推文在这条路上同样要把正文展开，两条路的可见结果必须一致。
	if articleText := twitterPostArticleText(post, raw); articleText != "" {
		nodes = append(nodes, OutgoingMessage{Text: articleText})
		metaText += "\n\n" + articleText
		post.Media = append(post.Media, post.Article.Media...)
	}
	post.Media = dedupeTwitterMedia(post.Media)
	if len(post.Media) == 0 {
		return resolverPlatformResult{Context: metaText, ForwardMessages: nodes}
	}
	images, videos, nodes := p.deliverTwitterMedia(ctx, req, raw, post.Media, nodes)
	return resolverPlatformResult{Context: metaText, ImageURLs: images, VideoURLs: videos, ForwardMessages: nodes}
}

func (p *ResolverPlugin) resolveTwitterLegacy(ctx context.Context, req PluginRequest, raw string) resolverPlatformResult {
	metaText := fmt.Sprintf("%s识别：小蓝鸟学习版", resolverNickname())
	if videoPath := p.downloadResolverVideo(ctx, req, raw); videoPath != "" {
		return resolverPlatformResult{Context: metaText, VideoURLs: []string{videoPath}, ForwardMessages: []OutgoingMessage{{Text: metaText}, {VideoURLs: []string{videoPath}}}}
	}
	if mediaURL := fetchTwitterMediaURL(ctx, raw); resolverMediaURLIsImage(mediaURL) {
		return resolverPlatformResult{Context: metaText, ImageURLs: []string{mediaURL}, ForwardMessages: []OutgoingMessage{{Text: metaText}, {ImageURLs: []string{mediaURL}}}}
	}
	return resolverPlatformTextResult(metaText + "\n媒体下载失败，可能是代理不可用、解析源失效或媒体链接被限制。")
}

func (p *ResolverPlugin) resolveYouTube(ctx context.Context, req PluginRequest, raw string) resolverPlatformResult {
	title := ""
	if info, ok := ytdlpDumpInfo(ctx, raw); ok {
		title = strings.TrimSpace(info.Title)
	}
	if title == "" {
		title = p.legacyPageTitle(ctx, raw)
	}
	text := fmt.Sprintf("%s识别：油管，%s", resolverNickname(), title)
	nodes := []OutgoingMessage{{Text: text}}
	if videoPath := p.downloadResolverVideo(ctx, req, raw); videoPath != "" {
		nodes = append(nodes, OutgoingMessage{VideoURLs: []string{videoPath}})
		return resolverPlatformResult{Context: strings.TrimSpace(text), VideoURLs: []string{videoPath}, ForwardMessages: nodes}
	}
	return resolverPlatformResult{Context: strings.TrimSpace(text), ForwardMessages: nodes}
}

func (p *ResolverPlugin) downloadResolverVideo(ctx context.Context, req PluginRequest, raw string) string {
	download := p.videoDownloader
	if download == nil {
		download = p.mediaDownloader
	}
	if download == nil {
		download = downloadPlatformVideoFile
	}
	videoPath := download(ctx, raw)
	recordResolverVideoLog(ctx, req, raw, videoPath)
	return videoPath
}

func fetchTwitterMediaURL(ctx context.Context, raw string) string {
	apiURL := configuredTwitterResolverURL(ctx, raw)
	if apiURL == "" {
		return ""
	}
	headers := resolverCommonHeaders()
	headers["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"
	var response struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if !fetchResolverJSON(ctx, apiURL, headers, &response) {
		return ""
	}
	return strings.TrimSpace(response.Data.URL)
}

func xiaohongshuMetaText(nickname string, note map[string]any) string {
	user, _ := note["user"].(map[string]any)
	return fmt.Sprintf("%s识别内容来自：【小红书】\n作者：%s\n标题：%s\n内容：%s", nickname, anyString(user["nickname"]), anyString(note["title"]), anyString(note["desc"]))
}

func extraBiliInfoText(view bilibiliViewResponse) string {
	lines := make([]string, 0, 3)
	if owner := strings.TrimSpace(view.Data.Owner.Name); owner != "" {
		lines = append(lines, "UP主："+owner)
	}
	stats := make([]string, 0, 4)
	if view.Data.Stat.View > 0 {
		stats = append(stats, fmt.Sprintf("播放：%d", view.Data.Stat.View))
	}
	if view.Data.Stat.Like > 0 {
		stats = append(stats, fmt.Sprintf("点赞：%d", view.Data.Stat.Like))
	}
	if view.Data.Stat.Coin > 0 {
		stats = append(stats, fmt.Sprintf("投币：%d", view.Data.Stat.Coin))
	}
	if view.Data.Stat.Favorite > 0 {
		stats = append(stats, fmt.Sprintf("收藏：%d", view.Data.Stat.Favorite))
	}
	if len(stats) > 0 {
		lines = append(lines, strings.Join(stats, "，"))
	}
	return strings.Join(lines, "\n")
}

func deleteResolverBoringCharacters(text string) string {
	replacer := strings.NewReplacer("/", " ", "\\", " ", ":", " ", "*", " ", "?", " ", "\"", " ", "<", " ", ">", " ", "|", " ")
	return compactWhitespace(replacer.Replace(text))
}
