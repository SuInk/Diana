package assistant

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os/exec"
	"strings"

	"github.com/SuInk/diana/model/applog"
)

type resolverSocialResult struct {
	Handled   bool
	Context   string
	ImageURLs []string
	VideoURLs []string
}

func hasKnownResolverMediaURL(event MessageEvent, text string) bool {
	source := strings.Join([]string{text, event.RawMessage, PlainText(event.Segments)}, "\n")
	for _, raw := range extractURLs(source) {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if key, _ := platformKeyAndLabel(parsed.Hostname()); platformSupportsMedia(key) {
			return true
		}
	}
	return false
}

func (p *ResolverPlugin) resolveSocialMedia(ctx context.Context, req PluginRequest, raw string, maxImages int) resolverSocialResult {
	parsed, err := url.Parse(raw)
	if err != nil {
		return resolverSocialResult{}
	}
	platform, label := platformKeyAndLabel(parsed.Hostname())
	if !platformSupportsMedia(platform) {
		// 只能抓标题的平台走通用元数据路径，不进媒体分发。
		return resolverSocialResult{}
	}
	switch platform {
	case "bilibili":
		return p.resolveBilibiliMedia(ctx, req, raw)
	case "douyin":
		return p.resolveDouyinMedia(ctx, req, raw, maxImages)
	case "xiaohongshu":
		return p.resolveXiaohongshuMedia(ctx, req, raw, maxImages)
	case "youtube", "x":
		return p.resolveYTDLPMedia(ctx, req, raw, label)
	default:
		return resolverSocialResult{}
	}
}

func (p *ResolverPlugin) resolveBilibiliMedia(ctx context.Context, req PluginRequest, raw string) resolverSocialResult {
	result := resolverSocialResult{Handled: true}
	view, ok := fetchBilibiliView(ctx, raw)
	if ok {
		result.Context = fmt.Sprintf("[Bilibili] %s", strings.TrimSpace(view.Data.Title))
		if owner := strings.TrimSpace(view.Data.Owner.Name); owner != "" {
			result.Context += "\nUP主：" + owner
		}
		if desc := strings.TrimSpace(view.Data.Desc); desc != "" {
			result.Context += "\n简介：" + truncateRunes(desc, 240)
		}
		result.ImageURLs = singleURL(view.Data.Pic)
		if view.Data.Duration > resolverVideoMaxDuration(ctx) {
			result.Context += fmt.Sprintf("\n视频时长超过限制（%d 秒），未下载。", resolverVideoMaxDuration(ctx))
			recordResolverMediaLog(ctx, req, raw, "bilibili", false, "duration limit")
			return result
		}
	} else {
		result.Context = "[Bilibili] 已识别链接，但元数据读取失败。"
	}
	return p.attachDownloadedVideo(ctx, req, raw, "bilibili", result)
}

func (p *ResolverPlugin) resolveDouyinMedia(ctx context.Context, req PluginRequest, raw string, maxImages int) resolverSocialResult {
	result := resolverSocialResult{Handled: true}
	detail, ok, status := fetchDouyinMediaDetail(ctx, raw)
	if status == "missing_cookie" {
		result.Context = "[抖音] 需要在插件设置里填写抖音 Cookie（或配置 DIANA_DOUYIN_CK）后才能解析。"
		recordResolverMediaLog(ctx, req, raw, "douyin", false, status)
		return result
	}
	if !ok {
		result.Context = "[抖音] 链接已识别，但平台接口解析失败。"
		recordResolverMediaLog(ctx, req, raw, "douyin", false, "metadata unavailable")
		return result
	}
	result.Context = "[抖音] " + strings.TrimSpace(detail.Desc)
	if result.Context == "[抖音] " {
		result.Context = "[抖音] 已识别内容"
	}
	if resolverDouyinMediaType(detail.AwemeType) == "image" {
		result.ImageURLs = limitStrings(douyinMediaImageURLs(detail), maxImages)
		if len(result.ImageURLs) == 0 {
			result.Context += "\n图集解析成功，但没有取得可发送的图片地址。"
		}
		recordResolverMediaLog(ctx, req, raw, "douyin", len(result.ImageURLs) > 0, "")
		return result
	}
	result.ImageURLs = singleURL(firstNonEmptyString(detail.Video.Cover.URLList))
	return p.attachDownloadedVideo(ctx, req, raw, "douyin", result)
}

