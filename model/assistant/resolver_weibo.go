// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

const (
	weiboStatusURL      = "https://m.weibo.cn/statuses/show?id=%s&_=%d"
	weiboBodyMaxRunes   = 800
	weiboErrnoInvisible = "20170"
)

var (
	weiboMobilePattern  = regexp.MustCompile(`(?i)weibo\.cn/(?:status|detail|\d+)/([0-9A-Za-z]+)`)
	weiboDesktopPattern = regexp.MustCompile(`(?i)weibo\.com/\d+/([0-9A-Za-z]+)`)
	weiboTVPattern      = regexp.MustCompile(`(?i)weibo\.com/tv/show/\d+:\d+\?(?:[^#]*&)?mid=(\d+)`)
	weiboEmojiPattern   = regexp.MustCompile(`(?is)<img[^>]*\balt=["']([^"']*)["'][^>]*>`)
	weiboBreakPattern   = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlTagPattern      = regexp.MustCompile(`(?s)<[^>]*>`)
)

// weiboStatusID 从微博链接里取出 statuses/show 能认的 ID（数字 mid 或 base62 bid）。
// 微博直播、文章、video.weibo.com 的 fid 链接需要别的接口，返回空让通用抓取接手。
func weiboStatusID(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if !hostMatchesDomain(host, "weibo.com", "weibo.cn") || host == "video.weibo.com" {
		return ""
	}
	target := host + parsed.EscapedPath()
	if parsed.RawQuery != "" {
		target += "?" + parsed.RawQuery
	}
	if match := weiboTVPattern.FindStringSubmatch(target); len(match) > 1 {
		return weiboMidToID(match[1])
	}
	if strings.Contains(parsed.Path, "/ttarticle/") || strings.Contains(parsed.Path, "/article/") {
		return ""
	}
	for _, pattern := range []*regexp.Regexp{weiboMobilePattern, weiboDesktopPattern} {
		if match := pattern.FindStringSubmatch(target); len(match) > 1 {
			return match[1]
		}
	}
	return ""
}

const weiboBase62 = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func weiboBase62Encode(number int64) string {
	if number == 0 {
		return "0"
	}
	var out []byte
	for number > 0 {
		out = append([]byte{weiboBase62[number%62]}, out...)
		number /= 62
	}
	return string(out)
}

// weiboMidToID 把数字 mid 转成 base62 的 bid，每 7 位一段，非首段补零到 4 位。
func weiboMidToID(mid string) string {
	var chunks []string
	for end := len(mid); end > 0; end -= 7 {
		start := end - 7
		if start < 0 {
			start = 0
		}
		chunks = append([]string{mid[start:end]}, chunks...)
	}
	for i, chunk := range chunks {
		number, err := strconv.ParseInt(chunk, 10, 64)
		if err != nil {
			return ""
		}
		encoded := weiboBase62Encode(number)
		if i > 0 && len(encoded) < 4 {
			encoded = strings.Repeat("0", 4-len(encoded)) + encoded
		}
		chunks[i] = encoded
	}
	return strings.Join(chunks, "")
}

