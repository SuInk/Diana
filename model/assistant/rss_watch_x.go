// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// X 官方给嵌入组件用的 syndication 时间线是公开的：不需要账号、不需要 API Key，
// 返回的页面里内联着 __NEXT_DATA__，推文正文、时间和链接都在里面。这就是内置
// 抓取的来源；被限流或想换来源的人可以在插件设置里填自定义 Feed 模板。
const (
	xSyndicationTimelineURL = "https://syndication.twitter.com/srv/timeline-profile/screen-name/"
	xNextDataPrefix         = `<script id="__NEXT_DATA__" type="application/json">`
)

func (p *RSSWatchPlugin) fetchXTimeline(ctx context.Context, handle string, settings SettingValues) (parsedFeed, error) {
	pageURL := xSyndicationTimelineURL + handle + "?showReplies=false"
	body, err := p.get(ctx, pageURL, settings, map[string]string{
		"User-Agent":      rssWatchBrowserUserAgent,
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
		"Referer":         "https://platform.twitter.com/",
	})
	if err != nil {
		return parsedFeed{}, fmt.Errorf("读取 @%s 的 X 时间线失败：%w（被限流时可在插件设置里填写自定义 Feed 模板换用自建中转）", handle, err)
	}
	return parseXTimelinePage(string(body), handle)
}

func parseXTimelinePage(page, handle string) (parsedFeed, error) {
	payload, err := extractXNextData(page)
	if err != nil {
		return parsedFeed{}, err
	}
	tweets, err := parseXSyndicationTweets(payload)
	if err != nil {
		return parsedFeed{}, err
	}
	if len(tweets) == 0 {
		return parsedFeed{}, fmt.Errorf("没有从 @%s 的 X 时间线读到推文：账号可能受保护、已改名或暂时被限流", handle)
	}
	feed := parsedFeed{Title: "@" + handle}
	for _, tweet := range tweets {
		item := rssWatchItem{
			ID:          "tweet:" + tweet.IDStr,
			Title:       cleanFeedText(firstNonEmpty(tweet.FullText, tweet.Text), 200),
			Content:     cleanFeedText(firstNonEmpty(tweet.FullText, tweet.Text), 4000),
			Author:      cleanFeedText(xTweetAuthor(tweet, handle), 200),
			PublishedAt: parseXTweetTime(tweet.CreatedAt),
			Link:        xTweetLink(tweet, handle),
		}
		item.ID = stableFeedItemID(item)
		feed.Items = append(feed.Items, item)
	}
	sortFeedItemsByPublishedAt(feed.Items)
	return feed, nil
}

type xSyndicationTweet struct {
	IDStr     string `json:"id_str"`
	Text      string `json:"text"`
	FullText  string `json:"full_text"`
	CreatedAt string `json:"created_at"`
	Permalink string `json:"permalink"`
	User      struct {
		Name       string `json:"name"`
		ScreenName string `json:"screen_name"`
	} `json:"user"`
}

func extractXNextData(page string) (string, error) {
	index := strings.Index(page, xNextDataPrefix)
	if index < 0 {
		return "", fmt.Errorf("没有在 X 时间线页面里找到推文数据：接口可能已调整，或请求被风控拦截")
	}
	rest := page[index+len(xNextDataPrefix):]
	end := strings.Index(rest, "</script>")
	if end < 0 {
		return "", fmt.Errorf("X 时间线页面结构异常，推文数据没有闭合")
	}
	return rest[:end], nil
}

// parseXSyndicationTweets 递归找出所有推文对象。syndication 的响应层级随版本改过
// 几次（entries[].content.tweet、timeline.instructions[]……），按结构特征找比按
// 固定路径取更耐改版。
func parseXSyndicationTweets(payload string) ([]xSyndicationTweet, error) {
	var root any
	if err := json.Unmarshal([]byte(payload), &root); err != nil {
		return nil, fmt.Errorf("解析 X 时间线数据失败: %w", err)
	}
	seen := map[string]bool{}
	var tweets []xSyndicationTweet
	var visit func(node any)
	visit = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			if tweet, ok := decodeXTweet(value); ok && !seen[tweet.IDStr] {
				seen[tweet.IDStr] = true
				tweets = append(tweets, tweet)
			}
			for _, child := range value {
				visit(child)
			}
		case []any:
			for _, child := range value {
				visit(child)
			}
		}
	}
	visit(root)
	return tweets, nil
}

func decodeXTweet(node map[string]any) (xSyndicationTweet, bool) {
	id, hasID := node["id_str"].(string)
	if !hasID || strings.TrimSpace(id) == "" {
		return xSyndicationTweet{}, false
	}
	text, hasText := node["full_text"].(string)
	if !hasText {
		text, hasText = node["text"].(string)
	}
	if !hasText {
		return xSyndicationTweet{}, false
	}
	if _, ok := node["created_at"].(string); !ok {
		return xSyndicationTweet{}, false
	}
	encoded, err := json.Marshal(node)
	if err != nil {
		return xSyndicationTweet{}, false
	}
	var tweet xSyndicationTweet
	if err := json.Unmarshal(encoded, &tweet); err != nil {
		return xSyndicationTweet{}, false
	}
	if strings.TrimSpace(tweet.FullText) == "" {
		tweet.FullText = text
	}
	return tweet, true
}

func xTweetAuthor(tweet xSyndicationTweet, handle string) string {
	if name := strings.TrimSpace(tweet.User.Name); name != "" {
		return name
	}
	if screen := strings.TrimSpace(tweet.User.ScreenName); screen != "" {
		return "@" + screen
	}
	return "@" + handle
}

func xTweetLink(tweet xSyndicationTweet, handle string) string {
	if permalink := strings.TrimSpace(tweet.Permalink); permalink != "" {
		if strings.HasPrefix(permalink, "http") {
			return permalink
		}
		return "https://x.com" + permalink
	}
	screen := firstNonEmpty(strings.TrimSpace(tweet.User.ScreenName), handle)
	if strings.TrimSpace(tweet.IDStr) == "" {
		return "https://x.com/" + screen
	}
	return "https://x.com/" + screen + "/status/" + tweet.IDStr
}

func parseXTweetTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "Mon Jan 02 15:04:05 -0700 2006"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}
