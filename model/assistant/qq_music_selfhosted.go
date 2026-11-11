// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 自建 QQMusicApi 有两套互不兼容的接口，名字都叫 QQMusicApi：
//
//   - 老的 Node 版（jsososo/QQMusicApi）：/search?key=、/song/urls?id=，返回 data.list
//     和 {mid: url} 映射。
//   - L-1124/QQMusicApi（Python，FastAPI）：/search/search_by_type、/song/{mid}/url，
//     返回 {code, msg, data}；Cookie 认 musicid/musickey，还带 /login/refresh_credential
//     可以续期。
//
// 设置里只有一个地址，不让用户再选方言：两边对另一边的路径都回 404，先试一遍、
// 记住答得上的那套就够了。

type qqSelfHostedDialect int

const (
	qqDialectUnknown qqSelfHostedDialect = iota
	qqDialectLegacy
	qqDialectL1124
)

const (
	// 没有本地过期时间可看时，多久问一次 /login/check_expired。每次点歌都问一遍
	// 等于把延迟翻倍，凭据又是按天过期的。
	qqCredentialRecheckInterval = 30 * time.Minute
	// 本地看得到过期时间时提前这么久续期，免得刚检查完就在下一次请求里过期。
	qqCredentialRefreshMargin = 10 * time.Minute
)

func (s *qqSource) dialectOrder(base string) []qqSelfHostedDialect {
	if cached, ok := s.dialects.Load(base); ok {
		return []qqSelfHostedDialect{cached.(qqSelfHostedDialect)}
	}
	return []qqSelfHostedDialect{qqDialectLegacy, qqDialectL1124}
}

func (s *qqSource) cachedDialect(base string) qqSelfHostedDialect {
	if cached, ok := s.dialects.Load(base); ok {
		return cached.(qqSelfHostedDialect)
	}
	return qqDialectUnknown
}

// qqL1124Envelope 是 L-1124 版所有接口的外壳。code 为 0 才算成功。
type qqL1124Envelope[T any] struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data T      `json:"data"`
}

// searchSelfHosted 的 responded 表示这套接口答上了话（哪怕没搜到），用来认方言。
func (s *qqSource) searchSelfHosted(ctx context.Context, f *musicFetcher, cfg musicConfig, base string, dialect qqSelfHostedDialect, query string) (found song, ok, responded bool) {
	var songs []qqSongPayload
	switch dialect {
	case qqDialectLegacy:
		var payload qqSelfHostedSearchResponse
		endpoint := fmt.Sprintf("%s/search?pageSize=5&key=%s", base, url.QueryEscape(query))
		if !f.fetchJSON(ctx, cfg, endpoint, false, s.headers(cfg), &payload) {
			return song{}, false, false
		}
		songs = payload.Data.List
	case qqDialectL1124:
		// quick_search 是搜索框联想，歌手只给一个拼好的字符串、没有时长；按类型搜
		// 才有和官方接口同形的歌曲对象。
		var payload qqL1124Envelope[struct {
			Song []qqSongPayload `json:"song"`
		}]
		endpoint := fmt.Sprintf("%s/search/search_by_type?keyword=%s&search_type=0&num=5", base, url.QueryEscape(query))
		if !f.fetchJSON(ctx, cfg, endpoint, false, s.headers(cfg), &payload) || payload.Code != 0 {
			return song{}, false, false
		}
		songs = payload.Data.Song
	default:
		return song{}, false, false
	}
	for _, entry := range songs {
		if found, ok := entry.toSong(); ok {
			return found, true, true
		}
	}
	return song{}, false, true
}

