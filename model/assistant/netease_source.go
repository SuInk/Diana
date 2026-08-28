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
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

// 音乐增强的曲库来源。这里只管「怎么从网易云拿到一首歌」，怎么把歌变成一条语音
// 在 music_plugin.go。分开是为了将来加别的平台时插件那边不用动。

const (
	neteaseDetailAPI = "https://music.163.com/api/song/detail/?ids=%%5B%s%%5D"
	neteaseSearchAPI = "https://music.163.com/api/search/get/?type=1&offset=0&limit=5&s=%s"
	neteaseOuterURL  = "https://music.163.com/song/media/outer/url?id=%s.mp3"
	neteaseReferer   = "https://music.163.com/"
)

var (
	neteaseHosts      = []string{"music.163.com", "163.com"}
	neteaseShortHosts = []string{"163cn.tv"}
	// 路径形式的歌曲页：/song/1234、/m/song/1234。查询串形式在下面单独取。
	neteasePathIDPattern = regexp.MustCompile(`(?i)/song/(\d+)`)
)

// neteaseReference 是消息里认出来的一条网易云引用：要么已经拿到歌曲 ID，
// 要么是需要跟一次重定向才知道 ID 的短链。
type neteaseReference struct {
	SongID   string
	ShortURL string
}

// neteaseReferences 从消息文本里找出网易云歌曲引用。
func neteaseReferences(text string) []neteaseReference {
	out := make([]neteaseReference, 0, 2)
	seen := map[string]bool{}
	for _, raw := range extractURLs(text) {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := parsed.Hostname()
		if hostMatchesDomain(host, neteaseShortHosts...) {
			if !seen["short:"+raw] {
				seen["short:"+raw] = true
				out = append(out, neteaseReference{ShortURL: raw})
			}
			continue
		}
		if !hostMatchesDomain(host, neteaseHosts...) {
			continue
		}
		if id := neteaseSongIDFromURL(parsed); id != "" && !seen["id:"+id] {
			seen["id:"+id] = true
			out = append(out, neteaseReference{SongID: id})
		}
	}
	return out
}

// neteaseSongIDFromURL 从歌曲页地址里取歌曲 ID。
//
// 网易云同一首歌有好几种地址写法：查询串 ?id=、单页应用的 #/song?id=、
// 移动端的 /m/song/1234。三种都得认，只认一种就会出现「这条链接没反应」。
//
// 但 ?id= 这个参数歌手页、专辑页、歌单页也都有，光看参数会把 /artist?id=6452
// 当成歌曲去请求接口。所以路径必须先说明这是一首歌，再取 ID。
func neteaseSongIDFromURL(parsed *url.URL) string {
	if id := neteaseSongIDFromParts(parsed.Path, parsed.Query().Get("id")); id != "" {
		return id
	}
	fragment := strings.TrimSpace(parsed.Fragment)
	if fragment == "" {
		return ""
	}
	// 单页应用把真正的路由塞在 # 后面，按同一套规则再解一次。
	inner, err := url.Parse(strings.TrimPrefix(fragment, "/"))
	if err != nil {
		return ""
	}
	return neteaseSongIDFromParts(inner.Path, inner.Query().Get("id"))
}

func neteaseSongIDFromParts(path, queryID string) string {
	if !neteasePathIsSong(path) {
		return ""
	}
	if id := neteaseIDFromPath(path); id != "" {
		return id
	}
	return neteaseNumericID(queryID)
}

// neteasePathIsSong 判断路径讲的是不是单曲：/song、/m/song、/song/1234
// 都算，/artist、/album、/playlist 不算。
func neteasePathIsSong(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if strings.EqualFold(strings.TrimSpace(segment), "song") {
			return true
		}
	}
	return false
}

func neteaseIDFromPath(path string) string {
	match := neteasePathIDPattern.FindStringSubmatch(path)
	if len(match) < 2 {
		return ""
	}
	return neteaseNumericID(match[1])
}

// neteaseNumericID 只接受纯数字的歌曲 ID，顺手挡掉超长输入。
func neteaseNumericID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 20 {
		return ""
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return ""
		}
	}
	if strings.Trim(value, "0") == "" {
		return ""
	}
	return value
}

// httpClient 按目标挑客户端：管理员自己填的自建 API 用普通客户端（自建服务
// 多半就在 127.0.0.1，被 SSRF 防护挡掉才是错的），其余一律走 netguard。
func (p *MusicPlugin) httpClient(cfg musicConfig, guarded bool) *http.Client {
	if p.client != nil {
		return p.client
	}
	if guarded {
		return netguard.NewPublicHTTPClient(cfg.Timeout)
	}
	return &http.Client{Timeout: cfg.Timeout}
}

