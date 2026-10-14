// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	heyboxLinkTreePath = "/bbs/app/link/tree"
	heyboxTable        = "AB45STUVWZEFGJ6CH01D237IXYPQRKLMN89"
	heyboxBodyMaxRunes = 800
)

var (
	heyboxPathPattern  = regexp.MustCompile(`/app/bbs/link/([0-9A-Za-z]+)`)
	heyboxImgPattern   = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	heyboxImgAttr      = regexp.MustCompile(`(?is)\b(?:data-original|data-actualsrc|data-default-watermark-src)\s*=\s*"([^"]+)"`)
	heyboxBlockPattern = regexp.MustCompile(`(?i)</p>|<br\s*/?>|</div>`)
	heyboxBlankLines   = regexp.MustCompile(`\n{3,}`)
)

// heyboxLinkID 取出帖子 ID：App 分享的 /app/bbs/link/<id>，或 api 域名分享页的 link_id 参数。
// 游戏页等其他小黑盒链接返回空，由通用抓取接手。
func heyboxLinkID(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !hostMatchesDomain(strings.ToLower(parsed.Hostname()), "xiaoheihe.cn") {
		return ""
	}
	if match := heyboxPathPattern.FindStringSubmatch(parsed.Path); len(match) > 1 {
		return match[1]
	}
	if strings.Contains(parsed.Path, "/web/share") {
		if id := parsed.Query().Get("link_id"); id != "" && regexp.MustCompile(`^[0-9A-Za-z]+$`).MatchString(id) {
			return id
		}
	}
	return ""
}

func heyboxMapChars(text string, table string) string {
	out := make([]byte, 0, len(text))
	for _, r := range text {
		out = append(out, table[int(r)%len(table)])
	}
	return string(out)
}

func heyboxInterleave(parts ...string) string {
	longest := 0
	for _, part := range parts {
		longest = max(longest, len(part))
	}
	var out []byte
	for i := 0; i < longest; i++ {
		for _, part := range parts {
			if i < len(part) {
				out = append(out, part[i])
			}
		}
	}
	return string(out)
}

func heyboxXtime(v int) int {
	if v&0x80 != 0 {
		return ((v << 1) ^ 27) & 0xFF
	}
	return v << 1
}

func heyboxMul3(v int) int  { return heyboxXtime(v) ^ v }
func heyboxMul6(v int) int  { return heyboxMul3(heyboxXtime(v)) }
func heyboxMul12(v int) int { return heyboxMul6(heyboxMul3(heyboxXtime(v))) }
func heyboxMul14(v int) int { return heyboxMul12(v) ^ heyboxMul6(v) ^ heyboxMul3(v) }

