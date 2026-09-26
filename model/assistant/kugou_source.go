// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// 酷狗音乐曲库。公共层见 music_source.go。
//
// 酷狗的歌曲标识是音频文件的 hash。ID 是「hash:album_id」的组合串：官方接口
// 只认 hash，album_id 留给自建 KuGouMusicApi 和老分享链接。ID 对公共层是不透明
// 的，只有这个文件需要知道它是两截。
//
// 网页版 www.kugou.com 的 play/getdata 和 wwwapi 的 play/songinfo 从 2026 年 9 月起
// 对所有歌曲返回 err_code 30020，现在走手机版的 getSongInfo：免费歌直接给地址，
// 付费歌 url 为空、error 是「需要付费」，歌名歌手照样给，链接解析不受影响。

const (
	kugouSearchAPI = "https://songsearch.kugou.com/song_search_v2?platform=WebFilter&page=1&pagesize=5&keyword=%s"
	kugouPlayAPI   = "https://m.kugou.com/app/i/getSongInfo.php?cmd=playInfo&hash=%s"
	kugouReferer   = "https://www.kugou.com/"
)

var (
	kugouHosts = []string{"kugou.com"}
	// 分享出来的地址把 hash 放在查询串或锚点里：/song/#hash=xxx&album_id=yyy。
	kugouHashPattern = regexp.MustCompile(`(?i)(?:^|[?&#])hash=([0-9a-f]{32})`)
	kugouAlbumID     = regexp.MustCompile(`(?i)(?:^|[?&#])album_id=(\d+)`)
)

type kugouSource struct {
	searchAPI string
	playAPI   string
}

func newKugouSource() *kugouSource {
	return &kugouSource{searchAPI: kugouSearchAPI, playAPI: kugouPlayAPI}
}

func (s *kugouSource) Key() string     { return "kugou" }
func (s *kugouSource) Label() string   { return "酷狗音乐" }
func (s *kugouSource) Referer() string { return kugouReferer }

func (s *kugouSource) References(text string) []musicReference {
	out := make([]musicReference, 0, 2)
	seen := map[string]bool{}
	for _, raw := range extractURLs(text) {
		parsed, err := url.Parse(raw)
		if err != nil || !hostMatchesDomain(parsed.Hostname(), kugouHosts...) {
			continue
		}
		if id := kugouSongIDFromURL(raw); id != "" && !seen[id] {
			seen[id] = true
			out = append(out, musicReference{Source: s.Key(), SongID: id})
			continue
		}
		// 酷狗的分享短链把 hash 藏在跳转之后，先留着链接本体等 ResolveSongID 去跟。
		if !seen["short:"+raw] {
			seen["short:"+raw] = true
			out = append(out, musicReference{Source: s.Key(), ShortURL: raw})
		}
	}
	return out
}

// kugouSongIDFromURL 从整条地址里取 hash 和 album_id。
//
// 直接在原始串上匹配而不是先解析成 URL：酷狗把参数放在 # 后面，标准解析会把
// 整段当成 fragment，再拆一次反而绕远。
func kugouSongIDFromURL(raw string) string {
	match := kugouHashPattern.FindStringSubmatch(raw)
	if len(match) != 2 {
		return ""
	}
	albumID := ""
	if album := kugouAlbumID.FindStringSubmatch(raw); len(album) == 2 {
		albumID = album[1]
	}
	return strings.ToLower(match[1]) + ":" + albumID
}

func kugouSplitSongID(songID string) (hash string, albumID string) {
	hash, albumID, _ = strings.Cut(strings.TrimSpace(songID), ":")
	return strings.ToLower(strings.TrimSpace(hash)), strings.TrimSpace(albumID)
}

func (s *kugouSource) headers(cfg musicConfig) map[string]string {
	headers := map[string]string{"Referer": kugouReferer, "Accept": "application/json, text/plain, */*"}
	if cookie := cfg.sourceOptions(s.Key()).Cookie; cookie != "" {
		headers["Cookie"] = cookie
		// KuGouMusicApi 支持用 Authorization 传 token/userid/dfid，避免把
		// 凭据放进查询串。官方接口忽略这个头，不影响无自建服务的请求。
		headers["Authorization"] = cookie
	}
	return headers
}

func (s *kugouSource) ResolveSongID(ctx context.Context, f *musicFetcher, cfg musicConfig, ref musicReference) string {
	if ref.SongID != "" {
		return ref.SongID
	}
	final := f.finalURL(ctx, cfg, ref.ShortURL, nil)
	if final == "" {
		return ""
	}
	parsed, err := url.Parse(final)
	if err != nil || !hostMatchesDomain(parsed.Hostname(), kugouHosts...) {
		return ""
	}
	return kugouSongIDFromURL(final)
}

type kugouSearchResponse struct {
	Status int `json:"status"`
	Data   struct {
		// Info 是旧 mobilecdn 接口和自建兼容接口的结构；Lists 是当前
		// songsearch 接口的结构。保留两套是为了不破坏用户已有的 APIBase。
		Info  []kugouLegacySearchEntry `json:"info"`
		Lists []kugouSearchEntry       `json:"lists"`
	} `json:"data"`
}

type kugouLegacySearchEntry struct {
	Hash       string `json:"hash"`
	SongName   string `json:"songname"`
	SingerName string `json:"singername"`
	AlbumName  string `json:"album_name"`
	AlbumID    string `json:"album_id"`
	Duration   int64  `json:"duration"`
}

type kugouSearchEntry struct {
	Hash       string `json:"FileHash"`
	SongName   string `json:"SongName"`
	SingerName string `json:"SingerName"`
	AlbumName  string `json:"AlbumName"`
	AlbumID    string `json:"AlbumID"`
	Duration   int64  `json:"Duration"`
}

