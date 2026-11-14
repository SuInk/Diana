// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

// 直播间链接没有作品 ID，也没有能下载的成片：按作品解析只会得到「接口解析失败」，
// 交给视频下载则是去拉一路直播流。这里单独读直播间信息，只回标题、主播和开播状态。

const (
	bilibiliLiveAnchorAPI = "https://api.live.bilibili.com/live_user/v1/UserInfo/get_anchor_in_room?roomid=%s"
	// 分享短链落到 reflow 页，带的是 room_id；这个接口按 room_id 查，匿名可用。
	douyinLiveReflowAPI = "https://webcast.amemv.com/webcast/room/reflow/info/?type_id=0&live_id=1&room_id=%s&app_id=1128"
	// live.douyin.com/<web_rid> 带的是 web_rid，只能走网页接口，需要一个 ttwid Cookie。
	douyinLiveWebEnterAPI = "https://live.douyin.com/webcast/room/web/enter/?aid=6383&app_name=douyin_web&live_id=1&device_platform=web&language=zh-CN&browser_language=zh-CN&browser_platform=MacIntel&browser_name=Chrome&browser_version=130.0.0.0&web_rid=%s"
	douyinLiveHomeURL     = "https://live.douyin.com/"
)

var (
	bilibiliLivePathPattern     = regexp.MustCompile(`^/(?:h5/|blanc/)?([0-9]+)/?$`)
	douyinLiveWebPathPattern    = regexp.MustCompile(`^/([0-9]+)/?$`)
	douyinLiveReflowPathPattern = regexp.MustCompile(`/webcast/reflow/([0-9]+)`)
	douyinSiteLivePathPattern   = regexp.MustCompile(`^/(?:follow|root)/live/([0-9]+)/?$`)
)

type liveRoomInfo struct {
	RoomID string
	Title  string
	Anchor string
	Area   string
	Cover  string
	// Status 取 live / offline / replay（B 站轮播）。
	Status string
	Online int64
}

func (info liveRoomInfo) statusText() string {
	switch info.Status {
	case "live":
		return "直播中"
	case "replay":
		return "未开播（轮播中）"
	default:
		return "未开播"
	}
}

// liveRoomContext 拼出给群里和模型看的直播间摘要。
func liveRoomContext(label string, info liveRoomInfo) string {
	title := strings.TrimSpace(info.Title)
	if title == "" {
		title = "直播间"
	}
	lines := []string{fmt.Sprintf("[%s直播] %s", label, title)}
	if anchor := strings.TrimSpace(info.Anchor); anchor != "" {
		lines = append(lines, "主播："+anchor)
	}
	lines = append(lines, "状态："+info.statusText())
	if area := strings.TrimSpace(info.Area); area != "" {
		lines = append(lines, "分区："+area)
	}
	if info.Status == "live" && info.Online > 0 {
		lines = append(lines, "人气："+strconv.FormatInt(info.Online, 10))
	}
	return strings.Join(lines, "\n")
}

func liveRoomSocialResult(platform, label string, info liveRoomInfo, status string) resolverSocialResult {
	result := resolverSocialResult{Handled: true}
	if status != "" {
		result.Failed = true
		result.Context = fmt.Sprintf("[%s] 这是直播间链接，直播间信息暂时读取失败。", label)
		return result
	}
	result.Context = liveRoomContext(label, info)
	result.ImageURLs = singleURL(info.Cover)
	if id := strings.TrimSpace(info.RoomID); id != "" {
		result.ResourceKeys = []string{platform + ":live:" + id}
	}
	return result
}

// bilibiliLiveRoomID 从 live.bilibili.com 的房间页取房间号（可能是短号）。
func bilibiliLiveRoomID(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !hostMatchesDomain(parsed.Hostname(), "live.bilibili.com") {
		return ""
	}
	if match := bilibiliLivePathPattern.FindStringSubmatch(parsed.EscapedPath()); len(match) > 1 {
		return match[1]
	}
	return ""
}

