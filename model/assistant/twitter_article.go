// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// 长文正文动辄上万字，整篇塞进上下文会挤掉对话历史，这里按字数截断，
	// 截断后仍然明确告诉模型「后面还有」，避免它把残篇当全文点评。
	maximumTwitterArticleRunes = 4000
	// 一篇长文最多取几张配图；正文才是主体，配图只做补充。
	maximumTwitterArticleImages = 6
	// 长文索引的存活时间：群里通常是先发推文、随后才要正文，几个小时足够。
	twitterArticleIndexTTL     = 6 * time.Hour
	twitterArticleIndexEntries = 32
	// 单篇正文超过这个长度就不进索引，避免长期占着内存。
	twitterArticleIndexMaxBytes = 128 * 1024

	defaultTwitterArticleAPIEnv = "DIANA_TWITTER_ARTICLE_API"
)

// twitterArticle 是一篇 X 站内长文（Article）的可发送形态。
type twitterArticle struct {
	ID    string
	Title string
	Text  string
	Media []twitterMedia
}

func (a twitterArticle) hasContent() bool {
	return strings.TrimSpace(a.Title) != "" || strings.TrimSpace(a.Text) != ""
}

type twitterArticlePayload struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	PreviewText string          `json:"preview_text"`
	CoverMedia  json.RawMessage `json:"cover_media"`
	Content     struct {
		Blocks []twitterArticleBlock `json:"blocks"`
	} `json:"content"`
	MediaEntities []json.RawMessage `json:"media_entities"`
}

// twitterArticleBlock 对应 Draft.js 的块结构，X 的长文正文就是这个格式。
type twitterArticleBlock struct {
	Text string `json:"text"`
	Type string `json:"type"`
}

type twitterArticleMediaPayload struct {
	MediaID   string `json:"media_id"`
	MediaKey  string `json:"media_key"`
	MediaInfo struct {
		TypeName       string `json:"__typename"`
		OriginalImgURL string `json:"original_img_url"`
		DurationMillis int64  `json:"duration_millis"`
		Variants       []struct {
			URL         string `json:"url"`
			Bitrate     int64  `json:"bitrate"`
			ContentType string `json:"content_type"`
		} `json:"variants"`
	} `json:"media_info"`
}

func parseTwitterArticle(data json.RawMessage) twitterArticle {
	if len(data) == 0 || string(data) == "null" {
		return twitterArticle{}
	}
	var payload twitterArticlePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return twitterArticle{}
	}
	article := twitterArticle{
		ID:    strings.TrimSpace(payload.ID),
		Title: compactWhitespace(payload.Title),
		Text: firstNonEmpty(
			twitterArticleBlocksText(payload.Content.Blocks),
			strings.TrimSpace(payload.PreviewText),
		),
	}
	article.Media = parseTwitterArticleMedia(payload.CoverMedia, payload.MediaEntities)
	return article
}

func parseTwitterArticleMedia(cover json.RawMessage, entities []json.RawMessage) []twitterMedia {
	out := make([]twitterMedia, 0, 1+len(entities))
	if media, ok := parseTwitterArticleMediaEntity(cover); ok {
		out = append(out, media)
	}
	for _, entity := range entities {
		media, ok := parseTwitterArticleMediaEntity(entity)
		if !ok {
			continue
		}
		out = append(out, media)
		if len(out) >= maximumTwitterArticleImages {
			break
		}
	}
	return dedupeTwitterMedia(out)
}

