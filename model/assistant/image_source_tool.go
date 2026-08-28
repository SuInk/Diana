// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const dianaImageSourceToolName = "diana.image_source"

// imageSourceMaxCandidates 限制一次能挑的图片数。群里连发九张图时把每一张都列进
// 工具描述没有意义，模型也挑不清。
const imageSourceMaxCandidates = 9

type dianaImageSourceTool struct {
	runtime  *Runtime
	event    MessageEvent
	plugin   *ImageSourcePlugin
	settings SettingValues
}

func newDianaImageSourceTool(runtime *Runtime, event MessageEvent, plugin *ImageSourcePlugin, settings SettingValues) *dianaImageSourceTool {
	return &dianaImageSourceTool{runtime: runtime, event: event, plugin: plugin, settings: settings}
}

func (t *dianaImageSourceTool) Name() string { return dianaImageSourceToolName }

func (t *dianaImageSourceTool) Description() string {
	return `查找聊天里某张图片的出处（反向图搜）。用于「这张图哪来的」「出自哪部番」「原图在哪」这类问题。` +
		`只能搜当前消息、被引用消息或语义指代到的图片，传 image_index 选第几张，省略表示第一张。` +
		`结果按相似度给出候选，相似度不高时要照实说「可能是」，不要当成确认。`
}

func (t *dianaImageSourceTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"image_index": toolIntParam("要搜第几张图，从 1 开始；省略表示第一张。", 1, imageSourceMaxCandidates),
	})
}

type imageSourceMatch struct {
	Provider   string   `json:"provider"`
	Similarity float64  `json:"similarity"`
	Title      string   `json:"title,omitempty"`
	Author     string   `json:"author,omitempty"`
	Source     string   `json:"source,omitempty"`
	Episode    string   `json:"episode,omitempty"`
	Timestamp  string   `json:"timestamp,omitempty"`
	URLs       []string `json:"urls,omitempty"`
}

type imageSourceResult struct {
	OK             bool               `json:"ok"`
	ImageIndex     int                `json:"image_index,omitempty"`
	ImageCount     int                `json:"image_count,omitempty"`
	Matches        []imageSourceMatch `json:"matches,omitempty"`
	MinSimilarity  float64            `json:"min_similarity,omitempty"`
	FilteredOut    int                `json:"filtered_out,omitempty"`
	ProviderErrors []string           `json:"provider_errors,omitempty"`
	Message        string             `json:"message"`
	FailureCode    string             `json:"failure_code,omitempty"`
}

func (t *dianaImageSourceTool) fail(code, message string) (string, error) {
	return marshalImageSourceResult(imageSourceResult{FailureCode: code, Message: message})
}

func marshalImageSourceResult(result imageSourceResult) (string, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *dianaImageSourceTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil || t.plugin == nil {
		return "", fmt.Errorf("diana image source: runtime is not configured")
	}
	candidates := t.candidateImages(ctx)
	if len(candidates) == 0 {
		return t.fail("no_image", "当前消息、被引用消息和语义指代里都没有可用的图片。请对方把要查的图片直接发出来或引用那条消息。")
	}
	index := imageSourceIndex(input, len(candidates))
	if index < 0 {
		return t.fail("invalid_input", fmt.Sprintf("image_index 超出范围：这里只有 %d 张图片。", len(candidates)))
	}

	maxBytes := int64(t.settings.Int(imageSourceSettingMaxUploadMegabytes, 8)) * 1024 * 1024
	payload, contentType, err := imageSourcePayload(ctx, candidates[index], maxBytes)
	if err != nil {
		return t.fail("image_unreadable", "这张图片读不出来："+err.Error())
	}

	matches, providerErrors := t.search(ctx, payload, contentType)
	minSimilarity := float64(t.settings.Int(imageSourceSettingMinSimilarity, 60))
	kept := make([]imageSourceMatch, 0, len(matches))
	for _, match := range matches {
		if match.Similarity >= minSimilarity {
			kept = append(kept, match)
		}
	}
	sort.SliceStable(kept, func(a, b int) bool { return kept[a].Similarity > kept[b].Similarity })

	result := imageSourceResult{
		OK:             true,
		ImageIndex:     index + 1,
		ImageCount:     len(candidates),
		Matches:        kept,
		MinSimilarity:  minSimilarity,
		FilteredOut:    len(matches) - len(kept),
		ProviderErrors: providerErrors,
	}
	switch {
	case len(kept) > 0:
		result.Message = "找到候选出处，按相似度从高到低排列。相似度在 90% 以上可以当作认出来了；60~90% 只能说「看着像」，要把不确定说出来。不要凭画风或印象补充候选里没有的作品名。"
	case len(matches) > 0:
		result.Message = fmt.Sprintf("有 %d 条结果但相似度都低于 %.0f%%，按噪声丢掉了。照实说没找到可靠出处，不要把低分结果说成来源。", len(matches), minSimilarity)
	case len(providerErrors) > 0:
		result.OK = false
		result.FailureCode = "provider_failed"
		result.Message = "所有来源都没能查成：" + strings.Join(providerErrors, "；") + "。把原因告诉用户，别只说没找到。"
	default:
		result.Message = "两个来源都没有匹配。照实说没查到出处；不要凭画风猜作者或作品。"
	}
	return marshalImageSourceResult(result)
}

