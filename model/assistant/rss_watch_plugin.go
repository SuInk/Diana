// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"
)

const (
	rssWatchPluginID = "official.rss-watch"

	rssWatchSettingTimeout   = "timeout_seconds"
	rssWatchSettingItemLimit = "judge_item_limit"

	// Twitter 订阅直接走 FxTwitter 的公开时间线接口，不需要任何额外部署。
	// 这和链接解析抓单条推文用的是同一个上游，只是换成 v2 的 profile 路由。
	// 以前这里还有一个「Twitter RSS 模板」设置项，默认能用之后它就只剩下被填错
	// 的份（公共 RSSHub 早已下线 X/Twitter 路由），所以整个删掉：想换别的来源，
	// 直接把那个来源的 Feed 地址当普通 RSS 订阅填进去就行。
	defaultTwitterStatusesAPI = "https://api.fxtwitter.com/2/profile/{handle}/statuses"
	maximumRSSBodyBytes       = 4 << 20
)

var twitterHandlePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

type RSSWatchPlugin struct {
	client *http.Client
}

type rssWatchSnapshot struct {
	ItemID      string
	PublishedAt time.Time
}

type rssWatchChange struct {
	FeedURL  string           `json:"feed_url"`
	FeedName string           `json:"feed_name,omitempty"`
	Items    []rssWatchItem   `json:"items"`
	Snapshot rssWatchSnapshot `json:"-"`
}

type rssWatchItem struct {
	ID          string    `json:"id"`
	Title       string    `json:"title,omitempty"`
	Link        string    `json:"link,omitempty"`
	Author      string    `json:"author,omitempty"`
	Content     string    `json:"content,omitempty"`
	PublishedAt time.Time `json:"published_at,omitempty"`
}

type parsedFeed struct {
	Title string
	Items []rssWatchItem
}

func NewRSSWatchPlugin(client *http.Client) *RSSWatchPlugin {
	if client == nil {
		client = netguard.NewPublicHTTPClient(20 * time.Second)
	}
	return &RSSWatchPlugin{client: client}
}

func (p *RSSWatchPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          rssWatchPluginID,
		Name:        "RSS 订阅",
		Version:     "0.2.0",
		Description: "订阅 RSS/Atom 或指定 X (Twitter) 用户，一条订阅可以同时盯多个账号或 Feed 并共用一套规则；发现新内容后由 LLM 判断是否需要通知，并生成实际回复。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:https", "task:persistent", "message:send", "llm:generate"},
		Settings: []PluginSettingSpec{
			{
				Key:         rssWatchSettingTimeout,
				Label:       "抓取超时",
				Description: "单次 Feed 请求的最长等待时间。",
				Type:        PluginSettingTypeNumber,
				Default:     20,
				Min:         settingRange(5),
				Max:         settingRange(60),
				Step:        1,
				Unit:        "秒",
			},
			{
				Key:         rssWatchSettingItemLimit,
				Label:       "单次判断条目上限",
				Description: "一次交给模型判断的最新条目数量；Feed 游标仍推进到已读取的最新条目。",
				Type:        PluginSettingTypeNumber,
				Default:     12,
				Min:         settingRange(1),
				Max:         settingRange(30),
				Step:        1,
				Unit:        "条",
			},
		},
	}
}

func (*RSSWatchPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func normalizeTwitterHandle(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
		if host == "x.com" || host == "twitter.com" {
			raw = strings.Split(strings.Trim(parsed.Path, "/"), "/")[0]
		}
	}
	raw = strings.TrimPrefix(raw, "@")
	if !twitterHandlePattern.MatchString(raw) {
		return "", fmt.Errorf("Twitter 用户名不正确，请填写 @handle、handle 或用户主页链接")
	}
	return raw, nil
}