// resolveSongID 补上短链那一步：163cn.tv 得跟一次跳转才知道是哪首歌。
func (p *MusicPlugin) resolveSongID(ctx context.Context, cfg musicConfig, ref neteaseReference) string {
	if ref.SongID != "" {
		return ref.SongID
	}
	final := p.finalURL(ctx, cfg, ref.ShortURL)
	if final == "" {
		return ""
	}
	parsed, err := url.Parse(final)
	if err != nil || !hostMatchesDomain(parsed.Hostname(), neteaseHosts...) {
		return ""
	}
	return neteaseSongIDFromURL(parsed)
}

func (p *MusicPlugin) finalURL(ctx context.Context, cfg musicConfig, raw string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return ""
	}
	applyBrowserHeaders(req, "")
	resp, err := p.httpClient(cfg, true).Do(req)
	if err != nil {
		log.Printf("music short link failed for %s: %v", redactURLQuery(raw), err)
		return ""
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.Request == nil || resp.Request.URL == nil {
		return ""
	}
	return resp.Request.URL.String()
}

type neteaseNamed struct {
	Name string `json:"name"`
}

// neteaseSongPayload 覆盖网易云两代字段名。详情接口和老搜索接口用
// artists/album/duration，NeteaseCloudMusicApi 的部分接口用 ar/al/dt，
// 只认一套就会在别人的自建实例上解出一首没名字的歌。
type neteaseSongPayload struct {
	ID       json.Number    `json:"id"`
	Name     string         `json:"name"`
	Artists  []neteaseNamed `json:"artists"`
	AR       []neteaseNamed `json:"ar"`
	Album    neteaseNamed   `json:"album"`
	AL       neteaseNamed   `json:"al"`
	Duration int64          `json:"duration"`
	DT       int64          `json:"dt"`
}

func (s neteaseSongPayload) toSong() (song, bool) {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		return song{}, false
	}
	artists := s.Artists
	if len(artists) == 0 {
		artists = s.AR
	}
	names := make([]string, 0, len(artists))
	for _, artist := range artists {
		if trimmed := strings.TrimSpace(artist.Name); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	album := strings.TrimSpace(s.Album.Name)
	if album == "" {
		album = strings.TrimSpace(s.AL.Name)
	}
	milliseconds := s.Duration
	if milliseconds == 0 {
		milliseconds = s.DT
	}
	return song{
		ID:       strings.TrimSpace(s.ID.String()),
		Name:     name,
		Artists:  strings.Join(names, "/"),
		Album:    album,
		Duration: time.Duration(milliseconds) * time.Millisecond,
	}, true
}

type neteaseDetailResponse struct {
	Code  int                  `json:"code"`
	Songs []neteaseSongPayload `json:"songs"`
}

type neteaseSearchResponse struct {
	Code   int `json:"code"`
	Result struct {
		Songs []neteaseSongPayload `json:"songs"`
	} `json:"result"`
}

type neteaseSongURLResponse struct {
	Code int `json:"code"`
	Data []struct {
		ID   json.Number `json:"id"`
		URL  string      `json:"url"`
		Size int64       `json:"size"`
		Type string      `json:"type"`
	} `json:"data"`
}

// fetchSongDetail 取歌名、歌手、专辑和时长。
//
// 自建 API 和官方接口返回的是同一套字段，所以先试自建、失败退回官方：
// 自建实例挂了不该让整个功能一起哑掉。
func (p *MusicPlugin) fetchSongDetail(ctx context.Context, cfg musicConfig, songID string) (song, bool) {
	if cfg.APIBase != "" {
		if found, ok := p.decodeSongDetail(ctx, cfg, cfg.APIBase+"/song/detail?ids="+songID, false); ok {
			return found, true
		}
	}
	return p.decodeSongDetail(ctx, cfg, fmt.Sprintf(p.officialDetailAPI, songID), true)
}

func (p *MusicPlugin) decodeSongDetail(ctx context.Context, cfg musicConfig, endpoint string, guarded bool) (song, bool) {
	var payload neteaseDetailResponse
	if !p.fetchJSON(ctx, cfg, endpoint, guarded, &payload) || len(payload.Songs) == 0 {
		return song{}, false
	}
	return payload.Songs[0].toSong()
}

// searchSong 按关键词搜一首歌，取第一条结果。
//
// 只取第一条而不是把候选交给模型再挑一轮：搜索排序本身就是按匹配度和热度来的，
// 多一轮模型调用换来的排序未必更好，却让点歌多等一次往返。
func (p *MusicPlugin) searchSong(ctx context.Context, cfg musicConfig, query string) (song, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return song{}, false
	}
	if cfg.APIBase != "" {
		endpoint := fmt.Sprintf("%s/search?type=1&limit=5&keywords=%s", cfg.APIBase, url.QueryEscape(query))
		if found, ok := p.decodeSearch(ctx, cfg, endpoint, false); ok {
			return found, true
		}
	}
	return p.decodeSearch(ctx, cfg, fmt.Sprintf(p.officialSearchAPI, url.QueryEscape(query)), true)
}