func (s *qqSource) playableSelfHosted(ctx context.Context, f *musicFetcher, cfg musicConfig, base string, dialect qqSelfHostedDialect, songID string) (playURL string, responded bool) {
	switch dialect {
	case qqDialectLegacy:
		var payload qqSelfHostedURLResponse
		endpoint := fmt.Sprintf("%s/song/urls?id=%s", base, url.QueryEscape(songID))
		if !f.fetchJSON(ctx, cfg, endpoint, false, s.headers(cfg), &payload) {
			return "", false
		}
		for _, candidate := range payload.Data {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				return candidate, true
			}
		}
		return "", true
	case qqDialectL1124:
		// 不传 file_type：它是按服务端枚举顺序排的整数，换个版本就对不上号，
		// 默认值是 128k MP3，至少每个版本都认。
		var payload qqL1124Envelope[struct {
			Data []struct {
				Purl   string `json:"purl"`
				Result int    `json:"result"`
			} `json:"data"`
		}]
		endpoint := fmt.Sprintf("%s/song/%s/url", base, url.PathEscape(songID))
		if !f.fetchJSON(ctx, cfg, endpoint, false, s.headers(cfg), &payload) || payload.Code != 0 {
			return "", false
		}
		for _, info := range payload.Data.Data {
			purl := strings.TrimSpace(info.Purl)
			if purl == "" {
				continue
			}
			if strings.HasPrefix(purl, "http://") || strings.HasPrefix(purl, "https://") {
				return purl, true
			}
			return strings.TrimRight(s.streamCDN, "/") + "/" + strings.TrimLeft(purl, "/"), true
		}
		return "", true
	}
	return "", false
}

// qqRefreshableCookie 判断 Cookie 是不是 L-1124 版那种能续期的凭据：要有
// musicid、musickey，还要有 refresh_key 或 refresh_token 之一。官方网页复制的
// uin/qqmusic_key 没法续期，不碰。
func qqRefreshableCookie(raw string) bool {
	values := musicCookieValues(raw)
	if values["musicid"] == "" || values["musickey"] == "" {
		return false
	}
	return values["refresh_key"] != "" || values["refresh_token"] != ""
}

// freshCredentials 在请求自建接口前确认 Cookie 没过期，过期就续期并写回设置。
// 返回的 cfg 带着这次请求该用的 Cookie；err 非空表示凭据已过期且续期失败，
// cfg 里仍是旧 Cookie，调用方要把错误报出去，不能当作一切正常。
//
// 整段持锁：并发的几次点歌只能有一个去续期，refresh_token 用过一次就作废，
// 两个请求同时拿旧 token 去换，后到的那个一定失败。
func (s *qqSource) freshCredentials(ctx context.Context, f *musicFetcher, cfg musicConfig) (musicConfig, error) {
	options := cfg.sourceOptions(s.Key())
	if options.APIBase == "" || !qqRefreshableCookie(options.Cookie) || s.cachedDialect(options.APIBase) == qqDialectLegacy {
		return cfg, nil
	}
	cookie := options.Cookie
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	// 别的请求刚续过期：设置已经写回，但这次请求拿的还是写回前的快照。
	if s.refreshedFrom == cookie && s.refreshedTo != "" {
		return cfg.withSourceCookie(s.Key(), s.refreshedTo), nil
	}
	// 刚续失败过的同一份 Cookie 直接报同样的错：再拿作废的 token 去换也是失败，
	// 而「检查过、没过期」的缓存又会让它被当成好凭据悄悄用下去。
	if s.failedCookie == cookie && time.Since(s.failedAt) < qqCredentialRecheckInterval {
		recordMusicCredentialProblem(ctx, s.failedErr.Error())
		return cfg, s.failedErr
	}
	if !s.credentialExpired(ctx, f, cfg, options.APIBase, cookie) {
		return cfg, nil
	}
	fresh, err := s.refreshCredential(ctx, f, cfg, options.APIBase, cookie)
	if err != nil {
		log.Printf("music qq credential refresh failed: %v", err)
		problem := fmt.Errorf("QQ 音乐 Cookie 已过期，自建接口续期失败（%v），请重新登录 QQ 音乐后复制 Cookie", err)
		s.failedCookie, s.failedAt, s.failedErr = cookie, time.Now(), problem
		recordMusicFailure(ctx, "%v", problem)
		recordMusicCredentialProblem(ctx, problem.Error())
		return cfg, problem
	}
	s.refreshedFrom, s.refreshedTo = cookie, fresh
	s.checkedCookie, s.checkedAt = fresh, time.Now()
	if f != nil && f.saveCookie != nil {
		if err := f.saveCookie(ctx, s.Key(), fresh); err != nil {
			// 这次请求照样用新 Cookie；只是重启后会回到旧的，再续一次。
			log.Printf("music qq refreshed cookie not saved: %v", err)
		}
	}
	return cfg.withSourceCookie(s.Key(), fresh), nil
}

