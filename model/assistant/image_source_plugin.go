// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
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
	"time"
)

// 图片溯源插件：群里丢来一张图问「这谁画的」「出处呢」，靠看图是答不出来的——
// 视觉模型认得出画风，认不出是哪个 pixiv id。这件事只能反查图库。
//
// 内置两条线：SauceNAO 覆盖插画、同人志、表情包，需要自备 API Key；trace.moe
// 专门认番剧截图，不用 key。两条都是「上传图片、返回可能的出处」，互补而不重复。
const (
	imageSourcePluginID = "official.image-source"

	imageSourceSettingSauceNAOKey     = "saucenao_api_key"
	imageSourceSettingSauceNAOEnabled = "saucenao_enabled"
	imageSourceSettingTraceMoeEnabled = "tracemoe_enabled"
	imageSourceSettingMinSimilarity   = "min_similarity"
	imageSourceSettingMaxResults      = "max_results"
	imageSourceSettingTimeout         = "timeout_seconds"
	imageSourceSettingPrivateEnabled  = "private_enabled"
	imageSourceSettingSauceNAOURL     = "saucenao_url"
	imageSourceSettingTraceMoeURL     = "tracemoe_url"
	imageSourceSettingMaxUploadMB     = "max_upload_mb"

	imageSourceProviderSauceNAO = "saucenao"
	imageSourceProviderTraceMoe = "trace.moe"

	defaultSauceNAOEndpoint = "https://saucenao.com/search.php"
	defaultTraceMoeEndpoint = "https://api.trace.moe/search"

	// 反查是给人看的线索，不是数据集：一次给三五条就够判断，多了只会让模型
	// 把整张表念出来。
	imageSourceMaxResponseBytes = 1 << 20
	// 结果按图片内容哈希落库；出处这种东西基本不变，但也不该永远不复查。
	imageSourceCacheTTL = 30 * 24 * time.Hour
)

// ImageSourceMatch 是一条反查结果。
type ImageSourceMatch struct {
	Provider   string   `json:"provider"`
	Similarity float64  `json:"similarity"`
	Title      string   `json:"title,omitempty"`
	Author     string   `json:"author,omitempty"`
	Source     string   `json:"source,omitempty"`
	URLs       []string `json:"urls,omitempty"`
	// Detail 放各家特有的补充信息，比如番剧的集数和时间点。
	Detail string `json:"detail,omitempty"`
}

type ImageSourcePlugin struct {
	client *http.Client
	// 两个接口地址是测试接缝，生产代码不改它们。
	saucenaoEndpoint string
	traceMoeEndpoint string
}

func NewImageSourcePlugin(client *http.Client) *ImageSourcePlugin {
	if client == nil {
		client = &http.Client{}
	}
	return &ImageSourcePlugin{
		client:           client,
		saucenaoEndpoint: defaultSauceNAOEndpoint,
		traceMoeEndpoint: defaultTraceMoeEndpoint,
	}
}