func twitterFeedURL(handle string) (string, error) {
	handle, err := normalizeTwitterHandle(handle)
	if err != nil {
		return "", err
	}
	raw := strings.ReplaceAll(defaultTwitterStatusesAPI, "{handle}", url.PathEscape(handle))
	return normalizeRSSURL(raw)
}

func normalizeRSSURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("Feed URL 不正确，仅支持完整的 http/https 地址")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("Feed URL 不能包含用户名或密码")
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (p *RSSWatchPlugin) snapshot(ctx context.Context, feedURL string, settings SettingValues) (rssWatchSnapshot, string, error) {
	feed, err := p.fetch(ctx, feedURL, settings)
	if err != nil {
		return rssWatchSnapshot{}, "", err
	}
	if len(feed.Items) == 0 {
		return rssWatchSnapshot{}, feed.Title, nil
	}
	latest := feed.Items[0]
	return rssWatchSnapshot{ItemID: latest.ID, PublishedAt: latest.PublishedAt}, feed.Title, nil
}

func (p *RSSWatchPlugin) check(ctx context.Context, feedURL, cursor string, publishedAt time.Time, settings SettingValues) (rssWatchChange, error) {
	feed, err := p.fetch(ctx, feedURL, settings)
	if err != nil {
		return rssWatchChange{}, err
	}
	change := rssWatchChange{FeedURL: feedURL, FeedName: feed.Title, Items: []rssWatchItem{}}
	if len(feed.Items) == 0 {
		return change, nil
	}
	latest := feed.Items[0]
	change.Snapshot = rssWatchSnapshot{ItemID: latest.ID, PublishedAt: latest.PublishedAt}
	for index, item := range feed.Items {
		if cursor != "" && item.ID == cursor {
			change.Items = append(change.Items, feed.Items[:index]...)
			break
		}
		if cursor == "" && !publishedAt.IsZero() && item.PublishedAt.After(publishedAt) {
			change.Items = append(change.Items, item)
		}
	}
	if cursor == "" && publishedAt.IsZero() {
		change.Items = append(change.Items, feed.Items...)
	}
	if cursor != "" && len(change.Items) == 0 && latest.ID != cursor {
		if publishedAt.IsZero() {
			change.Items = append(change.Items, latest)
		} else {
			for _, item := range feed.Items {
				if item.PublishedAt.After(publishedAt) {
					change.Items = append(change.Items, item)
				}
			}
		}
	}
	limit := settings.Int(rssWatchSettingItemLimit, 12)
	if limit < 1 {
		limit = 1
	}
	if limit > 30 {
		limit = 30
	}
	if len(change.Items) > limit {
		change.Items = change.Items[:limit]
	}
	for left, right := 0, len(change.Items)-1; left < right; left, right = left+1, right-1 {
		change.Items[left], change.Items[right] = change.Items[right], change.Items[left]
	}
	return change, nil
}

// feedFetchStatusError 把上游的状态码翻译成能照着做的说明。
//
// 光报一个「HTTP 404」没法行动：同样是 404，可能是用户名打错了，也可能是整条
// 路由已经下线。公共 RSSHub 的 X/Twitter 路由就属于后者——它现在把所有请求
// 302 到 google.com/404，任何用户名都是这个结果，重试和改用户名都没有用。
func feedFetchStatusError(requestedURL string, resp *http.Response) error {
	hint := ""
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		if redirectedOffHost(requestedURL, resp) {
			hint = "：整条路由已被上游下线（请求被重定向到了别的站点），换用户名或重试都不会生效"
			if isPublicRSSHubHost(requestedURL) {
				hint = "：公共 RSSHub 实例已经不再提供 X/Twitter 路由，请改成按 Twitter 用户订阅（默认直接读 X 的公开时间线），或换成自建 RSSHub 等仍然可用的 Feed 地址"
			}
		} else {
			hint = "：确认用户名或 Feed 地址是否正确"
		}
	}
	return fmt.Errorf("抓取 Feed 返回 HTTP %d%s", resp.StatusCode, hint)
}