func (p *MusicPlugin) decodeSearch(ctx context.Context, cfg musicConfig, endpoint string, guarded bool) (song, bool) {
	var payload neteaseSearchResponse
	if !p.fetchJSON(ctx, cfg, endpoint, guarded, &payload) {
		return song{}, false
	}
	for _, entry := range payload.Result.Songs {
		found, ok := entry.toSong()
		if !ok || found.ID == "" {
			continue
		}
		return found, true
	}
	return song{}, false
}

// fetchPlayableURL 拿到真正能下载的音频地址。
//
// 官方外链接口对下架和会员专享曲目会跳到 404 页而不是报错，所以拿到地址还要
// 看一眼落点像不像音频——否则下载下来的是一段 HTML，发出去是一条静音语音。
func (p *MusicPlugin) fetchPlayableURL(ctx context.Context, cfg musicConfig, songID string) string {
	if cfg.APIBase != "" {
		endpoint := fmt.Sprintf("%s/song/url?id=%s&br=%d", cfg.APIBase, songID, cfg.Bitrate)
		var payload neteaseSongURLResponse
		if p.fetchJSON(ctx, cfg, endpoint, false, &payload) {
			for _, entry := range payload.Data {
				if candidate := strings.TrimSpace(entry.URL); candidate != "" {
					return candidate
				}
			}
		}
	}
	final := p.finalURL(ctx, cfg, fmt.Sprintf(p.officialOuterURL, songID))
	if final == "" || !neteaseLooksPlayable(final) {
		return ""
	}
	return final
}

// neteaseLooksPlayable 判断外链跳转的落点是不是音频。
func neteaseLooksPlayable(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	path := strings.ToLower(parsed.Path)
	if strings.Contains(path, "/404") || strings.HasSuffix(path, ".html") {
		return false
	}
	for _, ext := range neteaseAudioExtensions {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

var neteaseAudioExtensions = []string{".mp3", ".m4a", ".flac", ".aac", ".wav", ".ogg"}

func (p *MusicPlugin) fetchJSON(ctx context.Context, cfg musicConfig, endpoint string, guarded bool, target any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	applyBrowserHeaders(req, "")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", neteaseReferer)
	if cfg.Cookie != "" {
		req.Header.Set("Cookie", "MUSIC_U="+cfg.Cookie)
	}
	resp, err := p.httpClient(cfg, guarded).Do(req)
	if err != nil {
		log.Printf("music request failed for %s: %v", redactURLQuery(endpoint), err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Printf("music bad status for %s: %s", redactURLQuery(endpoint), resp.Status)
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || len(body) == 0 {
		return false
	}
	if err := json.Unmarshal(body, target); err != nil {
		log.Printf("music parse failed for %s: %v", redactURLQuery(endpoint), err)
		return false
	}
	return true
}

func (p *MusicPlugin) downloadAudio(ctx context.Context, cfg musicConfig, raw string) (string, error) {
	if err := os.MkdirAll(cfg.OutputDir, 0o700); err != nil {
		return "", fmt.Errorf("创建音乐缓存目录失败: %w", err)
	}
	file, err := os.CreateTemp(cfg.OutputDir, "diana-music-*"+neteaseAudioExt(raw))
	if err != nil {
		return "", fmt.Errorf("创建音乐缓存文件失败: %w", err)
	}
	path := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		cleanupLocalMediaFile(path)
		return "", fmt.Errorf("创建音乐缓存文件失败: %w", closeErr)
	}
	headers := map[string]string{"User-Agent": resolverUserAgent, "Referer": neteaseReferer}
	if !p.downloadToFile(ctx, cfg, raw, path, headers) {
		cleanupLocalMediaFile(path)
		return "", fmt.Errorf("下载音频失败")
	}
	return path, nil
}

// downloadToFile 与链接解析的下载同规格：先看 Content-Length，再按上限截断读取，
// 超限直接判失败而不是发一段截断的音频出去。
func (p *MusicPlugin) downloadToFile(ctx context.Context, cfg musicConfig, raw, path string, headers map[string]string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return false
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := p.httpClient(cfg, true).Do(req)
	if err != nil {
		log.Printf("music download failed for %s: %v", redactURLQuery(raw), err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Printf("music download bad status for %s: %s", redactURLQuery(raw), resp.Status)
		return false
	}
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

func neteaseAudioExt(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ".mp3"
	}
	ext := strings.ToLower(filepath.Ext(parsed.Path))
	for _, candidate := range neteaseAudioExtensions {
		if ext == candidate {
			return ext
		}
	}
	return ".mp3"
}
