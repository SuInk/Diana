// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// B 站订阅和推特一样借 RSS 订阅的游标、规则和推送：来源地址存成 B 站公开接口，
// 抓取时按地址分流解析成 Feed 条目。三种来源：
//   - 动态：UP 主空间动态（含投稿视频、图文、转发）
//   - 直播：开播一次算一条，下播期间没有条目，游标原样保留
//   - 番剧：每集一条，新集上线即推送
const (
	rssSourceBilibiliDynamic = "bilibili_dynamic"
	rssSourceBilibiliLive    = "bilibili_live"
	rssSourceBilibiliBangumi = "bilibili_bangumi"

	bilibiliDynamicAPI   = "https://api.bilibili.com/x/polymer/web-dynamic/v1/feed/space"
	bilibiliLiveByUIDAPI = "https://api.live.bilibili.com/room/v1/Room/getRoomInfoOld"
	bilibiliLiveRoomAPI  = "https://api.live.bilibili.com/room/v1/Room/get_info"
	bilibiliSeasonAPI    = "https://api.bilibili.com/pgc/view/web/season"
	bilibiliBrowserUA    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
)

var (
	bilibiliNumericPattern = regexp.MustCompile(`^\d{1,20}$`)
	bilibiliSeasonPattern  = regexp.MustCompile(`(?i)^(ss|ep)(\d{1,20})$`)
	bilibiliShanghai       = time.FixedZone("CST", 8*3600)
)

// bilibiliSourceFromInput 认 B 站来源的几种写法：带前缀的 dynamic:UID / live:UID /
// bangumi:ss123，空间、直播间、番剧页面链接，以及已保存订阅交回来的接口地址。
// 不是 B 站来源时 ok 为 false，交给普通 Feed 处理。
func bilibiliSourceFromInput(raw string) (ReminderFeedSource, bool, error) {
	raw = strings.TrimSpace(raw)
	if kind, value, found := strings.Cut(raw, ":"); found && !strings.HasPrefix(value, "//") {
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case "dynamic", "动态":
			if source, ok, _ := bilibiliSourceFromInput(value); ok && source.Source == rssSourceBilibiliDynamic {
				return source, true, nil
			}
			return bilibiliDynamicSource(value)
		case "live", "直播":
			if source, ok, _ := bilibiliSourceFromInput(value); ok && source.Source == rssSourceBilibiliLive {
				return source, true, nil
			}
			if source, ok, _ := bilibiliSourceFromInput(value); ok && source.Source == rssSourceBilibiliDynamic {
				return bilibiliLiveByUIDSource(source.Handle)
			}
			return bilibiliLiveByUIDSource(value)
		case "bangumi", "番剧":
			if source, ok, _ := bilibiliSourceFromInput(value); ok && source.Source == rssSourceBilibiliBangumi {
				return source, true, nil
			}
			return bilibiliBangumiSource(value)
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ReminderFeedSource{}, false, nil
	}
	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	query := parsed.Query()
	switch {
	case host == "space.bilibili.com" || host == "m.bilibili.com" && len(segments) >= 2 && segments[0] == "space":
		uid := segments[0]
		if host == "m.bilibili.com" {
			uid = segments[1]
		}
		return bilibiliDynamicSource(uid)
	case host == "live.bilibili.com":
		return bilibiliLiveRoomSource(segments[len(segments)-1])
	case host == "bilibili.com" && len(segments) >= 3 && segments[0] == "bangumi" && segments[1] == "play":
		return bilibiliBangumiSource(segments[2])
	case host == "bilibili.com" && len(segments) >= 3 && segments[0] == "bangumi" && segments[1] == "media":
		return ReminderFeedSource{}, true, fmt.Errorf("番剧请填播放页链接（/bangumi/play/ss… 或 ep…），media 页 md 号暂不支持")
	case host == "api.bilibili.com" && parsed.Path == strings.TrimPrefix(bilibiliDynamicAPI, "https://api.bilibili.com"):
		return bilibiliDynamicSource(query.Get("host_mid"))
	case host == "api.live.bilibili.com" && strings.HasSuffix(bilibiliLiveByUIDAPI, parsed.Path):
		return bilibiliLiveByUIDSource(query.Get("mid"))
	case host == "api.live.bilibili.com" && strings.HasSuffix(bilibiliLiveRoomAPI, parsed.Path):
		return bilibiliLiveRoomSource(query.Get("room_id"))
	case host == "api.bilibili.com" && strings.HasSuffix(bilibiliSeasonAPI, parsed.Path):
		if id := query.Get("season_id"); id != "" {
			return bilibiliBangumiSource("ss" + id)
		}
		return bilibiliBangumiSource("ep" + query.Get("ep_id"))
	}
	return ReminderFeedSource{}, false, nil
}