// heyboxHkey 复刻小黑盒网页端的请求签名：时间戳、路径、nonce 交错取字符后过 md5，
// 再对尾部 6 个字符做一次 MixColumns 求和作为校验位。纯本地计算，不需要设备指纹。
func heyboxHkey(path string, timestamp int64, nonce string) string {
	var segments []string
	for _, segment := range strings.Split(path, "/") {
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	normalized := "/" + strings.Join(segments, "/") + "/"
	mixed := heyboxInterleave(
		heyboxMapChars(strconv.FormatInt(timestamp, 10), heyboxTable[:len(heyboxTable)-2]),
		heyboxMapChars(normalized, heyboxTable),
		heyboxMapChars(nonce, heyboxTable),
	)
	if len(mixed) > 20 {
		mixed = mixed[:20]
	}
	sum := md5.Sum([]byte(mixed))
	digest := hex.EncodeToString(sum[:])
	tail := []int{int(digest[26]), int(digest[27]), int(digest[28]), int(digest[29]), int(digest[30]), int(digest[31])}
	columns := []int{
		heyboxMul14(tail[0]) ^ heyboxMul12(tail[1]) ^ heyboxMul6(tail[2]) ^ heyboxMul3(tail[3]),
		heyboxMul3(tail[0]) ^ heyboxMul14(tail[1]) ^ heyboxMul12(tail[2]) ^ heyboxMul6(tail[3]),
		heyboxMul6(tail[0]) ^ heyboxMul3(tail[1]) ^ heyboxMul14(tail[2]) ^ heyboxMul12(tail[3]),
		heyboxMul12(tail[0]) ^ heyboxMul6(tail[1]) ^ heyboxMul3(tail[2]) ^ heyboxMul14(tail[3]),
	}
	total := tail[4] + tail[5]
	for _, value := range columns {
		total += value
	}
	return heyboxMapChars(digest[:5], heyboxTable[:len(heyboxTable)-4]) + fmt.Sprintf("%02d", total%100)
}

func heyboxNonce(timestamp int64) string {
	salt := make([]byte, 8)
	_, _ = rand.Read(salt)
	sum := md5.Sum([]byte(strconv.FormatInt(timestamp, 10) + hex.EncodeToString(salt)))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// heyboxLinkTreeURL 构造带签名的帖子接口地址。nonce 只能算一次，URL 参数和签名共用；
// 请求里不带 x_xhh_tokenid：带上设备指纹领来的 token 反而会触发验证码。
func heyboxLinkTreeURL(linkID string, timestamp int64, nonce string) string {
	query := url.Values{}
	query.Set("os_type", "web")
	query.Set("app", "heybox")
	query.Set("client_type", "web")
	query.Set("version", "999.0.4")
	query.Set("_time", strconv.FormatInt(timestamp, 10))
	query.Set("nonce", nonce)
	query.Set("hkey", heyboxHkey(heyboxLinkTreePath, timestamp+1, nonce))
	query.Set("link_id", linkID)
	query.Set("page", "1")
	query.Set("index", "1")
	query.Set("limit", "5")
	query.Set("x_client_type", "weboutapp")
	query.Set("x_app", "heybox_website")
	query.Set("x_os_type", "Windows")
	query.Set("web_version", "2.5")
	return "https://api.xiaoheihe.cn" + heyboxLinkTreePath + "?" + query.Encode()
}

type heyboxLink struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Text        string `json:"text"`
	HasVideo    int    `json:"has_video"`
	VideoURL    string `json:"video_url"`
	VideoThumb  string `json:"video_thumb"`
	Click       int    `json:"click"`
	LikeNum     int    `json:"link_award_num"`
	CommentNum  int    `json:"comment_num"`
	User        struct {
		Username string `json:"username"`
	} `json:"user"`
}

type heyboxResponse struct {
	Status string `json:"status"`
	Result struct {
		Link *heyboxLink `json:"link"`
	} `json:"result"`
}

// parseHeyboxResponse 解析帖子接口返回；失败时 reason 是 captcha / error。
func parseHeyboxResponse(body []byte) (heyboxLink, string) {
	var resp heyboxResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return heyboxLink{}, "error"
	}
	if resp.Status == "show_captcha" {
		return heyboxLink{}, "captcha"
	}
	if resp.Status != "ok" || resp.Result.Link == nil {
		return heyboxLink{}, "error"
	}
	return *resp.Result.Link, ""
}

