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

// 小红书主页是服务端渲染的：笔记列表就写在 window.__INITIAL_STATE__ 里，
// 不用登录也能读到标题、封面类型和发布时间。带上登录 Cookie 时这段数据里还会
// 有笔记 ID，能拼出直达链接。
const xiaohongshuStatePrefix = "window.__INITIAL_STATE__="

type xiaohongshuNoteCard struct {
	NoteID       string `json:"noteId"`
	DisplayTitle string `json:"displayTitle"`
	Type         string `json:"type"`
	Time         int64  `json:"time"`
	XsecToken    string `json:"xsecToken"`
	User         struct {
		Nickname string `json:"nickname"`
		NickName string `json:"nickName"`
	} `json:"user"`
	InteractInfo struct {
		LikedCount string `json:"likedCount"`
	} `json:"interactInfo"`
}

func (p *RSSWatchPlugin) fetchXiaohongshuUser(ctx context.Context, userID string, settings SettingValues) (parsedFeed, error) {
	pageURL := "https://www.xiaohongshu.com/user/profile/" + userID
	body, err := p.get(ctx, pageURL, settings, map[string]string{
		"User-Agent":      rssWatchBrowserUserAgent,
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "zh-CN,zh;q=0.9",
		"Referer":         "https://www.xiaohongshu.com/",
		"Cookie":          strings.TrimSpace(settings.String(rssWatchSettingXiaohongshuCookie, "")),
	})
	if err != nil {
		return parsedFeed{}, err
	}
	return parseXiaohongshuProfile(string(body), userID)
}

func parseXiaohongshuProfile(page, userID string) (parsedFeed, error) {
	state, err := extractXiaohongshuState(page)
	if err != nil {
		return parsedFeed{}, err
	}
	var payload struct {
		User map[string]json.RawMessage `json:"user"`
	}
	if err := json.Unmarshal([]byte(state), &payload); err != nil {
		return parsedFeed{}, fmt.Errorf("解析小红书主页数据失败: %w", err)
	}
	author := xiaohongshuNickname(payload.User["userPageData"])
	var notes [][]struct {
		NoteCard  xiaohongshuNoteCard `json:"noteCard"`
		XsecToken string              `json:"xsecToken"`
	}
	if err := json.Unmarshal(unwrapVueRawValue(payload.User["notes"]), &notes); err != nil {
		return parsedFeed{}, fmt.Errorf("解析小红书笔记列表失败: %w", err)
	}
	feed := parsedFeed{Title: "小红书用户 " + userID}
	if author != "" {
		feed.Title = author + " 的小红书笔记"
	}
	for _, row := range notes {
		for _, note := range row {
			card := note.NoteCard
			title := cleanFeedText(card.DisplayTitle, 500)
			if title == "" {
				continue
			}
			item := rssWatchItem{
				Title:       title,
				Author:      cleanFeedText(firstNonEmpty(card.User.Nickname, card.User.NickName, author), 200),
				Content:     cleanFeedText(strings.Join(nonEmptyStrings([]string{xiaohongshuNoteKind(card.Type), title, xiaohongshuLikes(card.InteractInfo.LikedCount)}), " "), 4000),
				PublishedAt: millisecondFeedTime(card.Time),
				Link:        xiaohongshuNoteLink(userID, card.NoteID, firstNonEmpty(card.XsecToken, note.XsecToken)),
			}
			if card.NoteID != "" {
				item.ID = "note:" + card.NoteID
			} else {
				// 没登录时小红书不给笔记 ID，链接又统一指向主页，只能按内容摘要
				// 生成条目 ID，否则每条笔记都会算成同一条。
				item.ID = stableFeedItemID(rssWatchItem{Title: item.Title, Content: item.Content, PublishedAt: item.PublishedAt})
			}
			feed.Items = append(feed.Items, item)
		}
	}
	if len(feed.Items) == 0 {
		return parsedFeed{}, fmt.Errorf("没有从小红书主页读到笔记：账号可能没有公开笔记，或触发了风控（可在插件设置里填写小红书 Cookie）")
	}
	sortFeedItemsByPublishedAt(feed.Items)
	return feed, nil
}

func xiaohongshuNickname(raw json.RawMessage) string {
	var page struct {
		BasicInfo struct {
			Nickname string `json:"nickname"`
		} `json:"basicInfo"`
	}
	if err := json.Unmarshal(unwrapVueRawValue(raw), &page); err != nil {
		return ""
	}
	return strings.TrimSpace(page.BasicInfo.Nickname)
}

// unwrapVueRawValue 剥掉小红书把状态包进 Vue ref 时多出来的 _rawValue 层。
func unwrapVueRawValue(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	var wrapper struct {
		RawValue json.RawMessage `json:"_rawValue"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.RawValue) > 0 && string(wrapper.RawValue) != "null" {
		return wrapper.RawValue
	}
	return raw
}

// extractXiaohongshuState 取出内联的状态 JSON，并把里面的裸 undefined 换成 null。
func extractXiaohongshuState(page string) (string, error) {
	index := strings.Index(page, xiaohongshuStatePrefix)
	if index < 0 {
		return "", fmt.Errorf("没有在小红书主页里找到笔记数据：页面可能要求登录或已触发风控")
	}
	rest := page[index+len(xiaohongshuStatePrefix):]
	if end := strings.Index(rest, "</script>"); end >= 0 {
		rest = rest[:end]
	}
	return replaceJSONUndefined(strings.TrimSuffix(strings.TrimSpace(rest), ";")), nil
}

// replaceJSONUndefined 把字符串字面量之外的 undefined 换成 null。小红书内联的是
// JS 对象而不是严格 JSON，直接整串替换会改坏正文里恰好写了 undefined 的笔记。
func replaceJSONUndefined(raw string) string {
	var builder strings.Builder
	builder.Grow(len(raw))
	inString, escaped := false, false
	for index := 0; index < len(raw); index++ {
		char := raw[index]
		if inString {
			builder.WriteByte(char)
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
			builder.WriteByte(char)
			continue
		}
		if char == 'u' && strings.HasPrefix(raw[index:], "undefined") {
			builder.WriteString("null")
			index += len("undefined") - 1
			continue
		}
		builder.WriteByte(char)
	}
	return builder.String()
}

func xiaohongshuNoteLink(userID, noteID, token string) string {
	if strings.TrimSpace(noteID) == "" {
		return "https://www.xiaohongshu.com/user/profile/" + userID
	}
	link := "https://www.xiaohongshu.com/explore/" + noteID
	if token = strings.TrimSpace(token); token != "" {
		link += "?xsec_token=" + token + "&xsec_source=pc_user"
	}
	return link
}

func xiaohongshuNoteKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "video":
		return "视频笔记"
	case "normal":
		return "图文笔记"
	}
	return "笔记"
}

func xiaohongshuLikes(count string) string {
	if count = strings.TrimSpace(count); count == "" {
		return ""
	}
	return "点赞 " + count
}

func millisecondFeedTime(milliseconds int64) time.Time {
	if milliseconds <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(milliseconds).UTC()
}
