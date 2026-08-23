// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 抖音的作品接口要签名，主页 HTML 里只有一段混淆 JS，纯 HTTP 客户端拿不到任何
// 数据。内置抓取的做法和 RSSHub 一样：用本机的一次性无头浏览器打开用户主页，
// 把页面自己请求到的官方接口响应截下来解析。
const (
	douyinPostAPIFragment = "/aweme/v1/web/aweme/post/"
	douyinCaptureTimeout  = 45 * time.Second
)

func (p *RSSWatchPlugin) fetchDouyinUser(ctx context.Context, secUID string, settings SettingValues) (parsedFeed, error) {
	timeout := rssWatchTimeout(settings)
	// 无头浏览器要等页面加载 + 接口返回，比一次 HTTP 抓取慢得多，
	// 抓取超时太短时按抓取用的下限放宽。
	if timeout < douyinCaptureTimeout {
		timeout = douyinCaptureTimeout
	}
	pageURL := "https://www.douyin.com/user/" + secUID
	body, err := agent.CaptureNetworkJSON(ctx, agent.SandboxedBrowserConfig{Timeout: timeout}, agent.JSONCaptureRequest{
		PageURL:      pageURL,
		URLContains:  douyinPostAPIFragment,
		Cookie:       strings.TrimSpace(settings.String(rssWatchSettingDouyinCookie, "")),
		CookieDomain: ".douyin.com",
		UserAgent:    rssWatchBrowserUserAgent,
		Timeout:      timeout,
	})
	if err != nil {
		return parsedFeed{}, fmt.Errorf("抓取抖音主页失败：%w（抖音订阅需要本机装有 Chrome/Chromium；频繁触发验证码时可在插件设置里填写抖音 Cookie）", err)
	}
	return parseDouyinPosts(body, secUID)
}

func parseDouyinPosts(body []byte, secUID string) (parsedFeed, error) {
	var payload struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
		AwemeList  []struct {
			AwemeID    string `json:"aweme_id"`
			Desc       string `json:"desc"`
			CreateTime int64  `json:"create_time"`
			Author     struct {
				Nickname string `json:"nickname"`
			} `json:"author"`
			Statistics struct {
				DiggCount    int64 `json:"digg_count"`
				CommentCount int64 `json:"comment_count"`
			} `json:"statistics"`
			VideoTag []struct {
				TagName string `json:"tag_name"`
			} `json:"video_tag"`
		} `json:"aweme_list"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return parsedFeed{}, fmt.Errorf("解析抖音作品列表失败: %w", err)
	}
	if payload.StatusCode != 0 && len(payload.AwemeList) == 0 {
		return parsedFeed{}, fmt.Errorf("抖音接口返回异常（status_code %d）：%s", payload.StatusCode, strings.TrimSpace(payload.StatusMsg))
	}
	if len(payload.AwemeList) == 0 {
		return parsedFeed{}, fmt.Errorf("没有从抖音主页读到作品：账号可能没有公开作品，或请求被风控拦截")
	}
	feed := parsedFeed{Title: "抖音用户 " + secUID}
	for _, post := range payload.AwemeList {
		if nickname := strings.TrimSpace(post.Author.Nickname); nickname != "" && feed.Title == "抖音用户 "+secUID {
			feed.Title = cleanFeedText(nickname, 200) + " 的抖音作品"
		}
		tags := make([]string, 0, len(post.VideoTag))
		for _, tag := range post.VideoTag {
			if name := strings.TrimSpace(tag.TagName); name != "" {
				tags = append(tags, name)
			}
		}
		description := strings.TrimSpace(post.Desc)
		item := rssWatchItem{
			ID:          "aweme:" + post.AwemeID,
			Title:       cleanFeedText(firstFeedLine(description), 500),
			Author:      cleanFeedText(post.Author.Nickname, 200),
			Content:     cleanFeedText(strings.Join(nonEmptyStrings([]string{description, strings.Join(tags, " "), douyinStats(post.Statistics.DiggCount, post.Statistics.CommentCount)}), " "), 4000),
			PublishedAt: unixFeedTime(post.CreateTime),
			Link:        "https://www.douyin.com/video/" + post.AwemeID,
		}
		item.ID = stableFeedItemID(item)
		feed.Items = append(feed.Items, item)
	}
	sortFeedItemsByPublishedAt(feed.Items)
	return feed, nil
}

func douyinStats(likes, comments int64) string {
	parts := make([]string, 0, 2)
	if likes > 0 {
		parts = append(parts, fmt.Sprintf("点赞 %d", likes))
	}
	if comments > 0 {
		parts = append(parts, fmt.Sprintf("评论 %d", comments))
	}
	return strings.Join(parts, " ")
}

func firstFeedLine(value string) string {
	if index := strings.IndexAny(value, "\r\n"); index >= 0 {
		return strings.TrimSpace(value[:index])
	}
	return strings.TrimSpace(value)
}