// credentialExpired 先看 Cookie 自带的过期时间，看不到再问自建接口。
// 问不出结果（网络错、接口自己失败）按没过期处理：宁可这次不续期，
// 也不拿可能还有效的 refresh_token 去换。
func (s *qqSource) credentialExpired(ctx context.Context, f *musicFetcher, cfg musicConfig, base, cookie string) bool {
	values := musicCookieValues(cookie)
	created, _ := strconv.ParseInt(values["musickey_create_time"], 10, 64)
	lifetime, _ := strconv.ParseInt(values["key_expires_in"], 10, 64)
	if created > 0 && lifetime > 0 {
		deadline := time.Unix(created+lifetime, 0).Add(-qqCredentialRefreshMargin)
		return !time.Now().Before(deadline)
	}
	if s.checkedCookie == cookie && time.Since(s.checkedAt) < qqCredentialRecheckInterval {
		return false
	}
	s.checkedCookie, s.checkedAt = cookie, time.Now()
	var payload struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if !f.fetchJSON(ctx, cfg, base+"/login/check_expired", false, s.headers(cfg), &payload) {
		return false
	}
	// 0.8.2 的接口成功时统一回 code 0，是否过期在 data.expired：true 已过期、
	// false 未过期。旧版曾把「已过期」包成 code 0 + data null，保留这条兜底；
	// 接口自己失败（code != 0）时不能当成过期，否则会拿还有效的 refresh_token
	// 去换，把一份好凭据换没。
	if payload.Code != 0 {
		return false
	}
	if data := strings.TrimSpace(string(payload.Data)); data != "" && data != "null" {
		var check struct {
			Expired *bool `json:"expired"`
		}
		if err := json.Unmarshal(payload.Data, &check); err == nil && check.Expired != nil {
			return *check.Expired
		}
		var expired bool
		if err := json.Unmarshal(payload.Data, &expired); err == nil {
			return expired
		}
	}
	return true
}

// qqSelfHostedCredential 是 /login/refresh_credential 返回的 Credential。Web 层按
// 字段名序列化（snake_case），驼峰别名一并接住，免得换个序列化选项就解不出来。
type qqSelfHostedCredential struct {
	MusicID            int64  `json:"musicid"`
	MusicKey           string `json:"musickey"`
	StrMusicID         string `json:"str_musicid"`
	RefreshToken       string `json:"refresh_token"`
	RefreshKey         string `json:"refresh_key"`
	AccessToken        string `json:"access_token"`
	OpenID             string `json:"openid"`
	UnionID            string `json:"unionid"`
	ExpiredAt          int64  `json:"expired_at"`
	MusickeyCreateTime int64  `json:"musickey_create_time"`
	KeyExpiresIn       int64  `json:"key_expires_in"`
	CamelCreateTime    int64  `json:"musickeyCreateTime"`
	CamelExpiresIn     int64  `json:"keyExpiresIn"`
}

func (s *qqSource) refreshCredential(ctx context.Context, f *musicFetcher, cfg musicConfig, base, cookie string) (string, error) {
	ctx, diagnostics := withMusicDiagnostics(ctx)
	var payload qqL1124Envelope[*qqSelfHostedCredential]
	if !f.fetchJSON(ctx, cfg, base+"/login/refresh_credential", false, s.headers(cfg), &payload) {
		if diagnostics.failed() {
			return "", errors.New(diagnostics.failures[len(diagnostics.failures)-1])
		}
		return "", errors.New("请求续期接口失败")
	}
	if payload.Code != 0 {
		return "", fmt.Errorf("续期接口返回 code %d：%s", payload.Code, strings.TrimSpace(payload.Msg))
	}
	credential := payload.Data
	if credential == nil || credential.MusicID <= 0 || strings.TrimSpace(credential.MusicKey) == "" {
		return "", errors.New("续期接口没有返回新的 musicid/musickey")
	}
	return qqMergeRefreshedCookie(cookie, *credential), nil
}