func bilibiliDynamicSource(uid string) (ReminderFeedSource, bool, error) {
	uid = strings.TrimPrefix(strings.TrimSpace(uid), "UID")
	if !bilibiliNumericPattern.MatchString(uid) {
		return ReminderFeedSource{}, true, fmt.Errorf("B 站 UID 不正确，请填数字 UID 或空间链接 space.bilibili.com/UID")
	}
	return ReminderFeedSource{FeedURL: bilibiliDynamicAPI + "?host_mid=" + uid, Source: rssSourceBilibiliDynamic, Handle: uid}, true, nil
}

func bilibiliLiveByUIDSource(uid string) (ReminderFeedSource, bool, error) {
	uid = strings.TrimSpace(uid)
	if !bilibiliNumericPattern.MatchString(uid) {
		return ReminderFeedSource{}, true, fmt.Errorf("B 站直播订阅请填主播 UID、空间链接或直播间链接 live.bilibili.com/房间号")
	}
	return ReminderFeedSource{FeedURL: bilibiliLiveByUIDAPI + "?mid=" + uid, Source: rssSourceBilibiliLive, Handle: "UID " + uid}, true, nil
}

func bilibiliLiveRoomSource(room string) (ReminderFeedSource, bool, error) {
	room = strings.TrimSpace(room)
	if !bilibiliNumericPattern.MatchString(room) {
		return ReminderFeedSource{}, true, fmt.Errorf("直播间链接不正确，应为 live.bilibili.com/房间号")
	}
	return ReminderFeedSource{FeedURL: bilibiliLiveRoomAPI + "?room_id=" + room, Source: rssSourceBilibiliLive, Handle: "直播间 " + room}, true, nil
}

func bilibiliBangumiSource(raw string) (ReminderFeedSource, bool, error) {
	match := bilibiliSeasonPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return ReminderFeedSource{}, true, fmt.Errorf("番剧请填 ss/ep 编号或播放页链接，例如 ss12345")
	}
	kind, id := strings.ToLower(match[1]), match[2]
	param := "season_id"
	if kind == "ep" {
		param = "ep_id"
	}
	return ReminderFeedSource{FeedURL: bilibiliSeasonAPI + "?" + param + "=" + id, Source: rssSourceBilibiliBangumi, Handle: kind + id}, true, nil
}

func isBilibiliFeedURL(feedURL string) bool {
	source, ok, err := bilibiliSourceFromInput(feedURL)
	return ok && err == nil && source.FeedURL != ""
}

func (p *RSSWatchPlugin) fetchBilibiliFeed(ctx context.Context, feedURL string) (parsedFeed, error) {
	source, _, err := bilibiliSourceFromInput(feedURL)
	if err != nil {
		return parsedFeed{}, err
	}
	switch source.Source {
	case rssSourceBilibiliDynamic:
		return p.fetchBilibiliDynamic(ctx, feedURL)
	case rssSourceBilibiliLive:
		return p.fetchBilibiliLive(ctx, feedURL)
	default:
		return p.fetchBilibiliBangumi(ctx, feedURL)
	}
}

type bilibiliEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Msg     string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
	Result  json.RawMessage `json:"result"`
}