// douyinLiveTarget 识别抖音直播间链接：reflow 页给 room_id，网页版给 web_rid。
func douyinLiveTarget(raw string) (roomID, webRID string) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", ""
	}
	host, path := parsed.Hostname(), parsed.EscapedPath()
	switch {
	case hostMatchesDomain(host, "webcast.amemv.com"), hostMatchesDomain(host, "webcast.douyin.com"):
		if match := douyinLiveReflowPathPattern.FindStringSubmatch(path); len(match) > 1 {
			return match[1], ""
		}
		if id := strings.TrimSpace(parsed.Query().Get("roomId")); id != "" && isAllDigits(id) {
			return id, ""
		}
	case hostMatchesDomain(host, "live.douyin.com"):
		if match := douyinLiveWebPathPattern.FindStringSubmatch(path); len(match) > 1 {
			return "", match[1]
		}
	case hostMatchesDomain(host, "douyin.com"):
		if match := douyinSiteLivePathPattern.FindStringSubmatch(path); len(match) > 1 {
			return "", match[1]
		}
	}
	return "", ""
}

type bilibiliLiveRoomResponse struct {
	Code int `json:"code"`
	Data struct {
		RoomID         int64  `json:"room_id"`
		Title          string `json:"title"`
		LiveStatus     int    `json:"live_status"`
		UserCover      string `json:"user_cover"`
		Online         int64  `json:"online"`
		AreaName       string `json:"area_name"`
		ParentAreaName string `json:"parent_area_name"`
	} `json:"data"`
}

type bilibiliLiveAnchorResponse struct {
	Code int `json:"code"`
	Data struct {
		Info struct {
			Uname string `json:"uname"`
		} `json:"info"`
	} `json:"data"`
}

func bilibiliLiveRoomFromResponse(room bilibiliLiveRoomResponse, anchor bilibiliLiveAnchorResponse) liveRoomInfo {
	info := liveRoomInfo{
		Title:  room.Data.Title,
		Anchor: anchor.Data.Info.Uname,
		Cover:  room.Data.UserCover,
		Online: room.Data.Online,
	}
	if room.Data.RoomID > 0 {
		info.RoomID = strconv.FormatInt(room.Data.RoomID, 10)
	}
	switch room.Data.LiveStatus {
	case 1:
		info.Status = "live"
	case 2:
		info.Status = "replay"
	default:
		info.Status = "offline"
	}
	parent, area := strings.TrimSpace(room.Data.ParentAreaName), strings.TrimSpace(room.Data.AreaName)
	switch {
	case parent != "" && area != "" && parent != area:
		info.Area = parent + " · " + area
	default:
		info.Area = firstNonEmpty(area, parent)
	}
	return info
}

func fetchLiveRoom(ctx context.Context, platform, roomID, webRID string) (liveRoomInfo, string) {
	switch platform {
	case "bilibili":
		return fetchBilibiliLiveRoom(ctx, roomID)
	case "douyin":
		return fetchDouyinLiveRoom(ctx, roomID, webRID)
	}
	return liveRoomInfo{}, "unsupported_link"
}

func fetchBilibiliLiveRoom(ctx context.Context, roomID string) (liveRoomInfo, string) {
	referer := "https://live.bilibili.com/" + roomID
	var room bilibiliLiveRoomResponse
	if !fetchBilibiliJSON(ctx, bilibiliLiveRoomAPI+"?room_id="+url.QueryEscape(roomID), referer, &room) {
		return liveRoomInfo{}, "request_failed"
	}
	if room.Code != 0 {
		return liveRoomInfo{}, fmt.Sprintf("api_code_%d", room.Code)
	}
	// 主播名只是锦上添花，查不到不影响结果。
	var anchor bilibiliLiveAnchorResponse
	if !fetchBilibiliJSON(ctx, fmt.Sprintf(bilibiliLiveAnchorAPI, url.QueryEscape(roomID)), referer, &anchor) || anchor.Code != 0 {
		anchor = bilibiliLiveAnchorResponse{}
	}
	return bilibiliLiveRoomFromResponse(room, anchor), ""
}

type douyinLiveRoom struct {
	IDStr     string `json:"id_str"`
	Status    int    `json:"status"`
	Title     string `json:"title"`
	UserCount int64  `json:"user_count"`
	Cover     struct {
		URLList []string `json:"url_list"`
	} `json:"cover"`
	Owner struct {
		Nickname string `json:"nickname"`
	} `json:"owner"`
}