// candidateImages 收集能查的图片：当前消息、被引用消息、语义指代选中的历史消息。
//
// 刻意不接受模型自己写的 URL。这个工具会把图片传给第三方服务，能指定任意地址就等于
// 多了一个对外发请求的入口；而「查这张图」这件事本来就只发生在聊天里已有的图片上。
func (t *dianaImageSourceTool) candidateImages(ctx context.Context) []string {
	images := availableImageURLs(t.event.Segments)
	if t.event.Quoted != nil {
		images = appendUniqueStrings(images, availableImageURLs(t.event.Quoted.Segments)...)
	}
	images = appendUniqueStrings(images, t.runtime.semanticReferenceImageURLs(ctx, t.event)...)
	if len(images) > imageSourceMaxCandidates {
		images = images[:imageSourceMaxCandidates]
	}
	return images
}

func imageSourceIndex(input map[string]any, count int) int {
	raw := strings.TrimSpace(configToolString(input, "image_index"))
	if raw == "" {
		return 0
	}
	index, err := strconv.Atoi(raw)
	if err != nil || index < 1 || index > count {
		return -1
	}
	return index - 1
}

// imageSourcePayload 把图片取成字节。三种来源都要认：QQ 的 CDN 链接、本地缓存路径、
// 以及历史里存成 data URL 的那些。
//
// 统一上传字节而不是把链接转给对方服务：QQ 的图片链接带时效、有时还要请求头，第三方
// 拿到多半是 403，而那种失败会表现成「查不到出处」，看不出是链接过期。
func imageSourcePayload(ctx context.Context, imageURL string, maxBytes int64) ([]byte, string, error) {
	imageURL = strings.TrimSpace(imageURL)
	switch {
	case strings.HasPrefix(imageURL, "data:"):
		return decodeImageDataURL(imageURL, maxBytes)
	case strings.HasPrefix(imageURL, "http://"), strings.HasPrefix(imageURL, "https://"):
		body, contentType, err := downloadImageBytesWithLimit(ctx, imageURL, maxBytes)
		if err != nil {
			return nil, "", err
		}
		return body, imageSourceContentType(contentType), nil
	default:
		dataURL, err := localImageAsDataURL(imageURL)
		if err != nil {
			return nil, "", err
		}
		return decodeImageDataURL(dataURL, maxBytes)
	}
}

func decodeImageDataURL(dataURL string, maxBytes int64) ([]byte, string, error) {
	comma := strings.Index(dataURL, ",")
	if comma < 0 {
		return nil, "", errors.New("data URL 格式不对")
	}
	header := dataURL[:comma]
	if !strings.Contains(header, ";base64") {
		return nil, "", errors.New("只支持 base64 编码的 data URL")
	}
	body, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil {
		return nil, "", err
	}
	if maxBytes > 0 && int64(len(body)) > maxBytes {
		return nil, "", fmt.Errorf("图片超过 %d MB 的上传上限", maxBytes/(1024*1024))
	}
	contentType := strings.TrimPrefix(header, "data:")
	contentType = strings.TrimSuffix(contentType, ";base64")
	return body, imageSourceContentType(contentType), nil
}

