// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

// 曲库来源的公共层。加一家平台只要实现 musicSource 并登记进 defaultMusicSources，
// 插件那边（music_plugin.go）不用动。
//
// 多曲库不是为了凑数：一首歌在网易云是会员专享、在酷狗能试听，这种事天天发生。
// 所以「搜到」不算数，「搜到而且放得出来」才算——一家放不了就问下一家，这是
// 多曲库唯一真正的意义。

// musicReference 是消息里认出来的一条分享引用。SongID 和 ShortURL 二选一，
// 后者要跟一次跳转才知道是哪首歌。
type musicReference struct {
	Source   string
	SongID   string
	ShortURL string
}

// song 是一首歌在这里需要知道的全部信息。ID 对本层是不透明的，
// 各家自己解释（网易云是数字 ID，QQ 是 songmid，酷狗是 hash:album_id）。
type song struct {
	Source   string
	ID       string
	Name     string
	Artists  string
	Album    string
	Duration time.Duration
	PlayURL  string
}

// Title 返回「歌名 - 歌手」这种一眼能认出来的说法，拿不到歌手时只留歌名。
func (s song) Title() string {
	name := strings.TrimSpace(s.Name)
	artists := strings.TrimSpace(s.Artists)
	switch {
	case name == "" && artists == "":
		return ""
	case artists == "":
		return name
	case name == "":
		return artists
	}
	return name + " - " + artists
}

// musicSource 是一家曲库。
type musicSource interface {
	Key() string
	Label() string
	// Referer 是下载音频时要带的来源页。各家 CDN 都按它挡外链，
	// 少带一个头就是 403，而 403 在群里表现为「机器人什么都没发」。
	Referer() string
	// References 从消息文本里认出属于这家平台的分享链接。
	References(text string) []musicReference
	// ResolveSongID 把引用变成歌曲 ID，短链在这里跟跳转。
	ResolveSongID(ctx context.Context, f *musicFetcher, cfg musicConfig, ref musicReference) string
	SongDetail(ctx context.Context, f *musicFetcher, cfg musicConfig, songID string) (song, bool)
	Search(ctx context.Context, f *musicFetcher, cfg musicConfig, query string) (song, bool)
	// PlayableURL 返回可下载的音频地址；拿不到（会员、独家、下架）返回空串。
	PlayableURL(ctx context.Context, f *musicFetcher, cfg musicConfig, songID string) string
}

func defaultMusicSources() []musicSource {
	return []musicSource{newNeteaseSource(), newQQSource(), newKugouSource()}
}

// musicSourceOptions 是一家曲库的凭据与自建接口地址。
type musicSourceOptions struct {
	APIBase string
	Cookie  string
}

func musicCookieValues(raw string) map[string]string {
	values := map[string]string{}
	for _, part := range strings.Split(raw, ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			values[key] = strings.TrimSpace(value)
		}
	}
	return values
}

// musicFetcher 收拢所有曲库共用的取数动作：挑客户端、取 JSON、跟跳转、下载。
type musicFetcher struct {
	// client 非空时所有请求都用它，测试用 httptest 注入；生产留空，
	// 对外抓取走 netguard 的公网客户端，只有管理员自己填的自建接口用普通客户端
	// ——自建服务通常就在 127.0.0.1，被 SSRF 防护挡掉才是错的。
	client *http.Client
}

type musicDiagnosticsKey struct{}

type musicDiagnostics struct {
	failures []string
}

func withMusicDiagnostics(ctx context.Context) (context.Context, *musicDiagnostics) {
	diagnostics := &musicDiagnostics{}
	return context.WithValue(ctx, musicDiagnosticsKey{}, diagnostics), diagnostics
}

func recordMusicFailure(ctx context.Context, format string, args ...any) {
	diagnostics, _ := ctx.Value(musicDiagnosticsKey{}).(*musicDiagnostics)
	if diagnostics == nil {
		return
	}
	diagnostics.failures = append(diagnostics.failures, fmt.Sprintf(format, args...))
}

func (d *musicDiagnostics) failed() bool { return d != nil && len(d.failures) > 0 }

func (f *musicFetcher) httpClient(cfg musicConfig, guarded bool) *http.Client {
	if f != nil && f.client != nil {
		return f.client
	}
	if guarded {
		return netguard.NewPublicHTTPClient(cfg.Timeout)
	}
	return &http.Client{Timeout: cfg.Timeout}
}

func (f *musicFetcher) request(ctx context.Context, cfg musicConfig, endpoint string, guarded bool, headers map[string]string) (*http.Response, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		recordMusicFailure(ctx, "创建请求失败：%v", err)
		return nil, false
	}
	applyBrowserHeaders(req, "")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := f.httpClient(cfg, guarded).Do(req)
	if err != nil {
		log.Printf("music request failed for %s: %v", redactURLQuery(endpoint), err)
		recordMusicFailure(ctx, "请求 %s 失败：%v", redactURLQuery(endpoint), err)
		return nil, false
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Printf("music bad status for %s: %s", redactURLQuery(endpoint), resp.Status)
		recordMusicFailure(ctx, "请求 %s 返回 %s", redactURLQuery(endpoint), resp.Status)
		resp.Body.Close()
		return nil, false
	}
	return resp, true
}