// redirectedOffHost 判断这次请求有没有被跨站重定向。Feed 正常不会跳到别的域名，
// 跳了基本就是「这条路由没了」的兜底页。
func redirectedOffHost(requestedURL string, resp *http.Response) bool {
	if resp == nil || resp.Request == nil || resp.Request.URL == nil {
		return false
	}
	requested, err := url.Parse(requestedURL)
	if err != nil {
		return false
	}
	return !strings.EqualFold(requested.Hostname(), resp.Request.URL.Hostname())
}

func isPublicRSSHubHost(requestedURL string) bool {
	parsed, err := url.Parse(requestedURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "rsshub.app" || strings.HasSuffix(host, ".rsshub.app")
}

func (p *RSSWatchPlugin) fetch(ctx context.Context, feedURL string, settings SettingValues) (parsedFeed, error) {
	feedURL, err := normalizeRSSURL(feedURL)
	if err != nil {
		return parsedFeed{}, err
	}
	timeout := time.Duration(settings.Int(rssWatchSettingTimeout, 20)) * time.Second
	if timeout < 5*time.Second {
		timeout = 5 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, feedURL, nil)
	if err != nil {
		return parsedFeed{}, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.5")
	req.Header.Set("User-Agent", "Diana-RSS/0.1")
	resp, err := p.client.Do(req)
	if err != nil {
		return parsedFeed{}, fmt.Errorf("抓取 Feed 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parsedFeed{}, feedFetchStatusError(feedURL, resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maximumRSSBodyBytes+1))
	if err != nil {
		return parsedFeed{}, fmt.Errorf("读取 Feed 失败: %w", err)
	}
	if len(body) > maximumRSSBodyBytes {
		return parsedFeed{}, fmt.Errorf("Feed 内容超过 %d MiB 限制", maximumRSSBodyBytes>>20)
	}
	// 按内容而不是按域名分流：自建的 FxTwitter 兼容实例也能直接用。
	if looksLikeJSONDocument(body) {
		feed, err := parseTwitterStatusesFeed(body)
		if err != nil {
			return parsedFeed{}, fmt.Errorf("解析 Feed 失败: %w", err)
		}
		// 标题要用订阅的那个账号，不能取第一条的作者：时间线里第一条常常是转推，
		// 那样订阅 @OpenAI 会显示成被转推者的名字。
		if handle := twitterHandleFromStatusesURL(feedURL); handle != "" {
			feed.Title = "@" + handle
		}
		return feed, nil
	}
	feed, err := parseRSSOrAtom(body)
	if err != nil {
		return parsedFeed{}, fmt.Errorf("解析 Feed 失败: %w", err)
	}
	return feed, nil
}

// twitterHandleFromStatusesURL 从默认时间线地址里取回订阅的账号名。自定义模板
// 路径形状不一定一样，取不到就返回空，交由上层保持原样。
func twitterHandleFromStatusesURL(feedURL string) string {
	parsed, err := url.Parse(feedURL)
	if err != nil {
		return ""
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := 0; index+1 < len(segments); index++ {
		if segments[index] != "profile" {
			continue
		}
		if handle := segments[index+1]; twitterHandlePattern.MatchString(handle) {
			return handle
		}
	}
	return ""
}

func looksLikeJSONDocument(body []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(body, " \t\r\n\ufeff"), []byte("{"))
}

// twitterStatusesResponse 是 FxTwitter /2/profile/{handle}/statuses 的响应。
// 只取订阅判断真正用得上的字段，其余（互动数、媒体、引用等）忽略。
type twitterStatusesResponse struct {
	Code    int `json:"code"`
	Results []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		URL  string `json:"url"`
		Text string `json:"text"`
		// raw_text 在上游有两种形态：纯字符串，或 {"text":...,"facets":[...]}。
		// 用 RawMessage 收着再按形态取，换成 string 会直接解码失败。
		RawText   json.RawMessage `json:"raw_text"`
		Timestamp int64           `json:"created_timestamp"`
		CreatedAt string          `json:"created_at"`
		Author    struct {
			Name       string `json:"name"`
			ScreenName string `json:"screen_name"`
		} `json:"author"`
	} `json:"results"`
}

// twitterRawTextValue 兼容 raw_text 的两种形态：纯字符串，或带 facets 的对象。
func twitterRawTextValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var wrapped struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		return strings.TrimSpace(wrapped.Text)
	}
	return ""
}

// parseTwitterStatusesFeed 把 X 时间线转成通用的 feed 条目。
func parseTwitterStatusesFeed(body []byte) (parsedFeed, error) {
	var payload twitterStatusesResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return parsedFeed{}, err
	}
	if payload.Code != 0 && (payload.Code < 200 || payload.Code >= 300) {
		return parsedFeed{}, fmt.Errorf("上游返回 code %d", payload.Code)
	}
	feed := parsedFeed{Items: make([]rssWatchItem, 0, len(payload.Results))}
	for _, entry := range payload.Results {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			text = twitterRawTextValue(entry.RawText)
		}
		published := time.Time{}
		if entry.Timestamp > 0 {
			published = time.Unix(entry.Timestamp, 0).UTC()
		} else if parsed := parseFeedTime(entry.CreatedAt); !parsed.IsZero() {
			published = parsed
		}
		author := strings.TrimSpace(entry.Author.Name)
		if handle := strings.TrimSpace(entry.Author.ScreenName); handle != "" && author == "" {
			author = "@" + handle
		}
		feed.Items = append(feed.Items, rssWatchItem{
			ID:          id,
			Title:       truncateRunes(text, 120),
			Link:        strings.TrimSpace(entry.URL),
			Author:      author,
			Content:     text,
			PublishedAt: published,
		})
	}
	return feed, nil
}