func (p *RSSWatchPlugin) bilibiliGet(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", bilibiliBrowserUA)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Origin", "https://www.bilibili.com")
	req.Header.Set("Accept", "application/json")
	// 动态接口没有 buvid3 会直接 -352 风控；带上登录 Cookie 更稳，没配就只带设备号。
	cookie := "buvid3=" + bilibiliBuvid()
	if sessdata := bilibiliSessdata(ctx); sessdata != "" {
		cookie += "; SESSDATA=" + sessdata
	}
	req.Header.Set("Cookie", cookie)
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 B 站接口失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusPreconditionFailed {
			return fmt.Errorf("B 站接口返回 HTTP 412，被风控拦截；可在链接解析插件里配置 B 站 SESSDATA 后重试")
		}
		return fmt.Errorf("B 站接口返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maximumRSSBodyBytes))
	if err != nil {
		return fmt.Errorf("读取 B 站接口失败: %w", err)
	}
	var envelope bilibiliEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("解析 B 站接口失败: %w", err)
	}
	if envelope.Code != 0 {
		hint := ""
		if envelope.Code == -352 || envelope.Code == -412 || envelope.Code == -101 {
			hint = "（被风控或需要登录，可在链接解析插件里配置 B 站 SESSDATA）"
		}
		return fmt.Errorf("B 站接口返回错误 %d: %s%s", envelope.Code, firstNonEmpty(envelope.Message, envelope.Msg), hint)
	}
	payload := envelope.Data
	if len(envelope.Result) > 0 && string(envelope.Result) != "null" {
		payload = envelope.Result
	}
	return json.Unmarshal(payload, target)
}

// bilibiliBuvid 生成一个形如浏览器设备号的值，只用来通过风控的格式检查。
func bilibiliBuvid() string {
	raw := strings.ToUpper(fmt.Sprintf("%032x", time.Now().UnixNano()))
	return raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:32] + "infoc"
}

type bilibiliDynamicItem struct {
	IDStr   string `json:"id_str"`
	Type    string `json:"type"`
	Modules struct {
		Author struct {
			Name  string `json:"name"`
			PubTS int64  `json:"pub_ts"`
		} `json:"module_author"`
		Dynamic bilibiliDynamicBody `json:"module_dynamic"`
		Tag     *struct {
			Text string `json:"text"`
		} `json:"module_tag"`
	} `json:"modules"`
	Orig *bilibiliDynamicItem `json:"orig"`
}

type bilibiliDynamicBody struct {
	Desc *struct {
		Text string `json:"text"`
	} `json:"desc"`
	Major *struct {
		Archive *struct {
			Title   string `json:"title"`
			Desc    string `json:"desc"`
			Cover   string `json:"cover"`
			JumpURL string `json:"jump_url"`
		} `json:"archive"`
		Draw *struct {
			Items []struct {
				Src string `json:"src"`
			} `json:"items"`
		} `json:"draw"`
		Opus *struct {
			Title   string `json:"title"`
			Summary struct {
				Text string `json:"text"`
			} `json:"summary"`
			Pics []struct {
				URL string `json:"url"`
			} `json:"pics"`
		} `json:"opus"`
		Article *struct {
			Title   string   `json:"title"`
			Desc    string   `json:"desc"`
			Covers  []string `json:"covers"`
			JumpURL string   `json:"jump_url"`
		} `json:"article"`
		Live *struct {
			Title string `json:"title"`
			Cover string `json:"cover"`
		} `json:"live"`
	} `json:"major"`
}

// summary 把一条动态拍平成标题、正文、配图和跳转链接。
func (body bilibiliDynamicBody) summary() (title, text string, images []string, link string) {
	if body.Desc != nil {
		text = body.Desc.Text
	}
	major := body.Major
	if major == nil {
		return
	}
	switch {
	case major.Archive != nil:
		title = "投稿视频：" + major.Archive.Title
		text = strings.TrimSpace(text + "\n" + major.Archive.Desc)
		images = append(images, major.Archive.Cover)
		link = normalizeBilibiliLink(major.Archive.JumpURL)
	case major.Opus != nil:
		title = major.Opus.Title
		text = firstNonEmpty(major.Opus.Summary.Text, text)
		for _, pic := range major.Opus.Pics {
			images = append(images, pic.URL)
		}
	case major.Draw != nil:
		for _, item := range major.Draw.Items {
			images = append(images, item.Src)
		}
	case major.Article != nil:
		title = "专栏：" + major.Article.Title
		text = strings.TrimSpace(text + "\n" + major.Article.Desc)
		images = append(images, major.Article.Covers...)
		link = normalizeBilibiliLink(major.Article.JumpURL)
	case major.Live != nil:
		title = "直播：" + major.Live.Title
		images = append(images, major.Live.Cover)
	}
	return
}

