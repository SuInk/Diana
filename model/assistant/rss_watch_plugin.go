// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

	rssWatchSettingTwitterTemplate = "twitter_rss_template"
	rssWatchSettingTimeout         = "timeout_seconds"
	rssWatchSettingItemLimit       = "judge_item_limit"

	// legacyTwitterRSSTemplate 是 0.1.x 默认写死的 RSSHub 公共实例。X 已经改成
	// 内置抓取，这个值再出现就只能当「没配置」处理，否则老用户会被继续绑在
	// 一个随时会挂的公共中转上。
	legacyTwitterRSSTemplate = "https://rsshub.app/twitter/user/{handle}"

	maximumRSSBodyBytes = 4 << 20

	// rssWatchBrowserUserAgent 是原生抓取使用的桌面 Chrome UA：X 会按 UA 决定
	// 是返回时间线数据还是一段风控页面。
	rssWatchBrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

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
		Description: "订阅 X 用户或任意 RSS/Atom：X 由内置抓取器直接读取，不依赖 RSSHub；发现新内容后由 LLM 判断是否需要通知，并生成实际回复。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:https", "task:persistent", "message:send", "llm:generate"},
		Settings: []PluginSettingSpec{
			{
				Key:         rssWatchSettingTwitterTemplate,
				Label:       "X 自定义 Feed 模板",
				Description: "留空即用内置抓取（syndication 接口，无需账号）。只有想走自建 RSSHub、Nitter 这类中转时才填，例如 https://rss.example.com/twitter/user/{handle}，其中 {handle} 会被替换成用户名。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
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
				Description: "一次交给 LLM 判断的最新条目数量；Feed 游标仍推进到已读取的最新条目。",
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

// customFeedTemplate 返回该平台配置的自定义 Feed 模板；0.1.x 写死的 RSSHub
// 公共实例按未配置处理。
func customFeedTemplate(spec rssWatchPlatformSpec, settings SettingValues) string {
	if spec.TemplateKey == "" {
		return ""
	}
	template := strings.TrimSpace(settings.String(spec.TemplateKey, ""))
	if template == legacyTwitterRSSTemplate {
		return ""
	}
	return template
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

func (p *RSSWatchPlugin) snapshot(ctx context.Context, source rssWatchSource, settings SettingValues) (rssWatchSnapshot, string, error) {
	feed, err := p.fetch(ctx, source, settings)
	if err != nil {
		return rssWatchSnapshot{}, "", err
	}
	if len(feed.Items) == 0 {
		return rssWatchSnapshot{}, feed.Title, nil
	}
	latest := feed.Items[0]
	return rssWatchSnapshot{ItemID: latest.ID, PublishedAt: latest.PublishedAt}, feed.Title, nil
}

func (p *RSSWatchPlugin) check(ctx context.Context, source rssWatchSource, cursor string, publishedAt time.Time, settings SettingValues) (rssWatchChange, error) {
	feed, err := p.fetch(ctx, source, settings)
	if err != nil {
		return rssWatchChange{}, err
	}
	change := rssWatchChange{FeedURL: source.URL, FeedName: feed.Title, Items: []rssWatchItem{}}
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

// fetch 按平台取内容：自定义模板优先，其次是平台内置抓取，最后按普通 RSS/Atom 解析。
func (p *RSSWatchPlugin) fetch(ctx context.Context, source rssWatchSource, settings SettingValues) (parsedFeed, error) {
	spec, ok := rssWatchPlatform(source.Platform)
	if !ok {
		return parsedFeed{}, fmt.Errorf("不支持的订阅平台 %q", source.Platform)
	}
	if template := customFeedTemplate(spec, settings); template != "" {
		feedURL, err := applyFeedTemplate(template, source.Target)
		if err != nil {
			return parsedFeed{}, err
		}
		return p.fetchFeedURL(ctx, feedURL, settings)
	}
	if spec.fetch != nil {
		return spec.fetch(ctx, p, source, settings)
	}
	return p.fetchFeedURL(ctx, source.URL, settings)
}

func (p *RSSWatchPlugin) fetchFeedURL(ctx context.Context, feedURL string, settings SettingValues) (parsedFeed, error) {
	body, err := p.get(ctx, feedURL, settings, map[string]string{
		"Accept":     "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.5",
		"User-Agent": "Diana-RSS/0.2",
	})
	if err != nil {
		return parsedFeed{}, err
	}
	feed, err := parseRSSOrAtom(body)
	if err != nil {
		return parsedFeed{}, fmt.Errorf("解析 Feed 失败: %w", err)
	}
	return feed, nil
}

// get 是所有原生抓取共用的 HTTP 读取：统一超时、统一体积上限、统一错误措辞。
func (p *RSSWatchPlugin) get(ctx context.Context, rawURL string, settings SettingValues, headers map[string]string) ([]byte, error) {
	rawURL, err := normalizeRSSURL(rawURL)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, rssWatchTimeout(settings))
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		if strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("抓取 Feed 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("抓取 Feed 返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maximumRSSBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取 Feed 失败: %w", err)
	}
	if len(body) > maximumRSSBodyBytes {
		return nil, fmt.Errorf("Feed 内容超过 %d MiB 限制", maximumRSSBodyBytes>>20)
	}
	return body, nil
}

func rssWatchTimeout(settings SettingValues) time.Duration {
	timeout := time.Duration(settings.Int(rssWatchSettingTimeout, 20)) * time.Second
	if timeout < 5*time.Second {
		timeout = 5 * time.Second
	}
	return timeout
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
	sortFeedItemsByPublishedAt(feed.Items)
	return feed, nil
}

// sortFeedItemsByPublishedAt 把条目按发布时间从新到旧排；缺时间的条目保持原顺序，
// 因为各平台接口本来就是新的在前。
func sortFeedItemsByPublishedAt(items []rssWatchItem) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i].PublishedAt, items[j].PublishedAt
		if left.IsZero() || right.IsZero() || left.Equal(right) {
			return false
		}
		return left.After(right)
	})
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
