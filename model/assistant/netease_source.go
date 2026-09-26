// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// 网易云音乐曲库。公共层见 music_source.go。

const (
	neteaseDetailAPI  = "https://music.163.com/api/song/detail/?ids=%%5B%s%%5D"
	neteaseSearchAPI  = "https://music.163.com/api/search/get/?type=1&offset=0&limit=5&s=%s"
	neteaseSongURLAPI = "https://music.163.com/api/song/enhance/player/url?id=%s&ids=%%5B%s%%5D&br=%d"
	neteaseOuterURL   = "https://music.163.com/song/media/outer/url?id=%s.mp3"
	neteaseAccountAPI = "https://music.163.com/api/nuser/account/get"
	neteaseReferer    = "https://music.163.com/"
)

var (
	neteaseHosts      = []string{"music.163.com", "163.com"}
	neteaseShortHosts = []string{"163cn.tv"}
	// 路径形式的歌曲页：/song/1234、/m/song/1234。查询串形式在下面单独取。
	neteasePathIDPattern = regexp.MustCompile(`(?i)/song/(\d+)`)
)

// neteaseSource 的接口地址做成字段而不是直接用常量，是为了让测试指向本地服务器
// ——否则「自建 API 挂了要退回官方」这类分支只能靠真的去打线上接口才能验，
// 测试就成了对外部服务的依赖。其余几家同理。
type neteaseSource struct {
	detailAPI  string
	searchAPI  string
	songURLAPI string
	outerURL   string
	accountAPI string
}

func newNeteaseSource() *neteaseSource {
	return &neteaseSource{detailAPI: neteaseDetailAPI, searchAPI: neteaseSearchAPI, songURLAPI: neteaseSongURLAPI, outerURL: neteaseOuterURL, accountAPI: neteaseAccountAPI}
}

func (s *neteaseSource) Key() string     { return "netease" }
func (s *neteaseSource) Label() string   { return "网易云音乐" }
func (s *neteaseSource) Referer() string { return neteaseReferer }