func normalizeBilibiliLink(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	return raw
}

func (p *RSSWatchPlugin) fetchBilibiliDynamic(ctx context.Context, feedURL string) (parsedFeed, error) {
	var data struct {
		Items []bilibiliDynamicItem `json:"items"`
	}
	if err := p.bilibiliGet(ctx, feedURL+"&features=itemOpusStyle", &data); err != nil {
		return parsedFeed{}, err
	}
	// 未登录时 B 站对空间动态返回 code 0 加空列表，不报错；不拦下来订阅会一直安静地什么都收不到。
	if len(data.Items) == 0 && bilibiliSessdata(ctx) == "" {
		return parsedFeed{}, fmt.Errorf("B 站空间动态需要登录才能读取，请在链接解析插件里配置 B 站 SESSDATA")
	}
	feed := parsedFeed{Items: []rssWatchItem{}}
	for _, entry := range data.Items {
		// 置顶动态永远排第一，按「最新在前」算游标会把它当成最新一条。
		if entry.Modules.Tag != nil && strings.Contains(entry.Modules.Tag.Text, "置顶") {
			continue
		}
		author := strings.TrimSpace(entry.Modules.Author.Name)
		if feed.Title == "" && author != "" {
			feed.Title = author + " 的 B 站动态"
		}
		title, text, images, link := entry.Modules.Dynamic.summary()
		if entry.Orig != nil {
			origTitle, origText, origImages, _ := entry.Orig.Modules.Dynamic.summary()
			origAuthor := strings.TrimSpace(entry.Orig.Modules.Author.Name)
			title = "转发 " + origAuthor
			text = strings.TrimSpace(text + "\n\n// @" + origAuthor + "：" + strings.TrimSpace(origTitle+"\n"+origText))
			images = append(images, origImages...)
		}
		if link == "" || entry.Orig != nil {
			link = "https://t.bilibili.com/" + entry.IDStr
		}
		item := rssWatchItem{
			ID: entry.IDStr, Title: cleanFeedText(title, 500), Link: link, Author: author,
			Content: cleanFeedText(text, 4000), Images: compactStrings(images),
		}
		if entry.Modules.Author.PubTS > 0 {
			item.PublishedAt = time.Unix(entry.Modules.Author.PubTS, 0)
		}
		feed.Items = append(feed.Items, item)
	}
	return feed, nil
}

func (p *RSSWatchPlugin) fetchBilibiliLive(ctx context.Context, feedURL string) (parsedFeed, error) {
	roomID := ""
	parsed, _ := url.Parse(feedURL)
	if mid := parsed.Query().Get("mid"); mid != "" {
		var info struct {
			RoomID int64 `json:"roomid"`
		}
		if err := p.bilibiliGet(ctx, feedURL, &info); err != nil {
			return parsedFeed{}, err
		}
		if info.RoomID == 0 {
			return parsedFeed{}, fmt.Errorf("UID %s 还没有开通直播间", mid)
		}
		roomID = strconv.FormatInt(info.RoomID, 10)
	} else {
		roomID = parsed.Query().Get("room_id")
	}
	var room struct {
		UID        int64  `json:"uid"`
		RoomID     int64  `json:"room_id"`
		Title      string `json:"title"`
		LiveStatus int    `json:"live_status"`
		LiveTime   string `json:"live_time"`
		UserCover  string `json:"user_cover"`
		AreaName   string `json:"area_name"`
	}
	if err := p.bilibiliGet(ctx, bilibiliLiveRoomAPI+"?room_id="+url.QueryEscape(roomID), &room); err != nil {
		return parsedFeed{}, err
	}
	if room.RoomID != 0 {
		roomID = strconv.FormatInt(room.RoomID, 10)
	}
	feed := parsedFeed{Title: "B 站直播间 " + roomID, Items: []rssWatchItem{}}
	// 下播时不给条目：游标停在上一场，下次开播的 live_time 更新，自然算新条目。
	if room.LiveStatus != 1 {
		return feed, nil
	}
	startedAt, _ := time.ParseInLocation(time.DateTime, room.LiveTime, bilibiliShanghai)
	content := "直播标题：" + room.Title
	if room.AreaName != "" {
		content += "\n分区：" + room.AreaName
	}
	feed.Items = append(feed.Items, rssWatchItem{
		ID: "live:" + roomID + ":" + room.LiveTime, Title: cleanFeedText("开播了："+room.Title, 500),
		Link: "https://live.bilibili.com/" + roomID, Content: content, PublishedAt: startedAt,
		Images: compactStrings([]string{room.UserCover}),
	})
	return feed, nil
}