// qqMergeRefreshedCookie 把新凭据写进原 Cookie：已有的键原地替换、保留原来的
// 顺序和大小写，没有的追加在后面，和续期无关的键原样留着。用户顺手贴进来的
// 官方 uin/qqmusic_key 也一起换掉，不然两套登录态各说各的。
func qqMergeRefreshedCookie(raw string, credential qqSelfHostedCredential) string {
	musicID := strconv.FormatInt(credential.MusicID, 10)
	updates := map[string]string{"musicid": musicID, "musickey": strings.TrimSpace(credential.MusicKey)}
	order := []string{"musicid", "musickey"}
	add := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" && value != "0" {
			updates[key] = value
			order = append(order, key)
		}
	}
	add("str_musicid", credential.StrMusicID)
	add("refresh_token", credential.RefreshToken)
	add("refresh_key", credential.RefreshKey)
	add("access_token", credential.AccessToken)
	add("openid", credential.OpenID)
	add("unionid", credential.UnionID)
	add("expired_at", strconv.FormatInt(credential.ExpiredAt, 10))
	add("musickey_create_time", strconv.FormatInt(max(credential.MusickeyCreateTime, credential.CamelCreateTime), 10))
	add("key_expires_in", strconv.FormatInt(max(credential.KeyExpiresIn, credential.CamelExpiresIn), 10))
	// 官方键只在原 Cookie 里就有时才换，不凭空加：headers 会按 musicid/musickey 补。
	uin := firstNonEmpty(updates["str_musicid"], musicID)
	officialKeys := map[string]string{"uin": uin, "qqmusic_uin": uin, "qqmusic_key": updates["musickey"], "qm_keyst": updates["musickey"]}

	parts := make([]string, 0, len(updates)+4)
	written := map[string]bool{}
	for _, part := range strings.Split(raw, ";") {
		key, _, ok := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		lower := strings.ToLower(key)
		value, isUpdate := updates[lower]
		if !isUpdate {
			value, isUpdate = officialKeys[lower]
		}
		if isUpdate {
			parts = append(parts, key+"="+value)
			written[lower] = true
			continue
		}
		parts = append(parts, strings.TrimSpace(part))
	}
	for _, key := range order {
		if !written[key] {
			parts = append(parts, key+"="+updates[key])
		}
	}
	return strings.Join(parts, "; ")
}

// qqOfficialCookie 给只有 musicid/musickey 的 Cookie 补上官方网页认的键。
// musickey 就是网页 Cookie 里的 qqmusic_key/qm_keyst，musicid 就是 uin——
// QQMusicApi 自己请求 y.qq.com 时也是这么换的。不补的话 musicu 接口把这份
// 好好的登录态当游客，凭据检查也会误判成失效。
func qqOfficialCookie(raw string) string {
	values := musicCookieValues(raw)
	musicID, musicKey := values["musicid"], values["musickey"]
	if musicID == "" || musicKey == "" {
		return raw
	}
	uin := firstNonEmpty(values["str_musicid"], musicID)
	extra := make([]string, 0, 4)
	for _, pair := range [][2]string{{"uin", uin}, {"qqmusic_uin", uin}, {"qqmusic_key", musicKey}, {"qm_keyst", musicKey}} {
		if values[pair[0]] == "" {
			extra = append(extra, pair[0]+"="+pair[1])
		}
	}
	if len(extra) == 0 {
		return raw
	}
	return strings.TrimRight(strings.TrimSpace(raw), "; ") + "; " + strings.Join(extra, "; ")
}
