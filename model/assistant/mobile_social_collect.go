// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/mobilectl"
)

var mobileUIChromeTokens = map[string]bool{
	"首页": true, "发现": true, "消息": true, "我": true, "关注": true, "推荐": true,
	"同城": true, "热榜": true, "社区": true, "商城": true, "购物": true, "搜索": true,
	"发布": true, "通知": true, "私信": true, "个人资料": true, "为你推荐": true, "正在关注": true,
	"for you": true, "following": true, "home": true, "search": true, "notifications": true,
}

var pureClockOrMetricPattern = regexp.MustCompile(`^(\d{1,2}:\d{2}|\d+(\.\d+)?[kKwW万]?|\d+%|\+\d+)$`)

// MobileSocialRawItem represents an unfiltered post/story collected from mobile UI or public API.
type MobileSocialRawItem struct {
	Hash        string `json:"hash"`
	Source      string `json:"source"`
	SourceLabel string `json:"source_label"`
	Title       string `json:"title"`
	Summary     string `json:"summary,omitempty"`
	URL         string `json:"url,omitempty"`
	Meta        string `json:"meta,omitempty"`
}

func (p *MobileSocialPlugin) collectAllSources(ctx context.Context, settings SettingValues) ([]MobileSocialRawItem, []string) {
	sources := enabledMobileSocialSources(settings)
	if len(sources) == 0 {
		return nil, nil
	}

	var (
		mu        sync.Mutex
		collected []MobileSocialRawItem
		warnings  []string
		wg        sync.WaitGroup
	)
	addItems := func(items []MobileSocialRawItem, err error, label string) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", label, err))
			return
		}
		collected = append(collected, items...)
	}

	var mobileSources []string
	for _, src := range sources {
		switch src {
		case mobileSourceHackerNews:
			wg.Add(1)
			go func() {
				defer recoverGoroutinePanic("mobile_social.collectHN")
				defer wg.Done()
				items, err := p.collectHackerNews(ctx, 12)
				addItems(items, err, "Hacker News")
			}()
		case mobileSourceV2EX:
			wg.Add(1)
			go func() {
				defer recoverGoroutinePanic("mobile_social.collectV2EX")
				defer wg.Done()
				items, err := p.collectV2EX(ctx, 15)
				addItems(items, err, "V2EX")
			}()
		default:
			if isMobileDeviceSource(src) {
				mobileSources = append(mobileSources, src)
			}
		}
	}

	if len(mobileSources) > 0 {
		wg.Add(1)
		go func() {
			defer recoverGoroutinePanic("mobile_social.collectMobile")
			defer wg.Done()
			items, errs := p.collectMobileSources(ctx, mobileSources, settings)
			mu.Lock()
			collected = append(collected, items...)
			warnings = append(warnings, errs...)
			mu.Unlock()
		}()
	}

	wg.Wait()
	return deduplicateRawItems(collected), warnings
}

func deduplicateRawItems(items []MobileSocialRawItem) []MobileSocialRawItem {
	seen := make(map[string]bool, len(items))
	out := make([]MobileSocialRawItem, 0, len(items))
	for _, item := range items {
		item.Title = strings.TrimSpace(item.Title)
		if item.Title == "" {
			continue
		}
		if item.Hash == "" {
			item.Hash = mobileSocialItemHash(item.Source, item.Title, item.URL)
		}
		if seen[item.Hash] {
			continue
		}
		seen[item.Hash] = true
		out = append(out, item)
	}
	return out
}