func (p *ImageSourcePlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID: imageSourcePluginID, Name: "图片溯源", Version: "0.1.0",
		Description: "以图搜图，回答「这张图哪来的」：把聊天里的图片反查图库，给出可能的原作、作者和链接。SauceNAO 覆盖 pixiv、Danbooru、同人志等，需要在 saucenao.com 免费注册后填 API Key；trace.moe 专门识别番剧截图，返回番名、集数和时间点，不需要 Key。两条线可以各自开关，结果按相似度过滤后交给模型转述。注意反查要把图片上传到对应的第三方图库；不希望这件事发生就关掉整个插件，或只留需要的那一条。",
		Official:    true, BuiltIn: true,
		Permissions: []string{"message:read", "network:https", "agent:tool"},
		Settings: []PluginSettingSpec{
			{Key: imageSourceSettingSauceNAOEnabled, Label: "启用 SauceNAO（插画、同人志、表情包）", Type: PluginSettingTypeBool, Default: true},
			{Key: imageSourceSettingSauceNAOKey, Label: "SauceNAO API Key", Type: PluginSettingTypeString, Default: "", Secret: true},
			{Key: imageSourceSettingTraceMoeEnabled, Label: "启用 trace.moe（番剧截图，无需 Key）", Type: PluginSettingTypeBool, Default: true},
			{Key: imageSourceSettingMinSimilarity, Label: "最低相似度", Type: PluginSettingTypeNumber, Default: 60, Min: settingRange(30), Max: settingRange(95), Step: 5, Unit: "%"},
			{Key: imageSourceSettingMaxResults, Label: "返回结果条数上限", Type: PluginSettingTypeNumber, Default: 3, Min: settingRange(1), Max: settingRange(8), Step: 1},
			{Key: imageSourceSettingTimeout, Label: "单次反查超时", Type: PluginSettingTypeNumber, Default: 20, Min: settingRange(5), Max: settingRange(60), Step: 5, Unit: "秒"},
			{Key: imageSourceSettingPrivateEnabled, Label: "私聊也允许反查", Type: PluginSettingTypeBool, Default: true},
			{Key: imageSourceSettingMaxUploadMB, Label: "上传大小上限", Description: "超过这个大小的图片不上传检索。SauceNAO 自身的上限是 15 MB。", Type: PluginSettingTypeNumber, Default: 8, Min: settingRange(1), Max: settingRange(15), Step: 1, Unit: "MB"},
			{Key: imageSourceSettingSauceNAOURL, Label: "SauceNAO 接口地址", Description: "留空用官方地址。只接受 HTTPS，本机调试可用 localhost 的 HTTP。", Type: PluginSettingTypeString, Default: ""},
			{Key: imageSourceSettingTraceMoeURL, Label: "trace.moe 接口地址", Description: "留空用官方地址。只接受 HTTPS，本机调试可用 localhost 的 HTTP。", Type: PluginSettingTypeString, Default: ""},
		},
	}
}

func (p *ImageSourcePlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

type imageSourceConfig struct {
	SauceNAOEnabled bool
	SauceNAOKey     string
	TraceMoeEnabled bool
	MinSimilarity   float64
	MaxResults      int
	Timeout         time.Duration
	PrivateEnabled  bool
	// 自建网关或反代时改这两个地址；留空走官方。
	SauceNAOURL string
	TraceMoeURL string
	MaxUploadMB int
}

func imageSourceConfigFromSettings(v SettingValues) imageSourceConfig {
	return imageSourceConfig{
		SauceNAOEnabled: v.Bool(imageSourceSettingSauceNAOEnabled, true),
		SauceNAOKey:     strings.TrimSpace(v.String(imageSourceSettingSauceNAOKey, "")),
		TraceMoeEnabled: v.Bool(imageSourceSettingTraceMoeEnabled, true),
		MinSimilarity:   float64(v.Int(imageSourceSettingMinSimilarity, 60)),
		MaxResults:      v.Int(imageSourceSettingMaxResults, 3),
		Timeout:         time.Duration(v.Int(imageSourceSettingTimeout, 20)) * time.Second,
		PrivateEnabled:  v.Bool(imageSourceSettingPrivateEnabled, true),
		SauceNAOURL:     strings.TrimSpace(v.String(imageSourceSettingSauceNAOURL, "")),
		TraceMoeURL:     strings.TrimSpace(v.String(imageSourceSettingTraceMoeURL, "")),
		MaxUploadMB:     v.Int(imageSourceSettingMaxUploadMB, 8),
	}
}

// maxUploadBytes 是「愿意往第三方图库传多大的图」的上限。图片是要出网的，
// 传之前就该按体积拦一道：超限的图 SauceNAO 那边也只会回一个 413，白跑一趟
// 还消耗当天的免费额度。
func (cfg imageSourceConfig) maxUploadBytes() int64 {
	mb := cfg.MaxUploadMB
	if mb <= 0 {
		mb = 8
	}
	return int64(mb) * 1024 * 1024
}

// imageSourceEndpoint 和联网搜索用同一条规矩：只放 HTTPS，本机 HTTP 留给调试。
// 配歪了就退回官方地址，而不是拿一个明文地址把图片发出去。
func imageSourceEndpoint(raw, fallback string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return fallback
	}
	if parsed.Scheme == "https" {
		return raw
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1") {
		return raw
	}
	return fallback
}

