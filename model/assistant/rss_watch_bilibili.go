// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 哔哩哔哩的 Web 接口都在风控后面：匿名请求会直接吃 -352。内置抓取的做法是
// 先向官方指纹接口领一个 buvid，再按官方 wbi 规则给请求签名，这样不登录也能
// 稳定读到 UP 主投稿；配置了登录 Cookie 时改读完整动态（图文、转发、直播）。
const (
	bilibiliFingerprintURL = "https://api.bilibili.com/x/frontend/finger/spi"
	bilibiliNavURL         = "https://api.bilibili.com/x/web-interface/nav"
	bilibiliVideoSearchURL = "https://api.bilibili.com/x/space/wbi/arc/search"
	bilibiliDynamicURL     = "https://api.bilibili.com/x/polymer/web-dynamic/v1/feed/space"

	bilibiliBuvidTTL = 6 * time.Hour
	bilibiliWbiTTL   = time.Hour
	bilibiliPageSize = 30
)

// bilibiliMixinKeyTab 是官方前端写死的 wbi 混淆密钥重排表。
var bilibiliMixinKeyTab = [...]int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35,
	27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13,
	37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4,
	22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52,
}

type bilibiliAccessCache struct {
	mu       sync.Mutex
	buvid    string
	buvidAt  time.Time
	mixinKey string
	mixinAt  time.Time
}

func (p *RSSWatchPlugin) fetchBilibiliSpace(ctx context.Context, uid string, settings SettingValues) (parsedFeed, error) {
	cookie := strings.TrimSpace(settings.String(rssWatchSettingBilibiliCookie, ""))
	if cookie != "" {
		feed, err := p.fetchBilibiliDynamics(ctx, uid, cookie, settings)
		if err == nil {
			return feed, nil
		}
		// 登录态失效或被风控时不能直接失败：投稿列表匿名也读得到，先保住订阅。
		if feed, videoErr := p.fetchBilibiliVideos(ctx, uid, cookie, settings); videoErr == nil {
			return feed, nil
		}
		return parsedFeed{}, err
	}
	return p.fetchBilibiliVideos(ctx, uid, "", settings)
}

func (p *RSSWatchPlugin) fetchBilibiliVideos(ctx context.Context, uid, cookie string, settings SettingValues) (parsedFeed, error) {
	query := url.Values{}
	query.Set("mid", uid)
	query.Set("ps", strconv.Itoa(bilibiliPageSize))
	query.Set("pn", "1")
	query.Set("tid", "0")
	query.Set("keyword", "")
	query.Set("order", "pubdate")
	query.Set("order_avoided", "true")
	query.Set("platform", "web")
	query.Set("web_location", "1550101")
	signed, err := p.signBilibiliQuery(ctx, query, cookie, settings)
	if err != nil {
		return parsedFeed{}, err
	}
	body, err := p.getBilibili(ctx, bilibiliVideoSearchURL+"?"+signed, uid, cookie, settings)
	if err != nil {
		return parsedFeed{}, err
	}
	return parseBilibiliVideos(body, uid)
}