func (p *MobileSocialPlugin) collectMobileSources(ctx context.Context, sources []string, settings SettingValues) ([]MobileSocialRawItem, []string) {
	ctrl := p.activeController()
	if ctrl == nil {
		return nil, []string{"手机控制驱动未就绪"}
	}
	screens := settings.Int(mobileSocialSettingSwipeScreens, 2)
	if screens < 1 {
		screens = 1
	}
	if screens > 5 {
		screens = 5
	}
	visionFallback := settings.Bool(mobileSocialSettingVisionFallback, true)
	deviceID := strings.TrimSpace(settings.String(mobileSocialSettingDeviceID, ""))

	var allItems []MobileSocialRawItem
	var warnings []string

	err := ctrl.WithExclusiveSession(ctx, func(ctx context.Context) error {
		dev, err := ctrl.ResolveDevice(ctx, deviceID)
		if err != nil {
			warnings = append(warnings, err.Error())
			return nil
		}
		defer func() {
			homeCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			_, _ = ctrl.PressKey(homeCtx, dev.ID, "home")
		}()

		for _, src := range sources {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			items, srcErr := p.readSingleMobileApp(ctx, ctrl, dev, src, screens, visionFallback)
			if srcErr != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", mobileSocialSourceLabel(src), srcErr))
				continue
			}
			allItems = append(allItems, items...)
		}
		return nil
	})
	if err != nil && ctx.Err() == nil {
		warnings = append(warnings, err.Error())
	}
	return allItems, warnings
}