func (s *neteaseSource) References(text string) []musicReference {
	out := make([]musicReference, 0, 2)
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
				out = append(out, musicReference{Source: s.Key(), ShortURL: raw})
			}
			continue
		}
		if !hostMatchesDomain(host, neteaseHosts...) {
			continue
		}
		if id := neteaseSongIDFromURL(parsed); id != "" && !seen["id:"+id] {
			seen["id:"+id] = true
			out = append(out, musicReference{Source: s.Key(), SongID: id})
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
	if match := neteasePathIDPattern.FindStringSubmatch(path); len(match) == 2 {
		if id := musicNumericID(match[1]); id != "" {
			return id
		}
	}
	return musicNumericID(queryID)
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

// musicNumericID 只接受纯数字 ID，顺手挡掉超长输入。
func musicNumericID(value string) string {
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

func (s *neteaseSource) headers(cfg musicConfig) map[string]string {
	headers := map[string]string{"Referer": neteaseReferer, "Accept": "application/json, text/plain, */*"}
	if cookie := cfg.sourceOptions(s.Key()).Cookie; cookie != "" {
		headers["Cookie"] = "MUSIC_U=" + cookie
	}
	return headers
}

// songURLHeaders 是官方取地址接口专用的请求头。
//
// 只带 MUSIC_U 时，接口按网页端处理，给的是带 authSecret 的地址，服务端去下载
// 一律 403——连本来能走外链的免费歌也放不了。带上 os=pc 就按桌面客户端给普通
// CDN 地址，会员歌能下到整首。游客不带 Cookie 时两种地址都能下，保持原样。
func (s *neteaseSource) songURLHeaders(cfg musicConfig) map[string]string {
	headers := s.headers(cfg)
	if cookie, ok := headers["Cookie"]; ok {
		headers["Cookie"] = "os=pc; appver=2.9.7; " + cookie
	}
	return headers
}

// ResolveSongID 补上短链那一步：163cn.tv 得跟一次跳转才知道是哪首歌。
func (s *neteaseSource) ResolveSongID(ctx context.Context, f *musicFetcher, cfg musicConfig, ref musicReference) string {
	if ref.SongID != "" {
		return ref.SongID
	}
	final := f.finalURL(ctx, cfg, ref.ShortURL, nil)
	if final == "" {
		return ""
	}
	parsed, err := url.Parse(final)
	if err != nil || !hostMatchesDomain(parsed.Hostname(), neteaseHosts...) {
		return ""
	}
	return neteaseSongIDFromURL(parsed)
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
	album := strings.TrimSpace(s.Album.Name)
	if album == "" {
		album = strings.TrimSpace(s.AL.Name)
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
	milliseconds := s.Duration
	if milliseconds == 0 {
		milliseconds = s.DT
	}
	return newSong("netease", strings.TrimSpace(s.ID.String()), s.Name, names, album,
		time.Duration(milliseconds)*time.Millisecond)
}

// newSong 是各家解析结果的统一出口：没有歌名或没有 ID 的条目一律判失败，
// 免得把一首「没名字的歌」发到群里。
func newSong(source, id, name string, artists []string, album string, duration time.Duration) (song, bool) {
	name = strings.TrimSpace(name)
	id = strings.TrimSpace(id)
	if name == "" || id == "" {
		return song{}, false
	}
	return song{
		Source:   source,
		ID:       id,
		Name:     name,
		Artists:  strings.Join(artists, "/"),
		Album:    strings.TrimSpace(album),
		Duration: duration,
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
		URL string `json:"url"`
		// FreeTrialInfo 非空说明给的是 30 秒试听片段：登录了但不是会员时，
		// 会员歌会走到这里。把试听当整首发出去，群里听到的是半截歌。
		FreeTrialInfo json.RawMessage `json:"freeTrialInfo"`
	} `json:"data"`
}

// fullTrackURL 取第一条完整曲目的地址，试听片段不算。
func (r neteaseSongURLResponse) fullTrackURL() string {
	for _, entry := range r.Data {
		trial := strings.TrimSpace(string(entry.FreeTrialInfo))
		if trial != "" && trial != "null" {
			continue
		}
		if candidate := strings.TrimSpace(entry.URL); candidate != "" {
			return candidate
		}
	}
	return ""
}

// SongDetail 取歌名、歌手、专辑和时长。
//
// 自建 API 和官方接口返回的是同一套字段，所以先试自建、失败退回官方：
// 自建实例挂了不该让整个功能一起哑掉。其余几家同理。
func (s *neteaseSource) SongDetail(ctx context.Context, f *musicFetcher, cfg musicConfig, songID string) (song, bool) {
	decode := func(endpoint string, guarded bool) (song, bool) {
		var payload neteaseDetailResponse
		if !f.fetchJSON(ctx, cfg, endpoint, guarded, s.headers(cfg), &payload) || len(payload.Songs) == 0 {
			return song{}, false
		}
		return payload.Songs[0].toSong()
	}
	if base := cfg.sourceOptions(s.Key()).APIBase; base != "" {
		if found, ok := decode(base+"/song/detail?ids="+url.QueryEscape(songID), false); ok {
			return found, true
		}
	}
	return decode(fmt.Sprintf(s.detailAPI, url.QueryEscape(songID)), true)
}

func (s *neteaseSource) Search(ctx context.Context, f *musicFetcher, cfg musicConfig, query string) (song, bool) {
	decode := func(endpoint string, guarded bool) (song, bool) {
		var payload neteaseSearchResponse
		if !f.fetchJSON(ctx, cfg, endpoint, guarded, s.headers(cfg), &payload) {
			return song{}, false
		}
		for _, entry := range payload.Result.Songs {
			if found, ok := entry.toSong(); ok {
				return found, true
			}
		}
		return song{}, false
	}
	if base := cfg.sourceOptions(s.Key()).APIBase; base != "" {
		endpoint := fmt.Sprintf("%s/search?type=1&limit=5&keywords=%s", base, url.QueryEscape(query))
		if found, ok := decode(endpoint, false); ok {
			return found, true
		}
	}
	return decode(fmt.Sprintf(s.searchAPI, url.QueryEscape(query)), true)
}

func (s *neteaseSource) PlayableURL(ctx context.Context, f *musicFetcher, cfg musicConfig, songID string) string {
	if base := cfg.sourceOptions(s.Key()).APIBase; base != "" {
		endpoint := fmt.Sprintf("%s/song/url?id=%s&br=%d", base, url.QueryEscape(songID), cfg.Bitrate)
		var payload neteaseSongURLResponse
		if f.fetchJSON(ctx, cfg, endpoint, false, s.headers(cfg), &payload) {
			if candidate := payload.fullTrackURL(); candidate != "" {
				return candidate
			}
		}
	}
	// 官方取地址接口认 MUSIC_U：填了会员账号的 Cookie，会员歌就能拿到整首。
	// 以前没填自建接口时只走外链，而外链不带 Cookie，填了 MUSIC_U 等于白填。
	bitrate := cfg.Bitrate
	if bitrate <= 0 {
		bitrate = 320000
	}
	var payload neteaseSongURLResponse
	escaped := url.QueryEscape(songID)
	if f.fetchJSON(ctx, cfg, fmt.Sprintf(s.songURLAPI, escaped, escaped, bitrate), true, s.songURLHeaders(cfg), &payload) {
		if candidate := payload.fullTrackURL(); candidate != "" {
			return candidate
		}
	}
	// 官方外链是一次 302。落点不像音频就说明这首歌是会员或独家，别把 404 页
	// 当音频下下来。
	final := f.finalURL(ctx, cfg, fmt.Sprintf(s.outerURL, url.QueryEscape(songID)), nil)
	if final == "" || !musicLinkLooksPlayable(final) {
		return ""
	}
	return final
}