// saucenaoUsable 表示 SauceNAO 这条线真的能用。开了但没填 Key 等于没开——
// 这种情况要说出来，不然用户只会看到「没查到」。
func (cfg imageSourceConfig) saucenaoUsable() bool {
	return cfg.SauceNAOEnabled && cfg.SauceNAOKey != ""
}

func (cfg imageSourceConfig) anyProviderUsable() bool {
	return cfg.saucenaoUsable() || cfg.TraceMoeEnabled
}

func (cfg imageSourceConfig) timeout() time.Duration {
	if cfg.Timeout <= 0 {
		return 20 * time.Second
	}
	return cfg.Timeout
}

// search 并发问每条线路，合并结果。一条线挂了不影响另一条：番剧截图本来
// 就只有 trace.moe 认得，插画反过来只有 SauceNAO 认得。
func (p *ImageSourcePlugin) search(ctx context.Context, cfg imageSourceConfig, image []byte) ([]ImageSourceMatch, []string) {
	type outcome struct {
		matches []ImageSourceMatch
		err     error
		name    string
	}
	var providers []func(context.Context) outcome
	if cfg.saucenaoUsable() {
		providers = append(providers, func(ctx context.Context) outcome {
			matches, err := p.searchSauceNAO(ctx, cfg, image)
			return outcome{matches: matches, err: err, name: imageSourceProviderSauceNAO}
		})
	}
	if cfg.TraceMoeEnabled {
		providers = append(providers, func(ctx context.Context) outcome {
			matches, err := p.searchTraceMoe(ctx, cfg, image)
			return outcome{matches: matches, err: err, name: imageSourceProviderTraceMoe}
		})
	}

	results := make(chan outcome, len(providers))
	for _, provider := range providers {
		go func(run func(context.Context) outcome) {
			results <- run(ctx)
		}(provider)
	}
	var matches []ImageSourceMatch
	var notes []string
	for range providers {
		got := <-results
		if got.err != nil {
			notes = append(notes, fmt.Sprintf("%s 查询失败：%s", got.name, got.err.Error()))
			continue
		}
		matches = append(matches, got.matches...)
	}
	matches = filterImageSourceMatches(matches, cfg)
	return matches, notes
}

// filterImageSourceMatches 按相似度过滤、排序、截断。
func filterImageSourceMatches(matches []ImageSourceMatch, cfg imageSourceConfig) []ImageSourceMatch {
	kept := make([]ImageSourceMatch, 0, len(matches))
	for _, match := range matches {
		if match.Similarity+0.001 < cfg.MinSimilarity {
			continue
		}
		kept = append(kept, match)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Similarity > kept[j].Similarity })
	limit := cfg.MaxResults
	if limit <= 0 {
		limit = 3
	}
	if len(kept) > limit {
		kept = kept[:limit]
	}
	return kept
}

// searchSauceNAO 走 SauceNAO 的 JSON 接口。图片直接上传，不传 URL：聊天平台
// 的图床地址常常带鉴权、会过期，或者干脆挡住境外访问。
func (p *ImageSourcePlugin) searchSauceNAO(ctx context.Context, cfg imageSourceConfig, image []byte) ([]ImageSourceMatch, error) {
	endpoint := firstNonEmpty(p.saucenaoEndpoint, defaultSauceNAOEndpoint)
	return p.searchSauceNAOAt(ctx, imageSourceEndpoint(cfg.SauceNAOURL, endpoint), cfg, image)
}