func (p *MobileSocialPlugin) readSingleMobileApp(
	ctx context.Context,
	ctrl *mobilectl.Controller,
	dev mobilectl.DeviceInfo,
	source string,
	screens int,
	visionFallback bool,
) ([]MobileSocialRawItem, error) {
	appID, fallbackURL := ResolveMobileAppIdentifier(dev.Platform, source)
	if _, err := ctrl.LaunchApp(ctx, dev.ID, appID); err != nil {
		if fallbackURL == "" {
			return nil, err
		}
		if _, urlErr := ctrl.OpenURL(ctx, dev.ID, fallbackURL); urlErr != nil {
			return nil, err
		}
	}
	if err := sleepWithContext(ctx, p.settleDelay); err != nil {
		return nil, err
	}

	var items []MobileSocialRawItem
	label := mobileSocialSourceLabel(source)

	for page := 0; page < screens; page++ {
		if page > 0 {
			if _, err := ctrl.SwipeDirection(ctx, dev.ID, "up"); err != nil {
				break
			}
			if err := sleepWithContext(ctx, p.settleDelay/2); err != nil {
				break
			}
		}
		var pageCards []string
		if snap, err := ctrl.Elements(ctx, dev.ID); err == nil {
			if page == 0 && looksLikeStartupModal(snap.Elements) {
				_, _ = ctrl.PressKey(ctx, dev.ID, "back")
				_ = sleepWithContext(ctx, 800*time.Millisecond)
				if snapAfter, errAfter := ctrl.Elements(ctx, dev.ID); errAfter == nil {
					snap = snapAfter
				}
			}
			pageCards = extractFeedCardsFromElements(snap.Elements, snap.Height)
		}
		totalRunes := 0
		for _, card := range pageCards {
			totalRunes += utf8.RuneCountInString(card)
		}
		if visionFallback && totalRunes < 36 {
			if _, pngBytes, err := ctrl.Screenshot(ctx, dev.ID); err == nil && len(pngBytes) > 0 {
				if visionCards := p.extractCardsFromScreenshot(ctx, label, pngBytes); len(visionCards) > 0 {
					pageCards = append(pageCards, visionCards...)
				}
			}
		}
		for _, card := range pageCards {
			title, summary := splitCardTitleAndSummary(card)
			if title == "" {
				continue
			}
			items = append(items, MobileSocialRawItem{
				Hash:        mobileSocialItemHash(source, title, ""),
				Source:      source,
				SourceLabel: label,
				Title:       title,
				Summary:     summary,
			})
		}
	}
	return items, nil
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func looksLikeStartupModal(elements []mobilectl.Element) bool {
	if len(elements) == 0 || len(elements) > 16 {
		return false
	}
	for _, el := range elements {
		txt := el.DisplayText()
		if strings.Contains(txt, "已签到") || strings.Contains(txt, "青少年模式") ||
			strings.Contains(txt, "跳过广告") || strings.Contains(txt, "版本更新") ||
			strings.Contains(txt, "以后再说") || strings.Contains(txt, "我知道了") {
			return true
		}
	}
	return false
}

// extractFeedCardsFromElements groups visible accessibility elements into feed post snippets,
// ignoring top status bar and bottom navigation bar chrome.
func extractFeedCardsFromElements(elements []mobilectl.Element, screenHeight int) []string {
	topCutoff := 0
	bottomCutoff := 0
	if screenHeight >= 400 {
		topCutoff = int(float64(screenHeight) * 0.06)
		bottomCutoff = int(float64(screenHeight) * 0.91)
	}
	var lines []string
	seenLine := map[string]bool{}
	for _, elem := range elements {
		if topCutoff > 0 && elem.Y+elem.Height <= topCutoff {
			continue
		}
		if bottomCutoff > 0 && elem.Y >= bottomCutoff {
			continue
		}
		txt := strings.Join(strings.Fields(elem.DisplayText()), " ")
		if txt == "" || utf8.RuneCountInString(txt) < 4 {
			continue
		}
		if mobileUIChromeTokens[strings.ToLower(txt)] || pureClockOrMetricPattern.MatchString(txt) {
			continue
		}
		if seenLine[txt] {
			continue
		}
		seenLine[txt] = true
		lines = append(lines, txt)
	}
	// Each substantial element (>12 runes) is treated as a distinct post snippet,
	// while adjacent short author/tag lines merge into the following body line.
	var cards []string
	var prefix string
	for _, line := range lines {
		n := utf8.RuneCountInString(line)
		if n < 12 {
			if prefix == "" {
				prefix = line
			} else {
				prefix = truncateRunes(prefix+" · "+line, 40)
			}
			continue
		}
		if prefix != "" {
			cards = append(cards, prefix+"："+line)
			prefix = ""
		} else {
			cards = append(cards, line)
		}
	}
	if prefix != "" && utf8.RuneCountInString(prefix) >= 10 {
		cards = append(cards, prefix)
	}
	return cards
}

func splitCardTitleAndSummary(card string) (string, string) {
	card = strings.TrimSpace(card)
	if card == "" {
		return "", ""
	}
	runes := []rune(card)
	if len(runes) <= 64 {
		return card, ""
	}
	return strings.TrimSpace(string(runes[:64])) + "…", truncateRunes(card, 260)
}

func (p *MobileSocialPlugin) extractCardsFromScreenshot(ctx context.Context, sourceLabel string, pngBytes []byte) []string {
	p.mu.Lock()
	rt := p.runtime
	p.mu.Unlock()
	if rt == nil {
		return nil
	}
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	visionCtx := withLLMUsagePurpose(ctx, PurposeMediaParse)
	raw, err := rt.runLLMProviderForGroup(visionCtx, llm.GroupVision, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(visionCtx, llm.GenerateRequest{
			Messages: []llm.Message{
				{
					Role:    llm.RoleSystem,
					Content: "你负责读取手机社媒信息流截图。提取画面中可见的每条独立帖子或新闻（忽略顶部状态栏、底部导航栏和明显的广告）。只输出 JSON：{\"posts\":[\"作者/标题：内容摘要\"]}。",
				},
				{
					Role:    llm.RoleUser,
					Content: "当前应用：" + sourceLabel,
					Parts: []llm.ContentPart{
						{Type: llm.ContentPartText, Text: "提取当前屏幕里的帖子（最多 6 条）："},
						{Type: llm.ContentPartImageURL, ImageURL: "data:image/png;base64," + b64},
					},
				},
			},
		})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		return nil
	}
	clean := strings.TrimSpace(stripJSONCodeFence(raw))
	start, end := strings.Index(clean, "{"), strings.LastIndex(clean, "}")
	if start < 0 || end <= start {
		return nil
	}
	var parsed struct {
		Posts []string `json:"posts"`
	}
	if json.Unmarshal([]byte(clean[start:end+1]), &parsed) != nil {
		return nil
	}
	var out []string
	for _, post := range parsed.Posts {
		if post = strings.TrimSpace(post); utf8.RuneCountInString(post) >= 8 {
			out = append(out, post)
		}
	}
	return out
}

func (p *MobileSocialPlugin) collectHackerNews(ctx context.Context, limit int) ([]MobileSocialRawItem, error) {
	if limit <= 0 {
		limit = 10
	}
	idsURL := strings.TrimRight(p.hnBaseURL, "/") + "/topstories.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, idsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var ids []int64
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ids); err != nil {
		return nil, err
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}

	type hnStory struct {
		ID          int64  `json:"id"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		Score       int    `json:"score"`
		Descendants int    `json:"descendants"`
		Type        string `json:"type"`
	}

	results := make([]MobileSocialRawItem, len(ids))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for idx, storyID := range ids {
		wg.Add(1)
		go func(i int, id int64) {
			defer recoverGoroutinePanic("mobile_social.hnItem")
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			itemURL := fmt.Sprintf("%s/item/%d.json", strings.TrimRight(p.hnBaseURL, "/"), id)
			itemReq, err := http.NewRequestWithContext(ctx, http.MethodGet, itemURL, nil)
			if err != nil {
				return
			}
			itemResp, err := p.httpClient.Do(itemReq)
			if err != nil {
				return
			}
			defer itemResp.Body.Close()
			if itemResp.StatusCode != http.StatusOK {
				return
			}
			var s hnStory
			if json.NewDecoder(io.LimitReader(itemResp.Body, 512<<10)).Decode(&s) != nil || strings.TrimSpace(s.Title) == "" {
				return
			}
			link := strings.TrimSpace(s.URL)
			if link == "" {
				link = "https://news.ycombinator.com/item?id=" + strconv.FormatInt(s.ID, 10)
			}
			meta := fmt.Sprintf("HN 热度 %d · 评论 %d", s.Score, s.Descendants)
			results[i] = MobileSocialRawItem{
				Hash:        mobileSocialItemHash(mobileSourceHackerNews, s.Title, link),
				Source:      mobileSourceHackerNews,
				SourceLabel: "Hacker News",
				Title:       strings.TrimSpace(s.Title),
				Summary:     meta,
				URL:         link,
				Meta:        meta,
			}
		}(idx, storyID)
	}
	wg.Wait()

	var out []MobileSocialRawItem
	for _, item := range results {
		if item.Title != "" {
			out = append(out, item)
		}
	}
	return out, nil
}

func (p *MobileSocialPlugin) collectV2EX(ctx context.Context, limit int) ([]MobileSocialRawItem, error) {
	if limit <= 0 {
		limit = 15
	}
	apiURL := strings.TrimRight(p.v2exBaseURL, "/") + "/api/topics/hot.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "DianaBot/1.0")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var topics []struct {
		ID      int64  `json:"id"`
		Title   string `json:"title"`
		Content string `json:"content"`
		URL     string `json:"url"`
		Replies int    `json:"replies"`
		Node    struct {
			Title string `json:"title"`
		} `json:"node"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&topics); err != nil {
		return nil, err
	}
	if len(topics) > limit {
		topics = topics[:limit]
	}
	out := make([]MobileSocialRawItem, 0, len(topics))
	for _, topic := range topics {
		title := strings.TrimSpace(topic.Title)
		if title == "" {
			continue
		}
		snippet := truncateRunes(strings.Join(strings.Fields(topic.Content), " "), 180)
		meta := fmt.Sprintf("V2EX/%s · %d 回复", firstNonEmpty(strings.TrimSpace(topic.Node.Title), "热议"), topic.Replies)
		summary := meta
		if snippet != "" {
			summary = meta + "：" + snippet
		}
		out = append(out, MobileSocialRawItem{
			Hash:        mobileSocialItemHash(mobileSourceV2EX, title, topic.URL),
			Source:      mobileSourceV2EX,
			SourceLabel: "V2EX",
			Title:       title,
			Summary:     summary,
			URL:         strings.TrimSpace(topic.URL),
			Meta:        meta,
		})
	}
	return out, nil
}
