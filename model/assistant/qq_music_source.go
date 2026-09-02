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

// QQ 音乐曲库。公共层见 music_source.go。
//
// 说清楚这家的现实情况：没有登录态时，取播放地址那一步（vkey）多半只给得出
// 可试听的曲目，会员和独家会返回空地址。填上自建 QQMusicApi 或 Cookie 才稳。
// 拿不到就返回空串，由上层换下一家曲库——这正是多曲库存在的理由。

const (
	qqSearchAPI = "https://c.y.qq.com/soso/fcgi-bin/client_search_cp?format=json&platform=yqq.json&new_json=1&p=1&n=5&w=%s"
	qqVkeyAPI   = "https://u.y.qq.com/cgi-bin/musicu.fcg?format=json&data=%s"
	qqStreamCDN = "https://ws.stream.qqmusic.qq.com/"
	qqReferer   = "https://y.qq.com/"
)

var (
	qqHosts      = []string{"y.qq.com"}
	qqShortHosts = []string{"url.cn"}
	// songmid 是字母数字混排的短串，songDetail/<mid> 和 song/<mid>.html 两种路径都有。
	qqPathMIDPattern = regexp.MustCompile(`(?i)/(?:songDetail|song)/([A-Za-z0-9]{6,32})(?:\.html)?`)
)

type qqSource struct {
	searchAPI string
	vkeyAPI   string
	streamCDN string
}

func newQQSource() *qqSource {
	return &qqSource{searchAPI: qqSearchAPI, vkeyAPI: qqVkeyAPI, streamCDN: qqStreamCDN}
}

func (s *qqSource) Key() string     { return "qq" }
func (s *qqSource) Label() string   { return "QQ 音乐" }
func (s *qqSource) Referer() string { return qqReferer }

func (s *qqSource) References(text string) []musicReference {
	out := make([]musicReference, 0, 2)
	seen := map[string]bool{}
	for _, raw := range extractURLs(text) {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := parsed.Hostname()
		if hostMatchesDomain(host, qqShortHosts...) {
			if !seen["short:"+raw] {
				seen["short:"+raw] = true
				out = append(out, musicReference{Source: s.Key(), ShortURL: raw})
			}
			continue
		}
		if !hostMatchesDomain(host, qqHosts...) {
			continue
		}
		if mid := qqSongMIDFromURL(parsed); mid != "" && !seen["id:"+mid] {
			seen["id:"+mid] = true
			out = append(out, musicReference{Source: s.Key(), SongID: mid})
		}
	}
	return out
}

// qqSongMIDFromURL 取 songmid。和网易云同理，songmid 这个参数只出现在单曲页，
// 但路径里的 /album/ /singer/ /playlist/ 也可能带别的 id，所以只认单曲那几种写法。
func qqSongMIDFromURL(parsed *url.URL) string {
	if mid := qqAlphanumericID(parsed.Query().Get("songmid")); mid != "" {
		return mid
	}
	for _, path := range []string{parsed.Path, strings.TrimPrefix(strings.TrimSpace(parsed.Fragment), "/")} {
		if match := qqPathMIDPattern.FindStringSubmatch(path); len(match) == 2 {
			if mid := qqAlphanumericID(match[1]); mid != "" {
				return mid
			}
		}
	}
	return ""
}

func qqAlphanumericID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 6 || len(value) > 32 {
		return ""
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return ""
		}
	}
	return value
}

func (s *qqSource) headers(cfg musicConfig) map[string]string {
	headers := map[string]string{"Referer": qqReferer, "Accept": "application/json, text/plain, */*"}
	if cookie := cfg.sourceOptions(s.Key()).Cookie; cookie != "" {
		headers["Cookie"] = cookie
	}
	return headers
}

// qqUINFromCookie 读取 QQ 音乐登录态里的账号。只把 Cookie 原样塞进请求头还
// 不够：vkey 的请求体也必须带同一个 uin，否则服务端会按游客处理，会员歌曲
// 仍然返回空 purl。
func qqUINFromCookie(raw string) string {
	values := musicCookieValues(raw)
	for _, key := range []string{"uin", "qqmusic_uin", "musicid", "strmusicid"} {
		value := strings.TrimSpace(values[key])
		value = strings.TrimPrefix(value, "o")
		if musicNumericID(value) != "" {
			return value
		}
	}
	return "0"
}