func imageSourceContentType(contentType string) string {
	contentType = strings.TrimSpace(strings.ToLower(contentType))
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = strings.TrimSpace(contentType[:idx])
	}
	if !strings.HasPrefix(contentType, "image/") {
		return "image/jpeg"
	}
	return contentType
}

// search 并发跑两个来源。一个挂掉不影响另一个：番剧截图和插画本来就分别只有一边
// 认得，串行跑还会让失败的那个把另一个的时间也吃掉。
func (t *dianaImageSourceTool) search(ctx context.Context, payload []byte, contentType string) ([]imageSourceMatch, []string) {
	timeout := time.Duration(t.settings.Int(imageSourceSettingTimeoutSeconds, 20)) * time.Second
	limit := t.settings.Int(imageSourceSettingMaxResults, 3)

	type providerRun struct {
		name string
		run  func(context.Context) ([]imageSourceMatch, error)
	}
	runs := make([]providerRun, 0, 2)
	if t.settings.Bool(imageSourceSettingSauceNAOEnabled, true) {
		runs = append(runs, providerRun{name: "SauceNAO", run: func(ctx context.Context) ([]imageSourceMatch, error) {
			return t.searchSauceNAO(ctx, payload, contentType, limit)
		}})
	}
	if t.settings.Bool(imageSourceSettingTraceMoeEnabled, true) {
		runs = append(runs, providerRun{name: "trace.moe", run: func(ctx context.Context) ([]imageSourceMatch, error) {
			return t.searchTraceMoe(ctx, payload, contentType, limit)
		}})
	}
	if len(runs) == 0 {
		return nil, []string{"两个溯源来源都被停用了"}
	}

	var mu sync.Mutex
	var matches []imageSourceMatch
	var failures []string
	var workers sync.WaitGroup
	for _, item := range runs {
		workers.Add(1)
		go func(item providerRun) {
			defer workers.Done()
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			found, err := item.run(runCtx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, item.name+" "+err.Error())
				return
			}
			matches = append(matches, found...)
		}(item)
	}
	workers.Wait()
	sort.Strings(failures)
	return matches, failures
}

func (t *dianaImageSourceTool) searchSauceNAO(ctx context.Context, payload []byte, contentType string, limit int) ([]imageSourceMatch, error) {
	apiKey := strings.TrimSpace(t.settings.String(imageSourceSettingSauceNAOAPIKey, ""))
	if apiKey == "" {
		return nil, errors.New("没有配置 API Key，插件设置里填上才能用")
	}
	endpoint, err := imageSourceEndpoint(t.settings.String(imageSourceSettingSauceNAOURL, defaultSauceNAOURL))
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("output_type", "2")
	query.Set("db", "999")
	query.Set("numres", strconv.Itoa(limit))
	query.Set("api_key", apiKey)
	endpoint.RawQuery = query.Encode()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "image"+imageSourceExtension(contentType))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(payload); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	raw, err := t.do(request)
	if err != nil {
		return nil, err
	}

	var decoded struct {
		Header struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"header"`
		Results []struct {
			Header struct {
				Similarity string `json:"similarity"`
				IndexName  string `json:"index_name"`
			} `json:"header"`
			Data map[string]any `json:"data"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, errors.New("返回内容看不懂，可能是限流页面")
	}
	// 负数是请求侧的问题（额度用尽、密钥不对），正数是服务端的问题。原文带出去，
	// 「今天的 100 次用完了」和「密钥填错了」得分得开。
	if decoded.Header.Status != 0 && strings.TrimSpace(decoded.Header.Message) != "" {
		return nil, errors.New(strings.TrimSpace(decoded.Header.Message))
	}

	out := make([]imageSourceMatch, 0, len(decoded.Results))
	for _, item := range decoded.Results {
		similarity, _ := strconv.ParseFloat(strings.TrimSpace(item.Header.Similarity), 64)
		match := imageSourceMatch{
			Provider:   "saucenao",
			Similarity: similarity,
			Source:     strings.TrimSpace(item.Header.IndexName),
			Title:      imageSourceFirstString(item.Data, "title", "jp_name", "eng_name", "source", "material"),
			Author:     imageSourceFirstString(item.Data, "member_name", "creator", "author_name", "artist", "company"),
			URLs:       imageSourceExternalURLs(item.Data),
		}
		out = append(out, match)
	}
	return out, nil
}