func (p *ImageSourcePlugin) searchSauceNAOAt(ctx context.Context, endpoint string, cfg imageSourceConfig, image []byte) ([]ImageSourceMatch, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	fields := map[string]string{
		"api_key":     cfg.SauceNAOKey,
		"output_type": "2",
		"db":          "999",
		"numres":      strconv.Itoa(maxInt(cfg.MaxResults, 1) * 2),
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, err
		}
	}
	part, err := writer.CreateFormFile("file", "image.png")
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(image); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	payload, err := p.readJSONResponse(req)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Header struct {
			Status         int    `json:"status"`
			Message        string `json:"message"`
			LongRemaining  *int   `json:"long_remaining"`
			ShortRemaining *int   `json:"short_remaining"`
		} `json:"header"`
		Results []struct {
			Header struct {
				Similarity string `json:"similarity"`
				IndexName  string `json:"index_name"`
			} `json:"header"`
			Data map[string]any `json:"data"`
		} `json:"results"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("返回内容不是预期的 JSON：%w", err)
	}
	switch {
	case parsed.Header.Status < 0:
		// 负数是请求本身的问题：Key 不对、图太大、格式不认。
		return nil, errors.New(firstNonEmpty(strings.TrimSpace(parsed.Header.Message), "请求被拒绝，请检查 API Key 与图片格式"))
	case parsed.Header.LongRemaining != nil && *parsed.Header.LongRemaining <= 0:
		return nil, errors.New("今日额度已用完")
	case parsed.Header.ShortRemaining != nil && *parsed.Header.ShortRemaining < 0:
		return nil, errors.New("短时间内请求过多，稍后再试")
	}

	matches := make([]ImageSourceMatch, 0, len(parsed.Results))
	for _, result := range parsed.Results {
		similarity, err := strconv.ParseFloat(strings.TrimSpace(result.Header.Similarity), 64)
		if err != nil {
			continue
		}
		match := ImageSourceMatch{
			Provider:   imageSourceProviderSauceNAO,
			Similarity: similarity,
			Source:     strings.TrimSpace(result.Header.IndexName),
			Title:      saucenaoDataString(result.Data, "title", "jp_name", "eng_name", "material", "source"),
			Author:     saucenaoDataString(result.Data, "member_name", "creator", "author_name", "artist", "company"),
			URLs:       saucenaoExternalURLs(result.Data),
		}
		if match.Title == "" && len(match.URLs) == 0 {
			// 既没名字也没链接的条目对用户毫无意义。
			continue
		}
		matches = append(matches, match)
	}
	return matches, nil
}

// saucenaoDataString 按优先级取第一个非空字段。各个图库的 data 结构不一样，
// 谁有名字就用谁的。
func saucenaoDataString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := data[key].(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		case []any:
			for _, item := range value {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return ""
}

func saucenaoExternalURLs(data map[string]any) []string {
	raw, ok := data["ext_urls"].([]any)
	if !ok {
		return nil
	}
	urls := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			continue
		}
		if text = strings.TrimSpace(text); text != "" {
			urls = append(urls, text)
		}
	}
	return urls
}

// searchTraceMoe 走 trace.moe：整张图片作为请求体上传，它按帧比对番剧。
func (p *ImageSourcePlugin) searchTraceMoe(ctx context.Context, cfg imageSourceConfig, image []byte) ([]ImageSourceMatch, error) {
	endpoint := firstNonEmpty(p.traceMoeEndpoint, defaultTraceMoeEndpoint)
	return p.searchTraceMoeAt(ctx, imageSourceEndpoint(cfg.TraceMoeURL, endpoint), cfg, image)
}

func (p *ImageSourcePlugin) searchTraceMoeAt(ctx context.Context, base string, cfg imageSourceConfig, image []byte) ([]ImageSourceMatch, error) {
	endpoint := base + "?" + url.Values{"anilistInfo": {"1"}, "cutBorders": {"1"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(image))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "image/jpeg")
	payload, err := p.readJSONResponse(req)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Error  string `json:"error"`
		Result []struct {
			Anilist  json.RawMessage `json:"anilist"`
			Filename string          `json:"filename"`
			Episode  json.RawMessage `json:"episode"`
			From     float64         `json:"from"`
			To       float64         `json:"to"`
			Similar  float64         `json:"similarity"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("返回内容不是预期的 JSON：%w", err)
	}
	if strings.TrimSpace(parsed.Error) != "" {
		return nil, errors.New(strings.TrimSpace(parsed.Error))
	}
	matches := make([]ImageSourceMatch, 0, len(parsed.Result))
	for _, result := range parsed.Result {
		title, url := traceMoeAnilistTitle(result.Anilist)
		if title == "" {
			title = strings.TrimSpace(result.Filename)
		}
		if title == "" {
			continue
		}
		match := ImageSourceMatch{
			Provider: imageSourceProviderTraceMoe,
			// trace.moe 的相似度是 0~1 的小数，这里统一成百分比。
			Similarity: result.Similar * 100,
			Title:      title,
			Source:     "番剧截图",
			Detail:     traceMoeEpisodeDetail(result.Episode, result.From),
		}
		if url != "" {
			match.URLs = []string{url}
		}
		matches = append(matches, match)
	}
	return matches, nil
}