func parseTwitterArticleMediaEntity(data json.RawMessage) (twitterMedia, bool) {
	if len(data) == 0 || string(data) == "null" {
		return twitterMedia{}, false
	}
	var payload twitterArticleMediaPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return twitterMedia{}, false
	}
	if imageURL := strings.TrimSpace(payload.MediaInfo.OriginalImgURL); imageURL != "" {
		return twitterMedia{Type: "photo", URL: imageURL}, true
	}
	// 长文里也能插视频；沿用推文那套按码率挑 MP4 的逻辑，别单独发明一套。
	media := twitterMedia{Type: "video", Duration: float64(payload.MediaInfo.DurationMillis) / 1000}
	for _, variant := range payload.MediaInfo.Variants {
		container := ""
		if strings.Contains(strings.ToLower(variant.ContentType), "mp4") {
			container = "mp4"
		}
		media.Formats = append(media.Formats, twitterMediaFormat{URL: variant.URL, Container: container, Bitrate: variant.Bitrate})
	}
	if media.downloadURL() == "" {
		return twitterMedia{}, false
	}
	return media, true
}

// twitterArticleBlocksText 把 Draft.js 块拼回给人读的纯文本。
// 列表和引用补上标记，图片块（atomic）只有占位数据、没有阅读价值，直接跳过，
// 配图走媒体通道单独发送。
func twitterArticleBlocksText(blocks []twitterArticleBlock) string {
	lines := make([]string, 0, len(blocks))
	ordered := 0
	appendBlank := func() {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
	}
	for _, block := range blocks {
		text := strings.TrimRight(block.Text, " \t")
		blockType := strings.ToLower(strings.TrimSpace(block.Type))
		if blockType != "ordered-list-item" {
			ordered = 0
		}
		if strings.TrimSpace(text) == "" {
			if blockType != "atomic" {
				appendBlank()
			}
			continue
		}
		switch blockType {
		case "atomic":
			continue
		case "unordered-list-item":
			text = "· " + strings.TrimSpace(text)
		case "ordered-list-item":
			ordered++
			text = fmt.Sprintf("%d. %s", ordered, strings.TrimSpace(text))
		case "blockquote":
			text = "「" + strings.TrimSpace(text) + "」"
		}
		lines = append(lines, text)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// twitterArticleText 渲染长文的标题与正文；正文超长时截断并说明还有后文。
func twitterArticleText(article twitterArticle) string {
	lines := make([]string, 0, 2)
	if title := strings.TrimSpace(article.Title); title != "" {
		lines = append(lines, "长文标题："+title)
	}
	body := strings.TrimSpace(article.Text)
	if runes := []rune(body); len(runes) > maximumTwitterArticleRunes {
		body = string(runes[:maximumTwitterArticleRunes]) +
			fmt.Sprintf("…\n（正文超过 %d 字，以上是开头部分，后面未取）", maximumTwitterArticleRunes)
	}
	if body != "" {
		lines = append(lines, "长文正文：\n"+body)
	}
	return strings.Join(lines, "\n")
}

// twitterArticleID 从 x.com/i/article/<id> 这类地址里取出长文 ID。
func twitterArticleID(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !hostMatchesDomain(parsed.Hostname(), "x.com", "twitter.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := 0; index+1 < len(parts); index++ {
		if !strings.EqualFold(parts[index], "article") && !strings.EqualFold(parts[index], "articles") {
			continue
		}
		if id := strings.TrimSpace(parts[index+1]); allASCIIDigits(id) {
			return id
		}
	}
	return ""
}

type twitterArticleIndexEntry struct {
	article   twitterArticle
	sourceURL string
	stored    time.Time
}

// x.com/i/article/<id> 对游客直接返回 404，公共镜像接口也只按推文 ID 提供数据，
// 长文本身没有任何免登录入口。但长文几乎总是先由一条推文带出来，
// 解析推文时把正文记下来，随后收到长文直链就能直接命中。
var twitterArticleIndex struct {
	mu      sync.Mutex
	entries map[string]twitterArticleIndexEntry
}

func rememberTwitterArticle(article twitterArticle, sourceURL string) {
	id := strings.TrimSpace(article.ID)
	if id == "" || !article.hasContent() || len(article.Text) > twitterArticleIndexMaxBytes {
		return
	}
	twitterArticleIndex.mu.Lock()
	defer twitterArticleIndex.mu.Unlock()
	if twitterArticleIndex.entries == nil {
		twitterArticleIndex.entries = map[string]twitterArticleIndexEntry{}
	}
	now := time.Now()
	for key, entry := range twitterArticleIndex.entries {
		if now.Sub(entry.stored) > twitterArticleIndexTTL {
			delete(twitterArticleIndex.entries, key)
		}
	}
	if _, exists := twitterArticleIndex.entries[id]; !exists && len(twitterArticleIndex.entries) >= twitterArticleIndexEntries {
		oldestKey, oldest := "", now
		for key, entry := range twitterArticleIndex.entries {
			if oldestKey == "" || entry.stored.Before(oldest) {
				oldestKey, oldest = key, entry.stored
			}
		}
		if oldestKey != "" {
			delete(twitterArticleIndex.entries, oldestKey)
		}
	}
	twitterArticleIndex.entries[id] = twitterArticleIndexEntry{article: article, sourceURL: strings.TrimSpace(sourceURL), stored: now}
}

func rememberedTwitterArticle(id string) (twitterArticle, string, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return twitterArticle{}, "", false
	}
	twitterArticleIndex.mu.Lock()
	defer twitterArticleIndex.mu.Unlock()
	entry, ok := twitterArticleIndex.entries[id]
	if !ok {
		return twitterArticle{}, "", false
	}
	if time.Since(entry.stored) > twitterArticleIndexTTL {
		delete(twitterArticleIndex.entries, id)
		return twitterArticle{}, "", false
	}
	return entry.article, entry.sourceURL, true
}

func resetTwitterArticleIndex() {
	twitterArticleIndex.mu.Lock()
	defer twitterArticleIndex.mu.Unlock()
	twitterArticleIndex.entries = nil
}

// twitterArticleAPIURL 生成自建长文接口地址，规则与 twitterMetadataAPIURL 一致。
func twitterArticleAPIURL(id string) string {
	if !allASCIIDigits(id) {
		return ""
	}
	template := strings.TrimSpace(os.Getenv(defaultTwitterArticleAPIEnv))
	if template == "" {
		return ""
	}
	switch {
	case strings.Contains(template, "{id}"):
		template = strings.ReplaceAll(template, "{id}", url.PathEscape(id))
	case strings.Contains(template, "{url}"):
		template = strings.ReplaceAll(template, "{url}", url.QueryEscape("https://x.com/i/article/"+id))
	default:
		parsed, err := url.Parse(template)
		if err != nil {
			return ""
		}
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + url.PathEscape(id)
		template = parsed.String()
	}
	parsed, err := url.Parse(template)
	if err != nil || parsed.Scheme != "https" {
		return ""
	}
	return parsed.String()
}

// parseTwitterArticleResponse 兼容几种常见的长文接口外层：{"article":…}、
// {"tweet":{"article":…}}、以及直接返回长文对象本身。
func parseTwitterArticleResponse(data []byte) (twitterArticle, bool) {
	if post, ok := parseTwitterPostResponse(data); ok && post.Article.hasContent() {
		return post.Article, true
	}
	var envelope struct {
		Article json.RawMessage `json:"article"`
	}
	if json.Unmarshal(data, &envelope) == nil {
		if article := parseTwitterArticle(envelope.Article); article.hasContent() {
			return article, true
		}
	}
	if article := parseTwitterArticle(data); article.hasContent() {
		return article, true
	}
	return twitterArticle{}, false
}

// fetchTwitterArticleFromAPI 走自建接口取长文；没配置或取不到都返回 false。
func fetchTwitterArticleFromAPI(ctx context.Context, id string) (twitterArticle, bool) {
	apiURL := twitterArticleAPIURL(id)
	if apiURL == "" {
		return twitterArticle{}, false
	}
	body, ok := fetchResolverBody(ctx, apiURL, twitterResolverHeaders())
	if !ok {
		return twitterArticle{}, false
	}
	return parseTwitterArticleResponse([]byte(body))
}

// lookupTwitterArticle 按「自建接口 → 已解析过的长文 → 同一条消息里的推文」逐层降级。
// 三条都不成立时说明这篇长文没有免登录入口，交给调用方给出可操作的提示。
func (p *ResolverPlugin) lookupTwitterArticle(ctx context.Context, id string, siblingTweets []string) (twitterArticle, string, bool) {
	if article, ok := fetchTwitterArticleFromAPI(ctx, id); ok {
		if strings.TrimSpace(article.ID) == "" {
			article.ID = id
		}
		rememberTwitterArticle(article, "")
		return article, "", true
	}
	if article, sourceURL, ok := rememberedTwitterArticle(id); ok {
		return article, sourceURL, true
	}
	fetchPost := p.twitterPostFetcher
	if fetchPost == nil {
		fetchPost = fetchTwitterPost
	}
	for _, candidate := range siblingTweets {
		post, ok := fetchPost(ctx, candidate)
		if !ok || !post.Article.hasContent() {
			continue
		}
		rememberTwitterArticle(post.Article, candidate)
		if strings.TrimSpace(post.Article.ID) == "" || post.Article.ID == id {
			return post.Article, candidate, true
		}
	}
	return twitterArticle{}, "", false
}

// twitterStatusURLsInRequest 挑出同一条消息里的推文链接。
// 用户常常是「推文链接 + 长文链接」一起发，或者先发推文再让机器人把正文发出来，
// 这时长文正文就藏在那条推文的数据里。
func twitterStatusURLsInRequest(req PluginRequest) []string {
	urls := extractResolverRequestURLs(req)
	out := make([]string, 0, len(urls))
	for _, raw := range urls {
		if twitterStatusID(raw) != "" {
			out = append(out, raw)
		}
	}
	return out
}

// resolveTwitterArticle 处理 x.com/i/article/<id> 这种长文直链。
func (p *ResolverPlugin) resolveTwitterArticle(ctx context.Context, req PluginRequest, raw, id string) resolverSocialResult {
	siblingTweets := twitterStatusURLsInRequest(req)
	article, sourceURL, ok := p.lookupTwitterArticle(ctx, id, siblingTweets)
	if !ok {
		recordResolverMediaLog(ctx, req, raw, "x", false, "article_login_required")
		return resolverSocialResult{
			Handled: true,
			Failed:  true,
			Context: "[X / Twitter 长文] 这篇站内长文对未登录访客直接返回 404，没法直接抓正文。把发布这篇长文的那条推文链接发出来，我就能把正文读出来。",
		}
	}
	// 同一条消息里已经带上了那条推文，推文那一路会展开同一篇长文，
	// 这里再发一遍就是同样的正文连着出现两次。
	if sourceURL != "" && slices.Contains(siblingTweets, sourceURL) {
		return resolverSocialResult{Suppressed: true}
	}
	metaText := strings.TrimSpace(fmt.Sprintf("%s识别：小蓝鸟学习版长文", resolverNickname()))
	if text := twitterArticleText(article); text != "" {
		metaText += "\n" + text
	}
	if sourceURL != "" {
		metaText += "\n原推文：" + sourceURL
	}
	nodes := []OutgoingMessage{{Text: metaText}}
	imageURLs, videoURLs, nodes := p.deliverTwitterMedia(ctx, req, raw, article.Media, nodes)
	recordResolverMediaLog(ctx, req, raw, "x", true, "article")
	return resolverSocialResult{
		Handled:         true,
		Context:         metaText,
		ImageURLs:       imageURLs,
		VideoURLs:       videoURLs,
		ForwardMessages: nodes,
		ResourceKeys:    []string{"x:article:" + id},
	}
}

// twitterPostArticleText 给带长文的推文补上长文正文，并把这篇长文记入索引，
// 方便随后单独发来的长文直链直接命中。
func twitterPostArticleText(post twitterPost, sourceURL string) string {
	if !post.Article.hasContent() {
		return ""
	}
	rememberTwitterArticle(post.Article, sourceURL)
	return twitterArticleText(post.Article)
}