type rssDocument struct {
	Channel struct {
		Title string         `xml:"title"`
		Items []rssFeedEntry `xml:"item"`
	} `xml:"channel"`
	Items []rssFeedEntry `xml:"item"`
}

type rssFeedEntry struct {
	GUID        string            `xml:"guid"`
	Title       string            `xml:"title"`
	Link        string            `xml:"link"`
	Author      string            `xml:"author"`
	Creator     string            `xml:"creator"`
	Description rssElementContent `xml:"description"`
	Encoded     rssElementContent `xml:"encoded"`
	PubDate     string            `xml:"pubDate"`
	Date        string            `xml:"date"`
}

type rssElementContent struct {
	Text string
}

func (content *rssElementContent) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	var builder strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.CharData:
			builder.Write(value)
		case xml.StartElement:
			var nested rssElementContent
			if err := decoder.DecodeElement(&nested, &value); err != nil {
				return err
			}
			if builder.Len() > 0 && nested.Text != "" {
				builder.WriteByte(' ')
			}
			builder.WriteString(nested.Text)
		case xml.EndElement:
			if value.Name == start.Name {
				content.Text = builder.String()
				return nil
			}
		}
	}
}

type atomDocument struct {
	Title   string `xml:"title"`
	Entries []struct {
		ID        string            `xml:"id"`
		Title     string            `xml:"title"`
		Summary   rssElementContent `xml:"summary"`
		Content   rssElementContent `xml:"content"`
		Updated   string            `xml:"updated"`
		Published string            `xml:"published"`
		Author    struct {
			Name string `xml:"name"`
		} `xml:"author"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
	} `xml:"entry"`
}

func parseRSSOrAtom(body []byte) (parsedFeed, error) {
	decoder := func() *xml.Decoder {
		value := xml.NewDecoder(strings.NewReader(string(body)))
		value.CharsetReader = charset.NewReaderLabel
		return value
	}
	var root struct{ XMLName xml.Name }
	if err := decoder().Decode(&root); err != nil {
		return parsedFeed{}, err
	}
	feed := parsedFeed{}
	switch strings.ToLower(root.XMLName.Local) {
	case "rss", "rdf":
		var doc rssDocument
		if err := decoder().Decode(&doc); err != nil {
			return parsedFeed{}, err
		}
		feed.Title = cleanFeedText(doc.Channel.Title, 200)
		entries := doc.Channel.Items
		if len(entries) == 0 {
			entries = doc.Items
		}
		for _, entry := range entries {
			content := firstNonEmpty(entry.Encoded.Text, entry.Description.Text)
			published := parseFeedTime(firstNonEmpty(entry.PubDate, entry.Date))
			item := rssWatchItem{ID: strings.TrimSpace(entry.GUID), Title: cleanFeedText(entry.Title, 500), Link: strings.TrimSpace(entry.Link), Author: cleanFeedText(firstNonEmpty(entry.Creator, entry.Author), 200), Content: cleanFeedText(content, 4000), PublishedAt: published}
			item.ID = stableFeedItemID(item)
			feed.Items = append(feed.Items, item)
		}
	case "feed":
		var doc atomDocument
		if err := decoder().Decode(&doc); err != nil {
			return parsedFeed{}, err
		}
		feed.Title = cleanFeedText(doc.Title, 200)
		for _, entry := range doc.Entries {
			link := ""
			for _, candidate := range entry.Links {
				if link == "" || candidate.Rel == "alternate" {
					link = strings.TrimSpace(candidate.Href)
				}
				if candidate.Rel == "alternate" {
					break
				}
			}
			item := rssWatchItem{ID: strings.TrimSpace(entry.ID), Title: cleanFeedText(entry.Title, 500), Link: link, Author: cleanFeedText(entry.Author.Name, 200), Content: cleanFeedText(firstNonEmpty(entry.Content.Text, entry.Summary.Text), 4000), PublishedAt: parseFeedTime(firstNonEmpty(entry.Published, entry.Updated))}
			item.ID = stableFeedItemID(item)
			feed.Items = append(feed.Items, item)
		}
	default:
		return parsedFeed{}, fmt.Errorf("不支持的 XML 根节点 %q，仅支持 RSS 2.0 与 Atom", root.XMLName.Local)
	}
	sort.SliceStable(feed.Items, func(i, j int) bool {
		left, right := feed.Items[i].PublishedAt, feed.Items[j].PublishedAt
		if left.IsZero() || right.IsZero() || left.Equal(right) {
			return false
		}
		return left.After(right)
	})
	return feed, nil
}

func parseFeedTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC850, time.ANSIC} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func cleanFeedText(raw string, maximum int) string {
	contextNode := &xhtml.Node{Type: xhtml.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := xhtml.ParseFragment(strings.NewReader(raw), contextNode)
	if err == nil {
		var text strings.Builder
		var visit func(*xhtml.Node)
		visit = func(node *xhtml.Node) {
			if node.Type == xhtml.TextNode {
				text.WriteByte(' ')
				text.WriteString(node.Data)
			}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				visit(child)
			}
		}
		for _, node := range nodes {
			visit(node)
		}
		raw = text.String()
	}
	raw = strings.Join(strings.Fields(raw), " ")
	runes := []rune(raw)
	if len(runes) > maximum {
		raw = string(runes[:maximum]) + "…"
	}
	return raw
}

func stableFeedItemID(item rssWatchItem) string {
	if id := strings.TrimSpace(item.ID); id != "" {
		return id
	}
	if link := strings.TrimSpace(item.Link); link != "" {
		return link
	}
	sum := sha256.Sum256([]byte(item.Title + "\x00" + item.Content + "\x00" + item.PublishedAt.UTC().Format(time.RFC3339Nano)))
	return "sha256:" + hex.EncodeToString(sum[:16])
}
