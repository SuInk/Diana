// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// resolverPlatformResult preserves the original platform-specific forwarding
// contract for injected resolvers and older integrations.
type resolverPlatformResult struct {
	Context         string
	ImageURLs       []string
	VideoURLs       []string
	ForwardMessages []OutgoingMessage
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
	detail, ok, status := fetchDouyinMediaDetail(ctx, raw)
	if status == "missing_cookie" {
		return resolverPlatformTextResult(fmt.Sprintf("%s识别：抖音，无法获取到管理员设置的抖音ck！", nickname))
	}
	if !ok {
		return resolverPlatformTextResult(fmt.Sprintf("%s识别：抖音，解析失败！", nickname))
	}
	text := fmt.Sprintf("%s识别：抖音，%s", nickname, strings.TrimSpace(detail.Desc))
	if strings.TrimSpace(text) == nickname+"识别：抖音，" {
		text = fmt.Sprintf("%s识别：抖音", nickname)
	}
	if resolverDouyinMediaType(detail.AwemeType) == "image" {
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

func (p *ResolverPlugin) resolveXiaohongshu(ctx context.Context, req PluginRequest, raw string) resolverPlatformResult {
	nickname := resolverNickname()
	note, status := fetchXiaohongshuNote(ctx, raw)
	switch status {
	case "missing_cookie":
		return resolverPlatformTextResult(fmt.Sprintf("%s识别内容来自：【小红书】\n无法获取到管理员设置的小红书ck！", nickname))
	case "expired_link":
		return resolverPlatformTextResult(fmt.Sprintf("%s识别内容来自：【小红书】\n分享链接已失效，或者对应直播已经结束。", nickname))
	case "live_link":
		return resolverPlatformTextResult(fmt.Sprintf("%s识别内容来自：【小红书】\n这是小红书直播链接，不是普通笔记；将继续尝试用沙盒浏览器读取直播页面。", nickname))
	case "unsupported_link":
		return resolverPlatformTextResult(fmt.Sprintf("%s识别内容来自：【小红书】\n该链接不是可识别的普通笔记链接。", nickname))
	case "note_unavailable":
		return resolverPlatformTextResult(fmt.Sprintf("%s识别内容来自：【小红书】\n笔记不存在、已删除，或当前分享参数已经过期。", nickname))
	case "page_unavailable", "request_failed":
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
	if len(post.Media) == 0 {
		return resolverPlatformResult{Context: metaText, ForwardMessages: nodes}
	}
	downloadMedia := p.twitterMediaDownloader
	if downloadMedia == nil {
		downloadMedia = downloadTwitterMediaFile
	}
	resolved := make([]string, len(post.Media))
	var downloads sync.WaitGroup
	for index := range post.Media {
		index := index
		downloads.Add(1)
		go func() {
			defer downloads.Done()
			resolved[index] = downloadMedia(ctx, post.Media[index])
		}()
	}
	downloads.Wait()
	images := make([]string, 0, len(post.Media))
	videos := make([]string, 0, len(post.Media))
	localImages := make([]string, 0, len(post.Media))
	failed := 0
	for index, media := range post.Media {
		mediaPath := strings.TrimSpace(resolved[index])
		if mediaPath == "" {
			failed++
			continue
		}
		if media.sendAsImage() {
			images = append(images, mediaPath)
			nodes = append(nodes, OutgoingMessage{ImageURLs: []string{mediaPath}})
			if localPath := localMediaPath(mediaPath); localPath != "" {
				if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
					localImages = append(localImages, localPath)
				}
			}
			continue
		}
		videos = append(videos, mediaPath)
		nodes = append(nodes, OutgoingMessage{VideoURLs: []string{mediaPath}})
		recordResolverVideoLog(ctx, req, raw, mediaPath)
	}
	if failed > 0 {
		nodes = append(nodes, OutgoingMessage{Text: fmt.Sprintf("有 %d 个媒体下载失败，未发送。", failed)})
	}
	cleanupLocalMediaFilesLater(localImages, resolverLocalMediaTTL)
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