func (t *dianaImageSourceTool) searchTraceMoe(ctx context.Context, payload []byte, contentType string, limit int) ([]imageSourceMatch, error) {
	endpoint, err := imageSourceEndpoint(t.settings.String(imageSourceSettingTraceMoeURL, defaultTraceMoeURL))
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("anilistInfo", "")
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", contentType)
	raw, err := t.do(request)
	if err != nil {
		return nil, err
	}

	var decoded struct {
		Error  string `json:"error"`
		Result []struct {
			Similarity float64 `json:"similarity"`
			Episode    any     `json:"episode"`
			From       float64 `json:"from"`
			Filename   string  `json:"filename"`
			Anilist    struct {
				ID    int `json:"id"`
				Title struct {
					Romaji  string `json:"romaji"`
					English string `json:"english"`
					Native  string `json:"native"`
				} `json:"title"`
			} `json:"anilist"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, errors.New("返回内容看不懂")
	}
	if message := strings.TrimSpace(decoded.Error); message != "" {
		return nil, errors.New(message)
	}

	out := make([]imageSourceMatch, 0, limit)
	for _, item := range decoded.Result {
		if len(out) >= limit {
			break
		}
		title := firstNonEmpty(
			strings.TrimSpace(item.Anilist.Title.Native),
			strings.TrimSpace(item.Anilist.Title.Romaji),
			strings.TrimSpace(item.Anilist.Title.English),
			strings.TrimSpace(item.Filename),
		)
		match := imageSourceMatch{
			Provider: "tracemoe",
			// trace.moe 的相似度是 0~1，其余来源是百分制，这里统一成百分制，
			// 免得下游拿两套刻度的数字比大小。
			Similarity: item.Similarity * 100,
			Source:     "番剧截图",
			Title:      title,
			Episode:    imageSourceEpisode(item.Episode),
			Timestamp:  imageSourceTimestamp(item.From),
		}
		if item.Anilist.ID > 0 {
			match.URLs = []string{fmt.Sprintf("https://anilist.co/anime/%d", item.Anilist.ID)}
		}
		out = append(out, match)
	}
	return out, nil
}

func (t *dianaImageSourceTool) do(request *http.Request) ([]byte, error) {
	response, err := t.plugin.httpClient().Do(request)
	if err != nil {
		return nil, errors.New("请求失败：" + err.Error())
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, errors.New("被限流了（HTTP 429），稍后再试")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return body, nil
}

// imageSourceEndpoint 和联网搜索用同一条规矩：只放 HTTPS，本机 HTTP 留给调试。
func imageSourceEndpoint(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return nil, errors.New("接口地址无效")
	}
	if parsed.User != nil {
		return nil, errors.New("接口地址里不能带账号密码")
	}
	if parsed.Scheme == "https" {
		return parsed, nil
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1") {
		return parsed, nil
	}
	return nil, errors.New("接口地址必须是 HTTPS")
}

func imageSourceExtension(contentType string) string {
	switch imageSourceContentType(contentType) {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".jpg"
	}
}

func imageSourceFirstString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := data[key].(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		case []any:
			names := make([]string, 0, len(value))
			for _, item := range value {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					names = append(names, strings.TrimSpace(text))
				}
			}
			if len(names) > 0 {
				return strings.Join(names, "、")
			}
		}
	}
	return ""
}

func imageSourceExternalURLs(data map[string]any) []string {
	raw, ok := data["ext_urls"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}

func imageSourceEpisode(value any) string {
	switch episode := value.(type) {
	case string:
		return strings.TrimSpace(episode)
	case float64:
		return strconv.FormatFloat(episode, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(episode))
		for _, item := range episode {
			if text := imageSourceEpisode(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "-")
	}
	return ""
}

// imageSourceTimestamp 把秒数写成 mm:ss。「出现在第 83.4 秒」没人这么读。
func imageSourceTimestamp(seconds float64) string {
	if seconds <= 0 {
		return ""
	}
	total := int(seconds)
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}