func (p *RSSWatchPlugin) fetchBilibiliBangumi(ctx context.Context, feedURL string) (parsedFeed, error) {
	var season struct {
		Title    string `json:"title"`
		Episodes []struct {
			ID        int64  `json:"id"`
			Title     string `json:"title"`
			LongTitle string `json:"long_title"`
			Cover     string `json:"cover"`
			ShareURL  string `json:"share_url"`
			PubTime   int64  `json:"pub_time"`
		} `json:"episodes"`
	}
	if err := p.bilibiliGet(ctx, feedURL, &season); err != nil {
		return parsedFeed{}, err
	}
	feed := parsedFeed{Title: season.Title, Items: make([]rssWatchItem, 0, len(season.Episodes))}
	// 接口按集数正序，Feed 约定最新在前。
	for index := len(season.Episodes) - 1; index >= 0; index-- {
		episode := season.Episodes[index]
		id := strconv.FormatInt(episode.ID, 10)
		item := rssWatchItem{
			ID: "ep" + id, Title: cleanFeedText(strings.TrimSpace(season.Title+" 第"+episode.Title+"话 "+episode.LongTitle), 500),
			Link: firstNonEmpty(episode.ShareURL, "https://www.bilibili.com/bangumi/play/ep"+id), Author: season.Title,
			Content: "番剧更新：" + strings.TrimSpace(episode.Title+" "+episode.LongTitle), Images: compactStrings([]string{episode.Cover}),
		}
		if episode.PubTime > 0 {
			item.PublishedAt = time.Unix(episode.PubTime, 0)
		}
		feed.Items = append(feed.Items, item)
	}
	return feed, nil
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = normalizeBilibiliLink(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// bilibiliSourceLabel 是 B 站来源在列表和通知抬头里的名字。
func bilibiliSourceLabel(source ReminderFeedSource) string {
	name := strings.TrimSpace(source.Name)
	handle := strings.TrimSpace(source.Handle)
	switch source.Source {
	case rssSourceBilibiliDynamic:
		return firstNonEmpty(name, "B 站 UID "+handle+" 的动态")
	case rssSourceBilibiliLive:
		return "B 站直播 " + handle
	case rssSourceBilibiliBangumi:
		return "番剧 " + firstNonEmpty(name, handle)
	}
	return ""
}

// withBilibiliFeedCredentials 让 B 站订阅沿用链接解析插件里这台机器人配的 SESSDATA，
// 不另设一个凭据项。没配时保持原样，好让环境变量兜底继续生效。
func (r *Runtime) withBilibiliFeedCredentials(ctx context.Context, event MessageEvent) context.Context {
	if r == nil || r.plugins == nil {
		return ctx
	}
	_, settings, enabled := r.plugins.PluginWithSettingsForGroup(resolverPluginID, r.pluginOverridesForEvent(event), r.pluginSettingOverridesForEvent(event))
	if !enabled {
		return ctx
	}
	creds := resolverCredentialsFromSettings(settings)
	if creds.BiliSessdata == "" {
		return ctx
	}
	return withResolverCredentials(ctx, creds)
}