func (r kugouSearchResponse) entries() []kugouSearchEntry {
	entries := append([]kugouSearchEntry(nil), r.Data.Lists...)
	for _, legacy := range r.Data.Info {
		entries = append(entries, kugouSearchEntry{
			Hash:       legacy.Hash,
			SongName:   legacy.SongName,
			SingerName: legacy.SingerName,
			AlbumName:  legacy.AlbumName,
			AlbumID:    legacy.AlbumID,
			Duration:   legacy.Duration,
		})
	}
	return entries
}

// kugouPlayResponse 是自建 KuGouMusicApi /song/url 的返回，沿用老网页接口的字段。
type kugouPlayResponse struct {
	Status int `json:"status"`
	Data   struct {
		PlayURL    string `json:"play_url"`
		AudioName  string `json:"audio_name"`
		SongName   string `json:"song_name"`
		AuthorName string `json:"author_name"`
		AlbumName  string `json:"album_name"`
		TimeLength int64  `json:"timelength"`
	} `json:"data"`
}

// kugouSongInfoResponse 是官方手机版 getSongInfo 的返回。timeLength 是秒，
// 和自建接口的毫秒不同。
type kugouSongInfoResponse struct {
	Status     int    `json:"status"`
	SongName   string `json:"songName"`
	SingerName string `json:"singerName"`
	FileName   string `json:"fileName"`
	TimeLength int64  `json:"timeLength"`
	URL        string `json:"url"`
}

// kugouTrack 把两种返回收成一份：歌曲信息和播放地址（可能为空）。
type kugouTrack struct {
	Name     string
	Artist   string
	Album    string
	Duration time.Duration
	PlayURL  string
}

func (s *kugouSource) Search(ctx context.Context, f *musicFetcher, cfg musicConfig, query string) (song, bool) {
	endpoint := fmt.Sprintf(s.searchAPI, url.QueryEscape(query))
	guarded := true
	if base := cfg.sourceOptions(s.Key()).APIBase; base != "" {
		endpoint = fmt.Sprintf("%s/search?keyword=%s&pagesize=5", base, url.QueryEscape(query))
		guarded = false
	}
	var payload kugouSearchResponse
	if !f.fetchJSON(ctx, cfg, endpoint, guarded, s.headers(cfg), &payload) {
		return song{}, false
	}
	for _, entry := range payload.entries() {
		hash := strings.ToLower(strings.TrimSpace(entry.Hash))
		if hash == "" {
			continue
		}
		names := []string{}
		if singer := strings.TrimSpace(entry.SingerName); singer != "" {
			names = append(names, singer)
		}
		// 搜索结果的 duration 是秒，播放接口的 timelength 是毫秒，别混用。
		found, ok := newSong(s.Key(), hash+":"+strings.TrimSpace(entry.AlbumID),
			entry.SongName, names, entry.AlbumName, time.Duration(entry.Duration)*time.Second)
		if ok {
			return found, true
		}
	}
	return song{}, false
}

// SongDetail 和 PlayableURL 打的是同一个接口：酷狗的播放接口一次就把歌名、
// 歌手、时长和播放地址全给了，没必要为详情单独跑一趟。
func (s *kugouSource) SongDetail(ctx context.Context, f *musicFetcher, cfg musicConfig, songID string) (song, bool) {
	track, ok := s.track(ctx, f, cfg, songID)
	if !ok {
		return song{}, false
	}
	names := []string{}
	if track.Artist != "" {
		names = append(names, track.Artist)
	}
	return newSong(s.Key(), songID, track.Name, names, track.Album, track.Duration)
}

func (s *kugouSource) PlayableURL(ctx context.Context, f *musicFetcher, cfg musicConfig, songID string) string {
	track, ok := s.track(ctx, f, cfg, songID)
	if !ok || track.PlayURL == "" || !musicLinkLooksPlayable(track.PlayURL) {
		return ""
	}
	return track.PlayURL
}

func (s *kugouSource) track(ctx context.Context, f *musicFetcher, cfg musicConfig, songID string) (kugouTrack, bool) {
	hash, albumID := kugouSplitSongID(songID)
	if hash == "" {
		return kugouTrack{}, false
	}
	if base := cfg.sourceOptions(s.Key()).APIBase; base != "" {
		endpoint := fmt.Sprintf("%s/song/url?hash=%s&album_id=%s", base, hash, url.QueryEscape(albumID))
		var payload kugouPlayResponse
		if !f.fetchJSON(ctx, cfg, endpoint, false, s.headers(cfg), &payload) {
			return kugouTrack{}, false
		}
		return kugouTrack{
			Name:     firstNonEmpty(strings.TrimSpace(payload.Data.SongName), strings.TrimSpace(payload.Data.AudioName)),
			Artist:   strings.TrimSpace(payload.Data.AuthorName),
			Album:    strings.TrimSpace(payload.Data.AlbumName),
			Duration: time.Duration(payload.Data.TimeLength) * time.Millisecond,
			PlayURL:  strings.TrimSpace(payload.Data.PlayURL),
		}, true
	}
	var payload kugouSongInfoResponse
	if !f.fetchJSON(ctx, cfg, fmt.Sprintf(s.playAPI, hash), true, s.headers(cfg), &payload) {
		return kugouTrack{}, false
	}
	return kugouTrack{
		Name:     firstNonEmpty(strings.TrimSpace(payload.SongName), strings.TrimSpace(payload.FileName)),
		Artist:   strings.TrimSpace(payload.SingerName),
		Duration: time.Duration(payload.TimeLength) * time.Second,
		PlayURL:  strings.TrimSpace(payload.URL),
	}, true
}