func (p *ResolverPlugin) resolveXiaohongshuMedia(ctx context.Context, req PluginRequest, raw string, maxImages int) resolverSocialResult {
	result := resolverSocialResult{Handled: true}
	note, status := fetchXiaohongshuNote(ctx, raw)
	if status != "" {
		switch status {
		case "missing_cookie":
			result.Context = "[小红书] 需要在插件设置里填写小红书 Cookie（或配置 DIANA_XHS_CK）后才能解析笔记。"
		case "expired_link":
			result.Context = "[小红书] 分享链接已失效。"
		case "live_link":
			result.Context = "[小红书] 暂不支持直播链接。"
		default:
			result.Context = "[小红书] 页面暂时无法读取：" + status
		}
		recordResolverMediaLog(ctx, req, raw, "xiaohongshu", false, status)
		return result
	}
	user, _ := note["user"].(map[string]any)
	result.Context = fmt.Sprintf("[小红书] %s\n作者：%s\n%s",
		strings.TrimSpace(anyString(note["title"])),
		strings.TrimSpace(anyString(user["nickname"])),
		truncateRunes(strings.TrimSpace(anyString(note["desc"])), 240),
	)
	if strings.TrimSpace(anyString(note["type"])) == "normal" {
		result.ImageURLs = limitStrings(xiaohongshuMediaImageURLs(note), maxImages)
		recordResolverMediaLog(ctx, req, raw, "xiaohongshu", len(result.ImageURLs) > 0, "")
		return result
	}
	result.ImageURLs = singleURL(firstNonEmptyString(xiaohongshuMediaImageURLs(note)))
	return p.attachDownloadedVideo(ctx, req, raw, "xiaohongshu", result)
}

func (p *ResolverPlugin) resolveYTDLPMedia(ctx context.Context, req PluginRequest, raw, platform string) resolverSocialResult {
	result := resolverSocialResult{Handled: true, Context: "[" + platform + "] 已识别链接"}
	if info, ok := ytdlpDumpInfo(ctx, raw); ok {
		if title := strings.TrimSpace(info.Title); title != "" {
			result.Context = "[" + platform + "] " + title
		}
		if desc := strings.TrimSpace(info.Description); desc != "" {
			result.Context += "\n简介：" + truncateRunes(desc, 240)
		}
		result.ImageURLs = singleURL(info.Thumbnail)
		if info.Duration > float64(resolverVideoMaxDuration(ctx)) {
			result.Context += fmt.Sprintf("\n视频时长超过限制（%d 秒），未下载。", resolverVideoMaxDuration(ctx))
			recordResolverMediaLog(ctx, req, raw, platform, false, "duration limit")
			return result
		}
	}
	return p.attachDownloadedVideo(ctx, req, raw, platform, result)
}

func (p *ResolverPlugin) attachDownloadedVideo(ctx context.Context, req PluginRequest, raw, platform string, result resolverSocialResult) resolverSocialResult {
	download := p.mediaDownloader
	if download == nil {
		download = downloadPlatformVideoFile
	}
	path := download(ctx, raw)
	if path != "" {
		result.VideoURLs = []string{path}
		recordResolverMediaLog(ctx, req, raw, platform, true, "")
		return result
	}
	reason := resolverDownloadFailureHint(ctx, raw)
	result.Context += "\n媒体下载失败：" + reason
	recordResolverMediaLog(ctx, req, raw, platform, false, reason)
	return result
}

type douyinMediaDetail struct {
	Desc      string `json:"desc"`
	AwemeType int    `json:"aweme_type"`
	Video     struct {
		Cover struct {
			URLList []string `json:"url_list"`
		} `json:"cover"`
	} `json:"video"`
	Images []struct {
		URLList []string `json:"url_list"`
	} `json:"images"`
}

func fetchDouyinMediaDetail(ctx context.Context, raw string) (douyinMediaDetail, bool, string) {
	cookie := resolverDouyinCookie(ctx)
	if cookie == "" {
		return douyinMediaDetail{}, false, "missing_cookie"
	}
	pageURL := fetchFinalURL(ctx, raw, resolverCommonHeaders())
	if pageURL == "" {
		pageURL = raw
	}
	match := douyinIDPattern.FindStringSubmatch(pageURL)
	if len(match) < 2 {
		return douyinMediaDetail{}, false, "unsupported_link"
	}
	awemeID := match[1]
	headers := resolverCommonHeaders()
	headers["Referer"] = "https://www.douyin.com/video/" + awemeID
	headers["Cookie"] = cookie
	apiURL := fmt.Sprintf(douyinVideoAPI, awemeID)
	if bogus := generateDouyinABogus(ctx, apiURL, headers["User-Agent"]); bogus != "" {
		apiURL += "&a_bogus=" + url.QueryEscape(bogus)
	}
	var response struct {
		AwemeDetail douyinMediaDetail `json:"aweme_detail"`
	}
	if !fetchResolverJSON(ctx, apiURL, headers, &response) {
		return douyinMediaDetail{}, false, "request_failed"
	}
	return response.AwemeDetail, true, ""
}