func qqVkeyRequest(songID, rawCookie string) ([]byte, error) {
	uin := qqUINFromCookie(rawCookie)
	if uin == "0" {
		// 游客请求保留已经验证过的旧接口；新接口在无登录态时更容易直接
		// 返回空 purl。只有 Cookie 里能取到真实账号时才走会员请求。
		return json.Marshal(map[string]any{
			"req_0": map[string]any{
				"module": "vkey.GetVkeyServer",
				"method": "CgiGetVkey",
				"param": map[string]any{
					"guid": qqGuid(songID), "songmid": []string{songID}, "songtype": []int{0},
					"uin": "0", "loginflag": 1, "platform": "20",
				},
			},
			"comm": map[string]any{"uin": 0, "format": "json", "ct": 24, "cv": 0},
		})
	}
	return json.Marshal(map[string]any{
		"req_0": map[string]any{
			"module": "music.vkey.GetVkey",
			"method": "UrlGetVkey",
			"param": map[string]any{
				"guid":           qqGuid(songID),
				"songmid":        []string{songID},
				"songtype":       []int{0},
				"filename":       []string{"M500" + songID + songID + ".mp3"},
				"uin":            uin,
				"loginflag":      1,
				"platform":       "23",
				"h5queryversion": 1,
				"quality":        "M500",
			},
		},
		"comm": map[string]any{"uin": uin, "format": "json", "ct": 24, "cv": 0},
	})
}

func (s *qqSource) ResolveSongID(ctx context.Context, f *musicFetcher, cfg musicConfig, ref musicReference) string {
	if ref.SongID != "" {
		return ref.SongID
	}
	final := f.finalURL(ctx, cfg, ref.ShortURL, nil)
	if final == "" {
		return ""
	}
	parsed, err := url.Parse(final)
	if err != nil || !hostMatchesDomain(parsed.Hostname(), qqHosts...) {
		return ""
	}
	return qqSongMIDFromURL(parsed)
}

type qqNamed struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

func (n qqNamed) text() string {
	return firstNonEmpty(strings.TrimSpace(n.Name), strings.TrimSpace(n.Title))
}

// qqSongPayload 覆盖 QQ 音乐新老两代搜索结果的字段名：老接口是
// songmid/songname/albumname/interval，new_json=1 之后是 mid/name/album/interval。
// 只认一套就会在对方悄悄换版本后解出一首没名字的歌。
type qqSongPayload struct {
	Mid       string    `json:"mid"`
	SongMid   string    `json:"songmid"`
	Name      string    `json:"name"`
	SongName  string    `json:"songname"`
	Singer    []qqNamed `json:"singer"`
	Album     qqNamed   `json:"album"`
	AlbumName string    `json:"albumname"`
	Interval  int64     `json:"interval"`
}

func (s qqSongPayload) toSong() (song, bool) {
	names := make([]string, 0, len(s.Singer))
	for _, singer := range s.Singer {
		if name := singer.text(); name != "" {
			names = append(names, name)
		}
	}
	album := firstNonEmpty(s.Album.text(), strings.TrimSpace(s.AlbumName))
	return newSong("qq",
		firstNonEmpty(strings.TrimSpace(s.Mid), strings.TrimSpace(s.SongMid)),
		firstNonEmpty(strings.TrimSpace(s.Name), strings.TrimSpace(s.SongName)),
		names, album, time.Duration(s.Interval)*time.Second)
}

type qqSearchResponse struct {
	Code int `json:"code"`
	Data struct {
		Song struct {
			List []qqSongPayload `json:"list"`
		} `json:"song"`
	} `json:"data"`
}

// qqSelfHostedSearchResponse 是 QQMusicApi 这类自建服务的返回，比官方接口浅一层。
type qqSelfHostedSearchResponse struct {
	Data struct {
		List []qqSongPayload `json:"list"`
	} `json:"data"`
}