type weiboStatus struct {
	ID        string `json:"id"`
	Bid       string `json:"bid"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
	User      struct {
		ScreenName string `json:"screen_name"`
	} `json:"user"`
	Pics []struct {
		URL   string `json:"url"`
		Large struct {
			URL string `json:"url"`
		} `json:"large"`
	} `json:"pics"`
	PageInfo *struct {
		Title   string `json:"title"`
		PagePic struct {
			URL string `json:"url"`
		} `json:"page_pic"`
		URLs struct {
			MP4720p string `json:"mp4_720p_mp4"`
			MP4HD   string `json:"mp4_hd_mp4"`
			MP4LD   string `json:"mp4_ld_mp4"`
		} `json:"urls"`
	} `json:"page_info"`
	RetweetedStatus *weiboStatus `json:"retweeted_status"`
}

type weiboResponse struct {
	OK    int         `json:"ok"`
	Errno string      `json:"errno"`
	Msg   string      `json:"msg"`
	Data  weiboStatus `json:"data"`
}

func (s weiboStatus) videoURL() string {
	if s.PageInfo == nil {
		return ""
	}
	return firstNonEmpty(s.PageInfo.URLs.MP4720p, s.PageInfo.URLs.MP4HD, s.PageInfo.URLs.MP4LD)
}

func (s weiboStatus) imageURLs() []string {
	var urls []string
	for _, pic := range s.Pics {
		urls = append(urls, firstNonEmpty(pic.Large.URL, pic.URL))
	}
	return dedupeMediaURLs(urls)
}

// weiboPlainText 把微博正文的 HTML 还原成纯文本：<br> 换行，表情图用 alt（[哈哈]），其余标签丢弃。
func weiboPlainText(raw string) string {
	text := weiboEmojiPattern.ReplaceAllString(raw, "$1")
	text = weiboBreakPattern.ReplaceAllString(text, "\n")
	text = htmlTagPattern.ReplaceAllString(text, "")
	text = strings.ReplaceAll(html.UnescapeString(text), "\u200b", "")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// parseWeiboResponse 解析 statuses/show 的返回；失败时 reason 是 invisible / error。
func parseWeiboResponse(body []byte) (weiboStatus, string) {
	var resp weiboResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return weiboStatus{}, "error"
	}
	if resp.OK != 1 {
		if resp.Errno == weiboErrnoInvisible {
			return weiboStatus{}, "invisible"
		}
		return weiboStatus{}, "error"
	}
	return resp.Data, ""
}

func weiboHeaders(id string) map[string]string {
	return map[string]string{
		"User-Agent":       resolverUserAgent(),
		"Accept":           "application/json, text/plain, */*",
		"Referer":          "https://m.weibo.cn/detail/" + id,
		"Origin":           "https://m.weibo.cn",
		"X-Requested-With": "XMLHttpRequest",
		"MWeibo-Pwa":       "1",
		"Sec-Fetch-Site":   "same-origin",
		"Sec-Fetch-Mode":   "cors",
		"Sec-Fetch-Dest":   "empty",
	}
}

// fetchWeiboStatus 不带 Cookie 且不跟随重定向：带了 Cookie 或被重定向到访客系统，
// 接口就只会回访客页。
func fetchWeiboStatus(ctx context.Context, id string) (weiboStatus, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(weiboStatusURL, url.QueryEscape(id), time.Now().UnixMilli()), nil)
	if err != nil {
		return weiboStatus{}, "error"
	}
	for key, value := range weiboHeaders(id) {
		req.Header.Set(key, value)
	}
	client := netguard.NewPublicHTTPClient(defaultPlatformTimeout)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("resolver weibo request failed: %v", err)
		return weiboStatus{}, "error"
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == 418 {
		return weiboStatus{}, "blocked"
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return weiboStatus{}, "error"
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return weiboStatus{}, "error"
	}
	return parseWeiboResponse(body)
}

func weiboContext(status weiboStatus) string {
	lines := []string{fmt.Sprintf("[微博] %s", strings.TrimSpace(status.User.ScreenName))}
	if text := weiboPlainText(status.Text); text != "" {
		lines = append(lines, truncateRunes(text, weiboBodyMaxRunes))
	}
	if status.PageInfo != nil {
		if title := strings.TrimSpace(status.PageInfo.Title); title != "" && status.videoURL() != "" {
			lines = append(lines, "视频："+title)
		}
	}
	if retweet := status.RetweetedStatus; retweet != nil {
		line := fmt.Sprintf("转发自 @%s", strings.TrimSpace(retweet.User.ScreenName))
		if text := weiboPlainText(retweet.Text); text != "" {
			line += "：" + truncateRunes(text, weiboBodyMaxRunes)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (p *ResolverPlugin) resolveWeiboMedia(ctx context.Context, req PluginRequest, raw string, maxImages int) resolverSocialResult {
	id := weiboStatusID(raw)
	if id == "" {
		return resolverSocialResult{}
	}
	result := resolverSocialResult{Handled: true}
	status, reason := fetchWeiboStatus(ctx, id)
	if reason != "" {
		result.Failed = true
		switch reason {
		case "invisible":
			result.Context = "[微博] 这条微博因博主设置暂不可见。"
		case "blocked":
			result.Context = "[微博] 被微博风控拦截了，稍后再试。"
		default:
			result.Context = "[微博] 页面暂时无法读取。"
		}
		recordResolverMediaLog(ctx, req, raw, "weibo", false, reason)
		return result
	}
	result.Context = weiboContext(status)
	result.ResourceKeys = []string{"weibo:" + id}
	images := status.imageURLs()
	if len(images) == 0 && status.RetweetedStatus != nil {
		images = status.RetweetedStatus.imageURLs()
	}
	videoURL := firstNonEmpty(status.videoURL())
	if videoURL == "" && status.RetweetedStatus != nil {
		videoURL = status.RetweetedStatus.videoURL()
	}
	if videoURL == "" {
		result.ImageURLs = limitStrings(images, maxImages)
		recordResolverMediaLog(ctx, req, raw, "weibo", true, "")
		return result
	}
	cover := ""
	if status.PageInfo != nil {
		cover = status.PageInfo.PagePic.URL
	}
	result.ImageURLs = singleURL(firstNonEmpty(cover, firstNonEmptyString(images)))
	headers := map[string]string{"User-Agent": resolverUserAgent(), "Referer": "https://weibo.com/"}
	if path := downloadGenericVideoFile(ctx, videoURL, headers); path != "" {
		result.VideoURLs = []string{path}
		recordResolverMediaLog(ctx, req, raw, "weibo", true, "")
		return result
	}
	result.Context += "\n媒体下载失败：视频文件下载失败或超出限制。"
	recordResolverMediaLog(ctx, req, raw, "weibo", false, "video_download_failed")
	return result
}