func resolverDouyinMediaType(code int) string {
	switch code {
	case 2, 68, 150:
		return "image"
	default:
		return "video"
	}
}

func douyinMediaImageURLs(detail douyinMediaDetail) []string {
	out := make([]string, 0, len(detail.Images))
	for _, image := range detail.Images {
		if value := firstNonEmptyString(image.URLList); value != "" {
			out = append(out, value)
		}
	}
	return dedupeMediaURLs(out)
}

func xiaohongshuMediaImageURLs(note map[string]any) []string {
	items, _ := note["imageList"].([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		image, _ := item.(map[string]any)
		for _, key := range []string{"urlDefault", "urlPre", "url"} {
			if value := strings.TrimSpace(anyString(image[key])); value != "" {
				out = append(out, value)
				break
			}
		}
	}
	return dedupeMediaURLs(out)
}

// resolverDownloadFailureHint 给出尽量具体的失败原因。
//
// 原先无论什么情况都回一句「平台限制、Cookie 失效或媒体超过大小限制」，
// 而 Cookie 过期恰恰是最高频的故障，用户从那句话里根本排查不出来。
// 这里按「缺依赖 → 缺凭据 → 超限 → 兜底」的顺序逐层缩小范围。
func resolverDownloadFailureHint(ctx context.Context, raw string) string {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		return "运行环境未安装 yt-dlp"
	}
	if isBilibiliURL(raw) {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			return "运行环境未安装 ffmpeg（B 站音视频需要合流）"
		}
	}
	return resolverCredentialFailureHint(ctx, raw)
}

// resolverCredentialFailureHint 在外部依赖齐全的前提下，按平台给出凭据或限额相关的原因。
// 和依赖检查拆开，既让判断逻辑清晰，也让这部分能在没装 yt-dlp 的环境里独立测试。
func resolverCredentialFailureHint(ctx context.Context, raw string) string {
	switch {
	case isDouyinURL(raw):
		if resolverDouyinCookie(ctx) == "" {
			return "未配置抖音 Cookie，请在插件设置里填写"
		}
		return "抖音 Cookie 可能已失效，或视频超过大小/时长上限"
	case isXiaohongshuURL(raw):
		if resolverXHSCookie(ctx) == "" {
			return "未配置小红书 Cookie，请在插件设置里填写"
		}
		return "小红书 Cookie 可能已失效，或视频超过大小/时长上限"
	case isBilibiliURL(raw):
		if _, err := exec.LookPath("node"); err != nil && bilibiliSessdata(ctx) == "" {
			return "B 站未配置 SESSDATA，大会员或需登录内容无法下载"
		}
		return "B 站限流、SESSDATA 失效，或视频超过大小/时长上限"
	}
	if resolverProxyURL(ctx) == "" && (isTwitterURL(raw) || strings.Contains(strings.ToLower(raw), "youtu")) {
		return "未配置代理，境外平台可能无法访问；也可能超过大小/时长上限"
	}
	return "平台限制、凭据失效或媒体超过大小/时长上限"
}

func recordResolverMediaLog(ctx context.Context, req PluginRequest, raw, platform string, success bool, detail string) {
	level := applog.LevelInfo
	kind := applog.KindOperation
	message := "社交媒体解析完成"
	if !success {
		level = applog.LevelError
		kind = applog.KindError
		message = "社交媒体解析失败"
	}
	log.Printf("resolver media platform=%s success=%t url=%s detail=%s", platform, success, redactURLQuery(raw), detail)
	if req.AppLogs == nil {
		return
	}
	_ = req.AppLogs.AppendLog(ctx, applog.Entry{
		Kind:    kind,
		Level:   level,
		Action:  "assistant.resolver.media",
		Message: message,
		Detail:  detail,
		Actor:   strings.TrimSpace(req.Event.UserID),
		Target:  redactURLQuery(raw),
		Metadata: map[string]any{
			"platform": platform,
			"group_id": req.Event.GroupID,
		},
	})
}

func singleURL(value string) []string {
	if value = strings.TrimSpace(value); value != "" {
		return []string{value}
	}
	return nil
}

func limitStrings(values []string, limit int) []string {
	values = dedupeMediaURLs(values)
	if limit > 0 && len(values) > limit {
		return values[:limit]
	}
	return values
}

func dedupeMediaURLs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func truncateRunes(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "..."
}