func (s *qqSource) Search(ctx context.Context, f *musicFetcher, cfg musicConfig, query string) (song, bool) {
	if base := cfg.sourceOptions(s.Key()).APIBase; base != "" {
		var payload qqSelfHostedSearchResponse
		endpoint := fmt.Sprintf("%s/search?pageSize=5&key=%s", base, url.QueryEscape(query))
		if f.fetchJSON(ctx, cfg, endpoint, false, s.headers(cfg), &payload) {
			for _, entry := range payload.Data.List {
				if found, ok := entry.toSong(); ok {
					return found, true
				}
			}
		}
	}
	var payload qqSearchResponse
	if !f.fetchJSON(ctx, cfg, fmt.Sprintf(s.searchAPI, url.QueryEscape(query)), true, s.headers(cfg), &payload) {
		return song{}, false
	}
	for _, entry := range payload.Data.Song.List {
		if found, ok := entry.toSong(); ok {
			return found, true
		}
	}
	return song{}, false
}

// SongDetail 直接用搜索接口按 songmid 反查。
//
// QQ 音乐的详情接口要签名，而搜索接口拿 songmid 当关键词就能把那首歌搜出来，
// 省掉一整套签名逻辑。搜出来的第一条不是同一首时宁可判失败，也不发错歌。
func (s *qqSource) SongDetail(ctx context.Context, f *musicFetcher, cfg musicConfig, songID string) (song, bool) {
	found, ok := s.Search(ctx, f, cfg, songID)
	if !ok || found.ID != songID {
		return song{}, false
	}
	return found, true
}

type qqVkeyResponse struct {
	Req0 struct {
		Data struct {
			Sip        []string `json:"sip"`
			MidURLInfo []struct {
				Purl string `json:"purl"`
			} `json:"midurlinfo"`
		} `json:"data"`
	} `json:"req_0"`
}

type qqSelfHostedURLResponse struct {
	Data map[string]string `json:"data"`
}

// PlayableURL 换取播放地址。
//
// 没有登录态时 vkey 接口对会员和独家曲目会返回空 purl——那不是错误，是这首歌
// 在这家放不了。返回空串让上层换下一家，别把空地址当成可播放的往下走。
func (s *qqSource) PlayableURL(ctx context.Context, f *musicFetcher, cfg musicConfig, songID string) string {
	if base := cfg.sourceOptions(s.Key()).APIBase; base != "" {
		var payload qqSelfHostedURLResponse
		endpoint := fmt.Sprintf("%s/song/urls?id=%s", base, url.QueryEscape(songID))
		if f.fetchJSON(ctx, cfg, endpoint, false, s.headers(cfg), &payload) {
			for _, candidate := range payload.Data {
				if candidate = strings.TrimSpace(candidate); candidate != "" {
					return candidate
				}
			}
		}
	}
	request, err := qqVkeyRequest(songID, cfg.sourceOptions(s.Key()).Cookie)
	if err != nil {
		return ""
	}
	var payload qqVkeyResponse
	endpoint := fmt.Sprintf(s.vkeyAPI, url.QueryEscape(string(request)))
	if !f.fetchJSON(ctx, cfg, endpoint, true, s.headers(cfg), &payload) {
		return ""
	}
	for _, info := range payload.Req0.Data.MidURLInfo {
		purl := strings.TrimSpace(info.Purl)
		if purl == "" {
			continue
		}
		if strings.HasPrefix(purl, "http://") || strings.HasPrefix(purl, "https://") {
			return purl
		}
		base := s.streamCDN
		for _, sip := range payload.Req0.Data.Sip {
			if sip = strings.TrimSpace(sip); sip != "" {
				base = sip
				break
			}
		}
		return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(purl, "/")
	}
	return ""
}

// qqGuid 造一个稳定的设备号。接口要求非空，但不校验来源；用歌曲 ID 派生，
// 同一首歌每次拿到的是同一个值，便于对照日志排查。
func qqGuid(seed string) string {
	var sum uint32 = 2166136261
	for _, b := range []byte(seed) {
		sum = (sum ^ uint32(b)) * 16777619
	}
	return fmt.Sprintf("%010d", uint64(sum)%10000000000)
}