type douyinLiveReflowResponse struct {
	StatusCode int `json:"status_code"`
	Data       struct {
		Room douyinLiveRoom `json:"room"`
	} `json:"data"`
}

type douyinLiveWebEnterResponse struct {
	StatusCode int `json:"status_code"`
	Data       struct {
		Data []douyinLiveRoom `json:"data"`
		User struct {
			Nickname string `json:"nickname"`
		} `json:"user"`
	} `json:"data"`
}

func douyinLiveRoomInfo(room douyinLiveRoom, fallbackID, anchor string) liveRoomInfo {
	info := liveRoomInfo{
		RoomID: firstNonEmpty(strings.TrimSpace(room.IDStr), fallbackID),
		Title:  room.Title,
		Anchor: firstNonEmpty(strings.TrimSpace(room.Owner.Nickname), anchor),
		Cover:  firstNonEmptyString(room.Cover.URLList),
		Online: room.UserCount,
		Status: "offline",
	}
	// status：2 直播中，4 已下播。
	if room.Status == 2 {
		info.Status = "live"
	}
	return info
}

func fetchDouyinLiveRoom(ctx context.Context, roomID, webRID string) (liveRoomInfo, string) {
	if roomID != "" {
		headers := resolverCommonHeaders()
		headers["User-Agent"] = douyinMobileUserAgent
		var resp douyinLiveReflowResponse
		if !fetchResolverJSON(ctx, fmt.Sprintf(douyinLiveReflowAPI, url.QueryEscape(roomID)), headers, &resp) {
			return liveRoomInfo{}, "request_failed"
		}
		if resp.StatusCode != 0 || strings.TrimSpace(resp.Data.Room.IDStr) == "" {
			return liveRoomInfo{}, fmt.Sprintf("api_code_%d", resp.StatusCode)
		}
		return douyinLiveRoomInfo(resp.Data.Room, roomID, ""), ""
	}
	if webRID == "" {
		return liveRoomInfo{}, "unsupported_link"
	}
	headers := douyinWebHeaders(ctx, douyinLiveHomeURL)
	if douyinCookieValue(headers["Cookie"], "ttwid") == "" {
		ttwid := fetchDouyinLiveTTWID(ctx)
		if ttwid == "" {
			return liveRoomInfo{}, "missing_ttwid"
		}
		headers["Cookie"] = strings.TrimLeft(strings.TrimSpace(headers["Cookie"]+"; ttwid="+ttwid), "; ")
	}
	var resp douyinLiveWebEnterResponse
	if !fetchResolverJSON(ctx, fmt.Sprintf(douyinLiveWebEnterAPI, url.QueryEscape(webRID)), headers, &resp) {
		return liveRoomInfo{}, "request_failed"
	}
	if resp.StatusCode != 0 || len(resp.Data.Data) == 0 {
		return liveRoomInfo{}, fmt.Sprintf("api_code_%d", resp.StatusCode)
	}
	return douyinLiveRoomInfo(resp.Data.Data[0], webRID, resp.Data.User.Nickname), ""
}

// fetchDouyinLiveTTWID 从直播首页领一个匿名 ttwid：网页接口没有它会回空响应。
func fetchDouyinLiveTTWID(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, douyinLiveHomeURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", douyinUserAgent)
	client := netguard.NewPublicHTTPClient(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		recordResolverFailure(ctx, "抖音直播 ttwid", err.Error())
		return ""
	}
	defer resp.Body.Close()
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "ttwid" {
			return strings.TrimSpace(cookie.Value)
		}
	}
	return ""
}

// douyinPageURL 只给分享短链跟一次跳转；已经带作品或直播间 ID 的地址原样返回。
func douyinPageURL(ctx context.Context, raw string) string {
	if douyinIDPattern.MatchString(raw) {
		return raw
	}
	if roomID, webRID := douyinLiveTarget(raw); roomID != "" || webRID != "" {
		return raw
	}
	if final := fetchFinalURL(ctx, raw, resolverCommonHeaders()); final != "" {
		return final
	}
	return raw
}