func parseBilibiliVideos(body []byte, uid string) (parsedFeed, error) {
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			List struct {
				VList []struct {
					BVID        string `json:"bvid"`
					Title       string `json:"title"`
					Description string `json:"description"`
					Author      string `json:"author"`
					Created     int64  `json:"created"`
					Length      string `json:"length"`
				} `json:"vlist"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return parsedFeed{}, fmt.Errorf("解析哔哩哔哩投稿列表失败: %w", err)
	}
	if payload.Code != 0 {
		return parsedFeed{}, bilibiliAPIError("读取哔哩哔哩投稿列表", payload.Code, payload.Message)
	}
	feed := parsedFeed{Title: "哔哩哔哩 UID " + uid}
	for _, entry := range payload.Data.List.VList {
		if strings.TrimSpace(feed.Title) == "哔哩哔哩 UID "+uid && strings.TrimSpace(entry.Author) != "" {
			feed.Title = cleanFeedText(entry.Author, 200) + " 的投稿"
		}
		item := rssWatchItem{
			ID:          "bvid:" + entry.BVID,
			Title:       cleanFeedText(entry.Title, 500),
			Link:        "https://www.bilibili.com/video/" + entry.BVID,
			Author:      cleanFeedText(entry.Author, 200),
			Content:     cleanFeedText(strings.TrimSpace(entry.Description+" "+bilibiliDurationText(entry.Length)), 4000),
			PublishedAt: unixFeedTime(entry.Created),
		}
		item.ID = stableFeedItemID(item)
		feed.Items = append(feed.Items, item)
	}
	sortFeedItemsByPublishedAt(feed.Items)
	return feed, nil
}

func (p *RSSWatchPlugin) fetchBilibiliDynamics(ctx context.Context, uid, cookie string, settings SettingValues) (parsedFeed, error) {
	// 官方前端在没有 WebGL 时就是这么填的：base64("no webgl") 去掉末尾两位。
	encoded := base64.StdEncoding.EncodeToString([]byte("no webgl"))
	noWebGL := encoded[:len(encoded)-2]
	query := url.Values{}
	query.Set("offset", "")
	query.Set("host_mid", uid)
	query.Set("platform", "web")
	query.Set("features", "itemOpusStyle,listOnlyfans,opusBigCover,onlyfansVote")
	query.Set("dm_img_list", `[{"x":6218,"y":-1445,"z":0,"timestamp":31,"type":0}]`)
	query.Set("dm_img_str", noWebGL)
	query.Set("dm_cover_img_str", noWebGL)
	body, err := p.getBilibili(ctx, bilibiliDynamicURL+"?"+query.Encode(), uid, cookie, settings)
	if err != nil {
		return parsedFeed{}, err
	}
	return parseBilibiliDynamics(body, uid)
}

func parseBilibiliDynamics(body []byte, uid string) (parsedFeed, error) {
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Items []bilibiliDynamicItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return parsedFeed{}, fmt.Errorf("解析哔哩哔哩动态失败: %w", err)
	}
	if payload.Code != 0 {
		return parsedFeed{}, bilibiliAPIError("读取哔哩哔哩动态", payload.Code, payload.Message)
	}
	feed := parsedFeed{Title: "哔哩哔哩 UID " + uid}
	for _, entry := range payload.Data.Items {
		author := strings.TrimSpace(entry.Modules.Author.Name)
		if author != "" && feed.Title == "哔哩哔哩 UID "+uid {
			feed.Title = cleanFeedText(author, 200) + " 的动态"
		}
		title, content := entry.text()
		item := rssWatchItem{
			ID:          "dynamic:" + entry.IDStr,
			Title:       cleanFeedText(title, 500),
			Author:      cleanFeedText(author, 200),
			Content:     cleanFeedText(content, 4000),
			PublishedAt: unixFeedTime(entry.Modules.Author.PubTS),
		}
		if entry.IDStr != "" {
			item.Link = "https://t.bilibili.com/" + entry.IDStr
		}
		if bvid := strings.TrimSpace(entry.Modules.Dynamic.Major.Archive.BVID); bvid != "" {
			item.Link = "https://www.bilibili.com/video/" + bvid
		}
		item.ID = stableFeedItemID(item)
		feed.Items = append(feed.Items, item)
	}
	sortFeedItemsByPublishedAt(feed.Items)
	return feed, nil
}

type bilibiliDynamicItem struct {
	IDStr   string `json:"id_str"`
	Type    string `json:"type"`
	Modules struct {
		Author struct {
			Name  string `json:"name"`
			PubTS int64  `json:"pub_ts"`
		} `json:"module_author"`
		Dynamic struct {
			Desc struct {
				Text string `json:"text"`
			} `json:"desc"`
			Major struct {
				Type    string `json:"type"`
				Archive struct {
					BVID  string `json:"bvid"`
					Title string `json:"title"`
					Desc  string `json:"desc"`
				} `json:"archive"`
				Opus struct {
					Title   string `json:"title"`
					Summary struct {
						Text string `json:"text"`
					} `json:"summary"`
				} `json:"opus"`
				Article struct {
					Title string `json:"title"`
					Desc  string `json:"desc"`
				} `json:"article"`
				Common struct {
					Title string `json:"title"`
					Desc  string `json:"desc"`
				} `json:"common"`
				Live struct {
					Title string `json:"title"`
					Desc  string `json:"desc_first"`
				} `json:"live"`
				None struct {
					Tips string `json:"tips"`
				} `json:"none"`
			} `json:"major"`
		} `json:"module_dynamic"`
	} `json:"modules"`
	Orig *bilibiliDynamicItem `json:"orig"`
}

// text 汇总一条动态的标题与正文，转发动态带上被转发内容。
func (item bilibiliDynamicItem) text() (string, string) {
	major := item.Modules.Dynamic.Major
	title := firstNonEmpty(major.Archive.Title, major.Opus.Title, major.Article.Title, major.Live.Title, major.Common.Title, major.None.Tips)
	content := firstNonEmpty(item.Modules.Dynamic.Desc.Text, major.Opus.Summary.Text, major.Archive.Desc, major.Article.Desc, major.Live.Desc, major.Common.Desc)
	if item.Orig != nil {
		originTitle, originContent := item.Orig.text()
		if origin := strings.TrimSpace(originTitle + " " + originContent); origin != "" {
			content = strings.TrimSpace(content + " //转发自 " + strings.TrimSpace(item.Orig.Modules.Author.Name) + "：" + origin)
		}
	}
	if title == "" {
		title = content
	}
	return title, content
}

func (p *RSSWatchPlugin) getBilibili(ctx context.Context, rawURL, uid, cookie string, settings SettingValues) ([]byte, error) {
	buvid, err := p.bilibiliBuvid(ctx, settings)
	if err != nil {
		return nil, err
	}
	// 带 Origin 会被当成跨站调用直接掐断，只留 Referer。
	return p.get(ctx, rawURL, settings, map[string]string{
		"User-Agent": rssWatchBrowserUserAgent,
		"Referer":    "https://space.bilibili.com/" + uid + "/video",
		"Cookie":     joinCookies(buvid, cookie),
	})
}

// bilibiliBuvid 领取并缓存匿名访问需要的设备指纹 Cookie。
func (p *RSSWatchPlugin) bilibiliBuvid(ctx context.Context, settings SettingValues) (string, error) {
	if p.bilibili == nil {
		p.bilibili = &bilibiliAccessCache{}
	}
	p.bilibili.mu.Lock()
	defer p.bilibili.mu.Unlock()
	if p.bilibili.buvid != "" && time.Since(p.bilibili.buvidAt) < bilibiliBuvidTTL {
		return p.bilibili.buvid, nil
	}
	body, err := p.get(ctx, bilibiliFingerprintURL, settings, map[string]string{"User-Agent": rssWatchBrowserUserAgent})
	if err != nil {
		return "", fmt.Errorf("获取哔哩哔哩设备指纹失败: %w", err)
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			B3 string `json:"b_3"`
			B4 string `json:"b_4"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Code != 0 || payload.Data.B3 == "" {
		return "", fmt.Errorf("哔哩哔哩设备指纹接口返回异常")
	}
	p.bilibili.buvid = "buvid3=" + payload.Data.B3 + "; buvid4=" + payload.Data.B4
	p.bilibili.buvidAt = time.Now()
	return p.bilibili.buvid, nil
}

// signBilibiliQuery 按官方 wbi 规则给查询串补上 wts 与 w_rid。
func (p *RSSWatchPlugin) signBilibiliQuery(ctx context.Context, query url.Values, cookie string, settings SettingValues) (string, error) {
	mixinKey, err := p.bilibiliMixinKey(ctx, cookie, settings)
	if err != nil {
		return "", err
	}
	query.Set("wts", strconv.FormatInt(time.Now().Unix(), 10))
	encoded := query.Encode()
	sum := md5.Sum([]byte(encoded + mixinKey))
	return encoded + "&w_rid=" + hex.EncodeToString(sum[:]), nil
}

func (p *RSSWatchPlugin) bilibiliMixinKey(ctx context.Context, cookie string, settings SettingValues) (string, error) {
	if p.bilibili == nil {
		p.bilibili = &bilibiliAccessCache{}
	}
	p.bilibili.mu.Lock()
	defer p.bilibili.mu.Unlock()
	if p.bilibili.mixinKey != "" && time.Since(p.bilibili.mixinAt) < bilibiliWbiTTL {
		return p.bilibili.mixinKey, nil
	}
	body, err := p.get(ctx, bilibiliNavURL, settings, map[string]string{
		"User-Agent": rssWatchBrowserUserAgent,
		"Referer":    "https://www.bilibili.com/",
		"Cookie":     cookie,
	})
	if err != nil {
		return "", fmt.Errorf("获取哔哩哔哩签名密钥失败: %w", err)
	}
	var payload struct {
		Data struct {
			WbiImg struct {
				ImgURL string `json:"img_url"`
				SubURL string `json:"sub_url"`
			} `json:"wbi_img"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("解析哔哩哔哩签名密钥失败: %w", err)
	}
	key := bilibiliMixinKey(bilibiliKeyFromURL(payload.Data.WbiImg.ImgURL) + bilibiliKeyFromURL(payload.Data.WbiImg.SubURL))
	if key == "" {
		return "", fmt.Errorf("哔哩哔哩签名密钥为空，接口可能已调整")
	}
	p.bilibili.mixinKey, p.bilibili.mixinAt = key, time.Now()
	return key, nil
}

func bilibiliKeyFromURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	name := rawURL[strings.LastIndex(rawURL, "/")+1:]
	if index := strings.Index(name, "."); index >= 0 {
		name = name[:index]
	}
	return name
}

func bilibiliMixinKey(raw string) string {
	if len(raw) < 64 {
		return ""
	}
	var builder strings.Builder
	for _, index := range bilibiliMixinKeyTab {
		builder.WriteByte(raw[index])
	}
	return builder.String()[:32]
}

func bilibiliAPIError(action string, code int, message string) error {
	message = strings.TrimSpace(message)
	switch code {
	case -352, -412:
		return fmt.Errorf("%s被哔哩哔哩风控拦截（code %d）：可在插件设置里填写登录 Cookie，或把检查周期调长后重试", action, code)
	case -404:
		return fmt.Errorf("%s失败：UP 主不存在或已注销", action)
	}
	if message == "" {
		message = strconv.Itoa(code)
	}
	return fmt.Errorf("%s失败（code %d）：%s", action, code, message)
}

func bilibiliDurationText(length string) string {
	if length = strings.TrimSpace(length); length == "" {
		return ""
	}
	return "时长 " + length
}

func joinCookies(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), ";")); value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, "; ")
}

func unixFeedTime(seconds int64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}