// heyboxImageURL：带查询参数的 CDN 地址末尾要补一个反斜杠才取得到原图。
func heyboxImageURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), `\`)
	if strings.Contains(raw, "?") {
		return raw + `\`
	}
	return raw
}

// heyboxBody 把帖子 text 字段（一段 JSON，分 html / text / img 段）拆成纯文本和图片地址。
// html 段已经带全了图文，遇到它就不再看后面重复的 img 段。
func heyboxBody(text string) (string, []string) {
	var segments []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal([]byte(text), &segments); err != nil {
		return heyboxPlainText(text), nil
	}
	var texts, images []string
	for _, segment := range segments {
		switch segment.Type {
		case "html":
			for _, tag := range heyboxImgPattern.FindAllString(segment.Text, -1) {
				if match := heyboxImgAttr.FindStringSubmatch(tag); len(match) > 1 {
					images = append(images, heyboxImageURL(html.UnescapeString(match[1])))
				}
			}
			if plain := heyboxPlainText(segment.Text); plain != "" {
				texts = append(texts, plain)
			}
			return strings.Join(texts, "\n"), dedupeMediaURLs(images)
		case "text":
			if plain := heyboxPlainText(segment.Text); plain != "" {
				texts = append(texts, plain)
			}
		case "img":
			if segment.URL != "" {
				images = append(images, heyboxImageURL(segment.URL))
			}
		}
	}
	return strings.Join(texts, "\n"), dedupeMediaURLs(images)
}

func heyboxPlainText(raw string) string {
	text := heyboxImgPattern.ReplaceAllString(raw, "")
	text = heyboxBlockPattern.ReplaceAllString(text, "\n")
	text = htmlTagPattern.ReplaceAllString(text, "")
	text = strings.ReplaceAll(html.UnescapeString(text), "\u00a0", " ")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	text = heyboxBlankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(text)
}

func heyboxContext(link heyboxLink, body string) string {
	lines := []string{"[小黑盒] " + strings.TrimSpace(link.Title)}
	if name := strings.TrimSpace(link.User.Username); name != "" {
		lines = append(lines, "作者："+name)
	}
	lines = append(lines, fmt.Sprintf("浏览 %d · 赞 %d · 评论 %d", link.Click, link.LikeNum, link.CommentNum))
	if body = firstNonEmpty(body, strings.TrimSpace(link.Description)); body != "" {
		lines = append(lines, truncateRunes(body, heyboxBodyMaxRunes))
	}
	return strings.Join(lines, "\n")
}

func fetchHeyboxLink(ctx context.Context, linkID string) (heyboxLink, string) {
	timestamp := time.Now().Unix()
	headers := map[string]string{
		"User-Agent": resolverUserAgent,
		"Referer":    "https://www.xiaoheihe.cn/",
		"Origin":     "https://www.xiaoheihe.cn",
		"Accept":     "application/json, text/plain, */*",
	}
	body, ok := fetchResolverBody(ctx, heyboxLinkTreeURL(linkID, timestamp, heyboxNonce(timestamp)), headers)
	if !ok {
		return heyboxLink{}, "error"
	}
	return parseHeyboxResponse([]byte(body))
}

func (p *ResolverPlugin) resolveHeyboxMedia(ctx context.Context, req PluginRequest, raw string, maxImages int) resolverSocialResult {
	id := heyboxLinkID(raw)
	if id == "" {
		return resolverSocialResult{}
	}
	result := resolverSocialResult{Handled: true}
	link, reason := fetchHeyboxLink(ctx, id)
	if reason != "" {
		// 同一出口 IP 请求太频繁会被要求验证码：如实说明，不重试，也不占去重窗口。
		result.Failed = true
		if reason == "captcha" {
			result.Context = "[小黑盒] 被小黑盒风控要求验证了，稍后再试。"
		} else {
			result.Context = "[小黑盒] 帖子暂时无法读取。"
		}
		recordResolverMediaLog(ctx, req, raw, "heybox", false, reason)
		return result
	}
	body, images := heyboxBody(link.Text)
	result.Context = heyboxContext(link, body)
	result.ResourceKeys = []string{"heybox:" + id}
	if link.HasVideo == 0 || strings.TrimSpace(link.VideoURL) == "" {
		result.ImageURLs = limitStrings(images, maxImages)
		recordResolverMediaLog(ctx, req, raw, "heybox", true, "")
		return result
	}
	result.ImageURLs = singleURL(firstNonEmpty(link.VideoThumb, firstNonEmptyString(images)))
	headers := map[string]string{"User-Agent": resolverUserAgent, "Referer": "https://www.xiaoheihe.cn/"}
	if path := downloadGenericVideoFile(ctx, strings.TrimSpace(link.VideoURL), headers); path != "" {
		result.VideoURLs = []string{path}
		recordResolverMediaLog(ctx, req, raw, "heybox", true, "")
		return result
	}
	result.Context += "\n媒体下载失败：视频文件下载失败或超出限制。"
	recordResolverMediaLog(ctx, req, raw, "heybox", false, "video_download_failed")
	return result
}