// traceMoeAnilistTitle 取番名。带 anilistInfo 时 anilist 是对象，不带时是个
// 纯数字 ID——两种都得认，否则换个参数就解析失败。
func traceMoeAnilistTitle(raw json.RawMessage) (title string, link string) {
	if len(raw) == 0 {
		return "", ""
	}
	var info struct {
		ID    int `json:"id"`
		Title struct {
			Native  string `json:"native"`
			Romaji  string `json:"romaji"`
			English string `json:"english"`
		} `json:"title"`
	}
	if err := json.Unmarshal(raw, &info); err == nil && (info.ID > 0 || info.Title.Native != "") {
		title = firstNonEmpty(
			strings.TrimSpace(info.Title.Native),
			strings.TrimSpace(info.Title.Romaji),
			strings.TrimSpace(info.Title.English),
		)
		if info.ID > 0 {
			link = fmt.Sprintf("https://anilist.co/anime/%d", info.ID)
		}
		return title, link
	}
	var id int
	if err := json.Unmarshal(raw, &id); err == nil && id > 0 {
		return "", fmt.Sprintf("https://anilist.co/anime/%d", id)
	}
	return "", ""
}

// traceMoeEpisodeDetail 把集数和出现时间拼成一句人话。episode 可能是数字、
// 字符串、数组，也可能干脆没有（剧场版）。
func traceMoeEpisodeDetail(rawEpisode json.RawMessage, from float64) string {
	parts := make([]string, 0, 2)
	if episode := traceMoeEpisodeLabel(rawEpisode); episode != "" {
		parts = append(parts, "第 "+episode+" 集")
	}
	if from > 0 {
		// 往下取整：from 是这一段的起点，说「约 01:23」拖进度条能看到那一帧，
		// 四舍五入到 01:24 反而越过去了。
		total := int(from)
		parts = append(parts, fmt.Sprintf("约 %02d:%02d", total/60, total%60))
	}
	return strings.Join(parts, " ")
}

func traceMoeEpisodeLabel(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err == nil && len(list) > 0 {
		labels := make([]string, 0, len(list))
		for _, item := range list {
			labels = append(labels, strings.TrimSpace(fmt.Sprint(item)))
		}
		return strings.Join(labels, "-")
	}
	return ""
}

func (p *ImageSourcePlugin) readJSONResponse(req *http.Request) ([]byte, error) {
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, imageSourceMaxResponseBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 正文常常就是原因（额度、鉴权），带一小段出来比只报状态码有用。
		return nil, fmt.Errorf("HTTP %d%s", resp.StatusCode, briefResponseDetail(payload))
	}
	return payload, nil
}

func briefResponseDetail(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if text == "" {
		return ""
	}
	if len([]rune(text)) > 120 {
		text = string([]rune(text)[:120]) + "…"
	}
	return "：" + text
}

func maxInt(value, floor int) int {
	if value < floor {
		return floor
	}
	return value
}