// fetchJSON 取一段 JSON。有的接口（QQ 音乐的搜索）返回的是 jsonp 或带 BOM 的
// text/html，所以解析前先把包裹剥掉，只按 Content-Type 判断会解不出来。
func (f *musicFetcher) fetchJSON(ctx context.Context, cfg musicConfig, endpoint string, guarded bool, headers map[string]string, target any) bool {
	resp, ok := f.request(ctx, cfg, endpoint, guarded, headers)
	if !ok {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || len(body) == 0 {
		recordMusicFailure(ctx, "读取 %s 返回失败：%v", redactURLQuery(endpoint), err)
		return false
	}
	if err := json.Unmarshal(unwrapJSONPayload(body), target); err != nil {
		log.Printf("music parse failed for %s: %v", redactURLQuery(endpoint), err)
		recordMusicFailure(ctx, "解析 %s 返回失败：%v", redactURLQuery(endpoint), err)
		return false
	}
	return true
}

// unwrapJSONPayload 剥掉 BOM 和 jsonp 的 callback(...) 外壳。
func unwrapJSONPayload(body []byte) []byte {
	text := strings.TrimSpace(strings.TrimPrefix(string(body), "\ufeff"))
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		return []byte(text)
	}
	open := strings.Index(text, "(")
	closing := strings.LastIndex(text, ")")
	if open < 0 || closing <= open {
		return []byte(text)
	}
	return []byte(strings.TrimSpace(text[open+1 : closing]))
}

// finalURL 跟完跳转，返回落点地址。短链和「外链换真实 CDN 地址」都靠它。
func (f *musicFetcher) finalURL(ctx context.Context, cfg musicConfig, raw string, headers map[string]string) string {
	resp, ok := f.request(ctx, cfg, raw, true, headers)
	if !ok {
		return ""
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.Request == nil || resp.Request.URL == nil {
		return ""
	}
	return resp.Request.URL.String()
}

// downloadToFile 与链接解析的下载同规格：先看 Content-Length，再按上限截断读取，
// 超限直接判失败而不是发一段截断的音频出去。
func (f *musicFetcher) downloadToFile(ctx context.Context, cfg musicConfig, raw, path string, headers map[string]string) bool {
	resp, ok := f.request(ctx, cfg, raw, true, headers)
	if !ok {
		return false
	}
	defer resp.Body.Close()
	if cfg.MaxBytes > 0 && resp.ContentLength > cfg.MaxBytes {
		log.Printf("music audio too large for %s: %d > %d", redactURLQuery(raw), resp.ContentLength, cfg.MaxBytes)
		return false
	}
	file, err := os.Create(path)
	if err != nil {
		return false
	}
	defer file.Close()
	written, err := io.Copy(file, io.LimitReader(resp.Body, cfg.MaxBytes+1))
	if err != nil || written == 0 || written > cfg.MaxBytes {
		log.Printf("music write failed for %s: written=%d err=%v", redactURLQuery(raw), written, err)
		return false
	}
	return true
}

// musicAudioExtensions 是各家 CDN 会给到的音频后缀，也用来判断外链落点是不是音频。
var musicAudioExtensions = []string{".mp3", ".m4a", ".flac", ".aac", ".wav", ".ogg"}

// musicLinkLooksPlayable 判断一个地址的落点像不像音频。
//
// 好几家的外链接口对下架和会员专享曲目都不报错，只是把人送到一个页面。不看落点
// 就会把一段 HTML 当音频发出去，群里收到的是一条打不开的静音语音。
func musicLinkLooksPlayable(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	path := strings.ToLower(parsed.Path)
	if strings.Contains(path, "/404") || strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".htm") {
		return false
	}
	for _, ext := range musicAudioExtensions {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// orderedSources 返回这次要问的曲库，按「优先那家排头、其余按登记顺序」排列。
func (p *MusicPlugin) orderedSources(cfg musicConfig) []musicSource {
	enabled := make([]musicSource, 0, len(p.sources))
	for _, source := range p.sources {
		if cfg.sourceEnabled(source.Key()) {
			enabled = append(enabled, source)
		}
	}
	preferred := strings.TrimSpace(cfg.PreferredSource)
	if preferred == "" {
		return enabled
	}
	slices.SortStableFunc(enabled, func(a, b musicSource) int {
		switch {
		case a.Key() == preferred && b.Key() != preferred:
			return -1
		case b.Key() == preferred && a.Key() != preferred:
			return 1
		default:
			return 0
		}
	})
	return enabled
}

func (p *MusicPlugin) sourceByKey(key string) (musicSource, bool) {
	for _, source := range p.sources {
		if source.Key() == key {
			return source, true
		}
	}
	return nil, false
}

// musicReferencesIn 按启用的曲库认出消息里的分享链接，顺序跟着曲库顺序走。
func (p *MusicPlugin) musicReferencesIn(cfg musicConfig, text string) []musicReference {
	out := make([]musicReference, 0, 2)
	for _, source := range p.orderedSources(cfg) {
		out = append(out, source.References(text)...)
	}
	return out
}
