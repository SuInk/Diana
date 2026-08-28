// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// 酷狗音乐曲库。公共层见 music_source.go。
//
// 酷狗的歌曲标识是音频文件的 hash，播放接口还要一个 album_id 才给全曲地址，
// 所以这里的 ID 是「hash:album_id」的组合串。ID 对公共层是不透明的，
// 只有这个文件需要知道它是两截。

const (
	kugouSearchAPI = "https://mobilecdn.kugou.com/api/v3/search/song?format=json&showtype=1&page=1&pagesize=5&keyword=%s"
	kugouPlayAPI   = "https://www.kugou.com/yy/index.php?r=play/getdata&hash=%s&album_id=%s&mid=%s"
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
	// 播放接口认一个设备号 cookie，值本身不校验来源，缺了才会被拒。
	// 用户填了自己的 Cookie 就用他的，没填就派生一个稳定值。
	if cookie := cfg.sourceOptions(s.Key()).Cookie; cookie != "" {
		headers["Cookie"] = cookie
	}
	return headers
}

// kugouDeviceID 由歌曲标识派生出一个稳定的 32 位设备号，同一首歌每次一致，
// 便于对照日志排查。
func kugouDeviceID(seed string) string {
	sum := md5.Sum([]byte("diana-kugou:" + seed))
	return hex.EncodeToString(sum[:])
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
		Info []struct {
			Hash       string `json:"hash"`
			SongName   string `json:"songname"`
			SingerName string `json:"singername"`
			AlbumName  string `json:"album_name"`
			AlbumID    string `json:"album_id"`
			Duration   int64  `json:"duration"`
		} `json:"info"`
	} `json:"data"`
}

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
	for _, entry := range payload.Data.Info {
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
	payload, ok := s.playData(ctx, f, cfg, songID)
	if !ok {
		return song{}, false
	}
	names := []string{}
	if author := strings.TrimSpace(payload.Data.AuthorName); author != "" {
		names = append(names, author)
	}
	name := firstNonEmpty(strings.TrimSpace(payload.Data.SongName), strings.TrimSpace(payload.Data.AudioName))
	return newSong(s.Key(), songID, name, names, payload.Data.AlbumName,
		time.Duration(payload.Data.TimeLength)*time.Millisecond)
}

func (s *kugouSource) PlayableURL(ctx context.Context, f *musicFetcher, cfg musicConfig, songID string) string {
	payload, ok := s.playData(ctx, f, cfg, songID)
	if !ok {
		return ""
	}
	candidate := strings.TrimSpace(payload.Data.PlayURL)
	if candidate == "" || !musicLinkLooksPlayable(candidate) {
		return ""
	}
	return candidate
}

func (s *kugouSource) playData(ctx context.Context, f *musicFetcher, cfg musicConfig, songID string) (kugouPlayResponse, bool) {
	hash, albumID := kugouSplitSongID(songID)
	if hash == "" {
		return kugouPlayResponse{}, false
	}
	endpoint := fmt.Sprintf(s.playAPI, hash, url.QueryEscape(albumID), kugouDeviceID(hash))
	guarded := true
	if base := cfg.sourceOptions(s.Key()).APIBase; base != "" {
		endpoint = fmt.Sprintf("%s/song/url?hash=%s&album_id=%s", base, hash, url.QueryEscape(albumID))
		guarded = false
	}
	var payload kugouPlayResponse
	if !f.fetchJSON(ctx, cfg, endpoint, guarded, s.headers(cfg), &payload) {
		return kugouPlayResponse{}, false
	}
	return payload, true
}
