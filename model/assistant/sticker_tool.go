// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	dianaStickerToolName             = "sticker"
	maximumStickerDescriptionLookups = 128
	stickerDescriptionWorkers        = 3
	// 刚发过的表情包在这段时间内往后排，随机补位时也先跳过。
	stickerRecentSendWindow = 12 * time.Hour
	stickerRecentSendFactor = 0.3
	// 命中的候选先取「返回数量 × 这个倍数」进池子，再按分数加权抽，排名靠前的更容易被抽中。
	stickerMatchedPoolFactor = 2
	stickerBackgroundTagTTL  = 3 * time.Minute
	stickerSendRateWindow    = time.Hour
)

type dianaStickerTool struct {
	runtime  *Runtime
	event    MessageEvent
	settings SettingValues
	searchMu sync.Mutex
	searched map[string]stickerCandidate
	// sentThisTurn 是这一轮已经发出（或正在发）的张数；工具实例每轮新建。
	sentThisTurn int
}

// StickerHistoryQuery is the storage boundary for the optional cross-conversation library.
// Shared reads remain inside one profile/namespace and never expose source identifiers.
type StickerHistoryQuery struct {
	Session          string
	ContextNamespace string
	ProfileID        string
	ShareGroups      bool
	SharePrivate     bool
	Limit            int
}

type StickerHistoryStore interface {
	ListRecentStickerEvents(context.Context, StickerHistoryQuery) ([]MessageEvent, error)
}

type stickerCandidate struct {
	ID            string
	Summary       string
	Description   string
	Tags          []string
	Tagged        bool
	FromAssets    bool
	Path          string
	Hash          string
	MessageID     string
	EventTime     int64
	LastSentAt    int64
	Score         float64
	SemanticScore int
	SourceEvent   MessageEvent
	SharedGroup   bool
	SharedPrivate bool
}

type stickerSearchItem struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty"`
	Matched     bool     `json:"matched"`
	MessageID   string   `json:"-"`
	Scope       string   `json:"scope"`
}

type stickerToolResult struct {
	OK         bool                `json:"ok"`
	Action     string              `json:"action"`
	Message    string              `json:"message"`
	Query      string              `json:"query,omitempty"`
	Candidates []stickerSearchItem `json:"candidates,omitempty"`
	Sent       *stickerSearchItem  `json:"sent,omitempty"`
}

func newDianaStickerTool(runtime *Runtime, event MessageEvent, settings SettingValues) *dianaStickerTool {
	return &dianaStickerTool{runtime: runtime, event: event, settings: settings, searched: map[string]stickerCandidate{}}
}

func (t *dianaStickerTool) Name() string { return dianaStickerToolName }

func (t *dianaStickerTool) Description() string {
	return `从 Diana 持久表情资产库中检索并发送一张表情包。用户明确要“发表情包”、希望用表情回应，或当前语境适合只用表情包回应时使用。` +
		`先用 operation=search 检索：query 写 2 到 6 个空格分隔的短关键词，覆盖情绪、动作、场景和同义说法，例如“安慰 抱抱 摸头 心疼”，不要写整句。` +
		`再结合候选的名称、标签与简介挑最贴合当前语境的一张，用 operation=send 原样传回 sticker_id；matched=false 的候选只是随机补位，都不合适就不发。` +
		`发送由工具完成，成功后不要声称还要上传，也不要把候选的内部 id 告诉用户。不得把普通历史图片当表情包发送。`
}

func (t *dianaStickerTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation":  toolEnumParam("search 只返回候选；send 发送一张。", "search", "send"),
		"query":      toolStringParam("search 的检索关键词，空格分隔，例如“无语 翻白眼 离谱”“开心 庆祝 撒花”；可留空随机看一批候选。"),
		"sticker_id": toolStringParam("search 返回的候选 id。只能原样使用本轮当前会话检索得到的 id。"),
	})
}

func (t *dianaStickerTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("sticker tool: runtime is not configured")
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	query := strings.TrimSpace(configToolString(input, "query"))
	stickerID := strings.TrimSpace(configToolString(input, "sticker_id"))

	switch operation {
	case "search":
		if reason := t.sendLimitReason(time.Now()); reason != "" {
			return marshalStickerResult(stickerToolResult{OK: true, Action: "limited", Message: reason, Query: query})
		}
		candidates, err := t.candidates(ctx, query)
		if err != nil {
			return "", err
		}
		limit := t.settings.Int(stickerSettingSearchResults, 8)
		now := time.Now().Unix()
		picked, matched := selectStickerCandidates(candidates, limit, now, secureRandomIndex)
		t.enrichCandidateDescriptions(ctx, picked)
		t.tagCandidatesInBackground(ctx, picked)
		t.rememberSearchCandidates(picked)
		items := stickerSearchItems(picked)
		var message string
		switch {
		case len(items) == 0:
			message = "本轮没有可用候选；继续正常回应，不要向用户提及内部图库、索引、搜索或工具状态，也不要声称已经发送。"
		case matched == 0 && query != "":
			message = fmt.Sprintf("没有关键词命中，以下 %d 个是随机候选；有贴合当前语境的再发，都不合适就不发，也可以换一组关键词再搜。", len(items))
		case matched < len(items):
			message = fmt.Sprintf("命中 %d 个候选，另有 %d 个随机补位（matched=false）；请按当前语境结合名称、标签和简介选择。", matched, len(items)-matched)
		default:
			message = fmt.Sprintf("找到 %d 个候选；请按当前语境结合名称、标签和简介选择。", len(items))
		}
		return marshalStickerResult(stickerToolResult{OK: true, Action: "searched", Message: message, Query: query, Candidates: items})
	case "send":
		var selected *stickerCandidate
		if stickerID != "" {
			if candidate, ok := t.searchedCandidate(stickerID); ok {
				selected = &candidate
			}
			if selected == nil {
				return marshalStickerResult(stickerToolResult{Action: "not_sent", Message: "这个 sticker_id 不属于本轮搜索候选；请重新 search。", Query: query})
			}
		} else {
			candidates, err := t.candidates(ctx, query)
			if err != nil {
				return "", err
			}
			picked, matched := selectStickerCandidates(candidates, 1, time.Now().Unix(), secureRandomIndex)
			if len(picked) == 0 || (query != "" && matched == 0) {
				return marshalStickerResult(stickerToolResult{Action: "not_sent", Message: "没有足够匹配的候选；请先 search 查看标签和简介，再传 sticker_id。本轮不发送表情，也不要向用户解释内部图库或搜索状态。", Query: query})
			}
			selected = &picked[0]
		}
		if _, err := os.Stat(selected.Path); err != nil {
			return "", fmt.Errorf("表情包缓存文件不可用: %w", err)
		}
		if selected.Hash != "" && !stickerFileMatchesHash(selected.Path, selected.Hash) {
			return "", fmt.Errorf("表情包缓存内容校验失败")
		}
		release, reason := t.reserveSend(time.Now())
		if reason != "" {
			return marshalStickerResult(stickerToolResult{Action: "limited", Message: reason, Query: query})
		}
		label := "表情包"
		if name := firstNonEmpty(selected.Summary, truncateRunes(selected.Description, 60)); name != "" {
			label += "：" + name
		}
		if err := t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{ImageURLs: []string{selected.Path}, ImageLabels: []string{label}})); err != nil {
			release()
			return "", fmt.Errorf("发送表情包失败: %w", err)
		}
		t.recordSent(ctx, *selected)
		item := stickerSearchItems([]stickerCandidate{*selected})[0]
		return marshalStickerResult(stickerToolResult{OK: true, Action: "sent", Message: "表情包已发送。", Query: query, Sent: &item})
	default:
		return "", fmt.Errorf("operation 必须是 search 或 send")
	}
}

func (t *dianaStickerTool) rememberSearchCandidates(candidates []stickerCandidate) {
	t.searchMu.Lock()
	defer t.searchMu.Unlock()
	t.searched = make(map[string]stickerCandidate, len(candidates))
	for _, candidate := range candidates {
		t.searched[candidate.ID] = candidate
	}
}

func (t *dianaStickerTool) searchedCandidate(id string) (stickerCandidate, bool) {
	t.searchMu.Lock()
	defer t.searchMu.Unlock()
	candidate, ok := t.searched[strings.TrimSpace(id)]
	return candidate, ok
}

// stickerSendLimiter 是按会话的滑动窗口计数。先占位再发送，发送失败退回占位，
// 并发的两轮不会一起越过上限。
type stickerSendLimiter struct {
	mu   sync.Mutex
	sent map[string][]time.Time
}

func (l *stickerSendLimiter) recentLocked(session string, now time.Time) []time.Time {
	kept := l.sent[session][:0]
	for _, at := range l.sent[session] {
		if now.Sub(at) < stickerSendRateWindow {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(l.sent, session)
		return nil
	}
	l.sent[session] = kept
	return kept
}

// full 报告会话是否已到上限，到了的话还要等多久才会空出一张。
func (l *stickerSendLimiter) full(session string, now time.Time, limit int) (bool, time.Duration) {
	if limit <= 0 {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.recentLocked(session, now)
	if len(recent) < limit {
		return false, 0
	}
	return true, stickerSendRateWindow - now.Sub(recent[len(recent)-limit])
}

func (l *stickerSendLimiter) reserve(session string, now time.Time, limit int) (func(), bool) {
	if limit <= 0 {
		return func() {}, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sent == nil {
		l.sent = map[string][]time.Time{}
	}
	if len(l.recentLocked(session, now)) >= limit {
		return nil, false
	}
	l.sent[session] = append(l.sent[session], now)
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		times := l.sent[session]
		for index := len(times) - 1; index >= 0; index-- {
			if times[index].Equal(now) {
				l.sent[session] = append(times[:index], times[index+1:]...)
				return
			}
		}
	}, true
}

const stickerLimitedHint = "这轮用文字回应，不要向用户提及表情包上限、图库或工具状态。"

// sendLimitReason 在这一轮或这个会话已到发送上限时返回给 Agent 的说明，没到返回空串。
func (t *dianaStickerTool) sendLimitReason(now time.Time) string {
	t.searchMu.Lock()
	sent := t.sentThisTurn
	t.searchMu.Unlock()
	if turnLimit := t.settings.Int(stickerSettingTurnLimit, 1); sent >= turnLimit {
		return fmt.Sprintf("这一轮已经发了 %d 张表情包，到了单轮上限；%s", sent, stickerLimitedHint)
	}
	hourly := t.settings.Int(stickerSettingHourlyLimit, 10)
	if full, wait := t.runtime.stickerSends.full(sessionKey(t.event), now, hourly); full {
		return fmt.Sprintf("这个会话最近一小时已发 %d 张表情包，到了上限，约 %d 分钟后才能再发；%s", hourly, int(math.Ceil(wait.Minutes())), stickerLimitedHint)
	}
	return ""
}

// reserveSend 为一次发送占住单轮和每小时的名额；到上限时返回说明。发送失败要调用 release 退回。
func (t *dianaStickerTool) reserveSend(now time.Time) (func(), string) {
	t.searchMu.Lock()
	defer t.searchMu.Unlock()
	turnLimit := t.settings.Int(stickerSettingTurnLimit, 1)
	if t.sentThisTurn >= turnLimit {
		return nil, fmt.Sprintf("这一轮已经发了 %d 张表情包，到了单轮上限；%s", t.sentThisTurn, stickerLimitedHint)
	}
	hourly := t.settings.Int(stickerSettingHourlyLimit, 10)
	releaseSlot, ok := t.runtime.stickerSends.reserve(sessionKey(t.event), now, hourly)
	if !ok {
		_, wait := t.runtime.stickerSends.full(sessionKey(t.event), now, hourly)
		return nil, fmt.Sprintf("这个会话最近一小时已发 %d 张表情包，到了上限，约 %d 分钟后才能再发；%s", hourly, int(math.Ceil(wait.Minutes())), stickerLimitedHint)
	}
	t.sentThisTurn++
	return func() {
		releaseSlot()
		t.searchMu.Lock()
		t.sentThisTurn--
		t.searchMu.Unlock()
	}, ""
}

// recordSent 记下这次发送，下次检索时刚发过的往后排。记录失败不影响已经发出去的表情。
func (t *dianaStickerTool) recordSent(ctx context.Context, candidate stickerCandidate) {
	if candidate.Hash == "" {
		return
	}
	t.runtime.mu.RLock()
	store := t.runtime.messageStore
	t.runtime.mu.RUnlock()
	usage, ok := store.(StickerUsageStore)
	if !ok {
		return
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := usage.RecordStickerSent(saveCtx, sessionKey(t.event), candidate.Hash, time.Now().Unix()); err != nil {
		log.Printf("diana sticker usage record failed: %v", err)
	}
}

func stickerFileMatchesHash(path, expected string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), strings.TrimSpace(expected))
}

func (t *dianaStickerTool) candidates(ctx context.Context, query string) ([]stickerCandidate, error) {
	limit := t.settings.Int(stickerSettingHistoryLimit, 1000)
	shareGroups := t.settings.Bool(stickerSettingCrossGroup, false)
	sharePrivate := t.settings.Bool(stickerSettingCrossPrivate, false)
	assetQuery := StickerHistoryQuery{
		Session:          sessionKey(t.event),
		ContextNamespace: strings.TrimSpace(t.event.ContextNamespace),
		ProfileID:        strings.TrimSpace(t.event.ProfileID),
		ShareGroups:      shareGroups,
		SharePrivate:     sharePrivate,
		Limit:            limit,
	}
	t.runtime.mu.RLock()
	store := t.runtime.messageStore
	inMemory := append([]MessageEvent(nil), t.runtime.history[sessionKey(t.event)]...)
	t.runtime.mu.RUnlock()
	includeGeneric := t.settings.Bool(stickerSettingIncludeGeneric, true)
	semanticScores := t.semanticCandidateScores(ctx, query, shareGroups)
	var candidates []stickerCandidate
	if assetStore, ok := store.(StickerAssetStore); ok {
		assets, err := assetStore.ListStickerAssets(ctx, assetQuery)
		if err != nil {
			return nil, fmt.Errorf("读取表情包资产失败: %w", err)
		}
		candidates = stickerCandidatesFromAssets(assets, assetQuery.Session, includeGeneric, semanticScores)
	} else {
		events := inMemory
		if store != nil {
			var loaded []MessageEvent
			var err error
			if stickerStore, ok := store.(StickerHistoryStore); ok {
				loaded, err = stickerStore.ListRecentStickerEvents(ctx, assetQuery)
			} else {
				loaded, err = store.ListRecentMessageEvents(ctx, assetQuery.Session, limit)
			}
			if err != nil {
				return nil, fmt.Errorf("读取表情包历史失败: %w", err)
			}
			events = loaded
		}
		if len(events) > limit {
			events = events[len(events)-limit:]
		}
		candidates = stickerCandidatesFromEvents(events, assetQuery.Session, includeGeneric, semanticScores)
		// 资产库一次查询就带出了简介；只有退回到聊天记录时才逐张回查。
		for index := range candidates {
			if index >= maximumStickerDescriptionLookups {
				break
			}
			lines := t.runtime.historyImageCachedSegmentDescriptions(ctx, []MessageSegment{{Type: "image", Data: map[string]string{
				"cached_file":         candidates[index].Path,
				imageContentSHA256Key: candidates[index].Hash,
			}}})
			if len(lines) > 0 {
				candidates[index].Description = strings.TrimSpace(strings.TrimPrefix(lines[0], "图片1摘要="))
				if candidates[index].Description == "尚无缓存描述" {
					candidates[index].Description = ""
				}
			}
		}
	}
	rankStickerCandidates(candidates, query, time.Now().Unix())
	return candidates, nil
}

func stickerCandidatesFromAssets(assets []StickerAsset, currentSession string, includeGeneric bool, semanticScores map[string]int) []stickerCandidate {
	candidates := make([]stickerCandidate, 0, len(assets))
	seen := map[string]bool{}
	for _, asset := range assets {
		summary := normalizeStickerSummary(asset.Summary)
		if summary == "" {
			summary = "动画表情"
		}
		if !includeGeneric && summary == "动画表情" {
			continue
		}
		path := normalizedLocalImagePath(asset.Path)
		hash := strings.ToLower(strings.TrimSpace(asset.ContentSHA256))
		if path == "" || !validSHA256(hash) || seen[hash] {
			continue
		}
		seen[hash] = true
		source := MessageEvent{
			ProfileID: asset.ProfileID, ContextNamespace: asset.ContextNamespace, Kind: asset.Kind,
			GroupID: asset.GroupID, UserID: asset.UserID, MessageID: asset.MessageID, Time: asset.EventTime,
			Segments: []MessageSegment{{Type: "image", Data: map[string]string{
				"summary": asset.Summary, "cached_file": path, "cached_mime": asset.MIME, imageContentSHA256Key: hash,
			}}},
		}
		candidates = append(candidates, stickerCandidate{
			ID: hash[:24], Summary: summary, Path: path, Hash: hash, MessageID: asset.MessageID, EventTime: asset.EventTime,
			Description: strings.TrimSpace(firstNonEmpty(asset.Gist, asset.Description)),
			Tags:        asset.Tags, Tagged: asset.Tagged, FromAssets: true, LastSentAt: asset.LastSentAt,
			SemanticScore: semanticScores[stickerCandidateEventKey(source)], SourceEvent: source,
			SharedGroup:   asset.Kind == EventKindGroup && asset.Session != currentSession,
			SharedPrivate: asset.Kind == EventKindPrivate && asset.Session != currentSession,
		})
	}
	return candidates
}

func stickerCandidatesFromEvents(events []MessageEvent, currentSession string, includeGeneric bool, semanticScores map[string]int) []stickerCandidate {
	seen := map[string]bool{}
	candidates := make([]stickerCandidate, 0)
	for eventIndex := len(events) - 1; eventIndex >= 0; eventIndex-- {
		event := events[eventIndex]
		for segmentIndex, segment := range event.Segments {
			summary, ok := StickerSegmentLabel(segment)
			if !ok || (!includeGeneric && summary == "动画表情") {
				continue
			}
			path := normalizedLocalImagePath(segment.Data["cached_file"])
			if path == "" {
				continue
			}
			hash := strings.ToLower(strings.TrimSpace(segment.Data[imageContentSHA256Key]))
			if !validSHA256(hash) {
				hash = ""
			}
			key := hash
			if key == "" {
				key = path
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			id := fmt.Sprintf("%s-%d", strings.TrimSpace(event.MessageID), segmentIndex+1)
			if hash != "" {
				id = hash[:24]
			}
			candidates = append(candidates, stickerCandidate{
				ID: id, Summary: summary, Path: path, Hash: hash, MessageID: event.MessageID, EventTime: event.Time,
				SemanticScore: semanticScores[stickerCandidateEventKey(event)], SourceEvent: event,
				SharedGroup:   event.Kind == EventKindGroup && sessionKey(event) != currentSession,
				SharedPrivate: event.Kind == EventKindPrivate && sessionKey(event) != currentSession,
			})
		}
	}
	return candidates
}

// stickerQueryTerms 把查询拆成关键词：空白和常见标点都算分隔。
func stickerQueryTerms(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return unicode.IsSpace(r) || (unicode.IsPunct(r) && r != '_' && r != '-') || strings.ContainsRune("、，。；：！？|/", r)
	})
	seen := map[string]bool{}
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != "" && !seen[field] {
			seen[field] = true
			terms = append(terms, field)
		}
	}
	return terms
}

type stickerTerm struct {
	text   string
	weight float64
}

// rankStickerCandidates 按关键词打分：名称和标签命中权重高于简介，越少见的词分越高（IDF），
// 再叠加语义检索分。整句一个词都没命中时，把长词拆成两字片段低权重再试一次，兼容旧的整句查询。
// 机器人刚在本会话发过的往后排。
func rankStickerCandidates(candidates []stickerCandidate, query string, now int64) {
	type fields struct{ strong, weak string }
	docs := make([]fields, len(candidates))
	for index, candidate := range candidates {
		docs[index] = fields{
			strong: strings.ToLower(candidate.Summary + "\n" + strings.Join(candidate.Tags, "\n")),
			weak:   strings.ToLower(candidate.Description),
		}
	}
	documentFrequency := func(term string) int {
		count := 0
		for _, doc := range docs {
			if strings.Contains(doc.strong, term) || strings.Contains(doc.weak, term) {
				count++
			}
		}
		return count
	}
	var terms []stickerTerm
	for _, term := range stickerQueryTerms(query) {
		if documentFrequency(term) > 0 || len([]rune(term)) < 4 {
			terms = append(terms, stickerTerm{text: term, weight: 1})
			continue
		}
		runes := []rune(term)
		for start := 0; start+2 <= len(runes); start++ {
			terms = append(terms, stickerTerm{text: string(runes[start : start+2]), weight: 0.3})
		}
	}
	idf := make([]float64, len(terms))
	for index, term := range terms {
		idf[index] = math.Log(1 + float64(len(candidates))/float64(1+documentFrequency(term.text)))
	}
	wholeQuery := strings.ToLower(strings.TrimSpace(query))
	for index := range candidates {
		score := float64(candidates[index].SemanticScore)
		for termIndex, term := range terms {
			switch {
			case strings.Contains(docs[index].strong, term.text):
				score += 30 * term.weight * idf[termIndex]
			case strings.Contains(docs[index].weak, term.text):
				score += 10 * term.weight * idf[termIndex]
			}
		}
		if wholeQuery != "" && strings.ToLower(candidates[index].Summary) == wholeQuery {
			score += 50
		}
		if stickerSentRecently(candidates[index], now) {
			score *= stickerRecentSendFactor
		}
		candidates[index].Score = score
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].EventTime > candidates[j].EventTime
	})
}

func stickerSentRecently(candidate stickerCandidate, now int64) bool {
	return candidate.LastSentAt > 0 && now-candidate.LastSentAt < int64(stickerRecentSendWindow/time.Second)
}

// selectStickerCandidates 从已排序的候选里挑出最多 limit 个交给 Agent，返回其中命中关键词的个数。
// 命中的在前几名里按分数平方加权抽，免得每次都是同一批；不够的用没命中的随机补位，
// 刚发过的最后才补。randomIndex(n) 返回 [0,n) 的随机数，测试可以替换。
func selectStickerCandidates(candidates []stickerCandidate, limit int, now int64, randomIndex func(int) int) ([]stickerCandidate, int) {
	if limit <= 0 || len(candidates) == 0 {
		return nil, 0
	}
	var matched, fresh, recent []stickerCandidate
	for _, candidate := range candidates {
		switch {
		case candidate.Score > 0:
			matched = append(matched, candidate)
		case stickerSentRecently(candidate, now):
			recent = append(recent, candidate)
		default:
			fresh = append(fresh, candidate)
		}
	}
	pool := matched
	if len(pool) > limit*stickerMatchedPoolFactor {
		pool = pool[:limit*stickerMatchedPoolFactor]
	}
	picked := make([]stickerCandidate, 0, limit)
	if len(pool) <= limit {
		picked = append(picked, pool...)
	} else {
		pool = append([]stickerCandidate(nil), pool...)
		for len(picked) < limit {
			index := weightedStickerIndex(pool, randomIndex)
			picked = append(picked, pool[index])
			pool = append(pool[:index], pool[index+1:]...)
		}
		sort.SliceStable(picked, func(i, j int) bool { return picked[i].Score > picked[j].Score })
	}
	matchedCount := len(picked)
	for _, rest := range [][]stickerCandidate{fresh, recent} {
		rest = append([]stickerCandidate(nil), rest...)
		for len(picked) < limit && len(rest) > 0 {
			index := randomIndex(len(rest))
			picked = append(picked, rest[index])
			rest = append(rest[:index], rest[index+1:]...)
		}
	}
	return picked, matchedCount
}

func weightedStickerIndex(pool []stickerCandidate, randomIndex func(int) int) int {
	const resolution = 1 << 20
	total := 0.0
	for _, candidate := range pool {
		total += candidate.Score * candidate.Score
	}
	if total <= 0 {
		return randomIndex(len(pool))
	}
	target := float64(randomIndex(resolution)) / resolution * total
	for index, candidate := range pool {
		target -= candidate.Score * candidate.Score
		if target < 0 {
			return index
		}
	}
	return len(pool) - 1
}

func (t *dianaStickerTool) semanticCandidateScores(ctx context.Context, query string, shareGroups bool) map[string]int {
	scores := map[string]int{}
	if strings.TrimSpace(query) == "" {
		return scores
	}
	crossGroups := shareGroups && t.event.Kind == EventKindGroup
	ctx = withSemanticSearchPurpose(ctx, "sticker_search")
	for rank, event := range t.runtime.semanticSearchEvents(ctx, t.event, query, 0, time.Now().Unix(), crossGroups) {
		// Keep exact sticker-name matches stronger while making semantic neighbors
		// outrank merely recent candidates.
		score := 80 - rank
		if score < 40 {
			score = 40
		}
		scores[stickerCandidateEventKey(event)] = score
	}
	return scores
}

func stickerCandidateEventKey(event MessageEvent) string {
	return sessionKey(event) + "\x00" + strings.TrimSpace(event.MessageID)
}

// parseStickerAnnotation 从表情包标注里拆出简介和末尾「标签：」行。compactRecallImageDescription
// 会把换行压成空格，所以按最后一个「标签」标记切分；没有标签行就整段当简介。
func parseStickerAnnotation(text string) (string, []string) {
	text = strings.TrimSpace(text)
	marker, at := "", -1
	for _, candidate := range []string{"标签：", "标签:"} {
		if index := strings.LastIndex(text, candidate); index > at {
			marker, at = candidate, index
		}
	}
	if at < 0 {
		return text, nil
	}
	gist := strings.TrimSpace(text[:at])
	var tags []string
	seen := map[string]bool{}
	for _, tag := range strings.FieldsFunc(text[at+len(marker):], func(r rune) bool {
		return strings.ContainsRune("、，,;；/|\n", r)
	}) {
		tag = strings.Trim(strings.TrimSpace(tag), "。.「」\"'")
		if tag == "" || seen[tag] || len([]rune(tag)) > 20 {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
		if len(tags) == 12 {
			break
		}
	}
	return gist, tags
}

func (r *Runtime) stickerTagStore() StickerTagStore {
	r.mu.RLock()
	store := r.messageStore
	r.mu.RUnlock()
	tagStore, _ := store.(StickerTagStore)
	return tagStore
}

func (r *Runtime) saveStickerTags(hash, gist string, tags []string) {
	store := r.stickerTagStore()
	if store == nil || hash == "" {
		return
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := store.SaveStickerTags(saveCtx, StickerTagRecord{ContentSHA256: hash, Gist: gist, Tags: tags}); err != nil {
		log.Printf("diana sticker tags save failed: %v", err)
	}
}

// enrichCandidateDescriptions only touches the bounded result set returned to the
// Agent. Cached descriptions stay free; missing ones use the existing vision route
// and are persisted by image hash so later searches do not call the model again.
func (t *dianaStickerTool) enrichCandidateDescriptions(ctx context.Context, candidates []stickerCandidate) {
	if len(candidates) == 0 || t.runtime.recallImageDescriptionStore() == nil {
		return
	}
	jobs := make(chan int)
	workerCount := stickerDescriptionWorkers
	if workerCount > len(candidates) {
		workerCount = len(candidates)
	}
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer recoverGoroutinePanic("sticker_tool.go:enrich")
			defer workers.Done()
			for index := range jobs {
				candidate := &candidates[index]
				if strings.TrimSpace(candidate.Description) != "" || candidate.Hash == "" {
					continue
				}
				annotation, err := t.runtime.describeStickerImage(ctx, t.event, candidate.Path)
				if err != nil {
					continue
				}
				gist, tags := parseStickerAnnotation(annotation)
				candidate.Description = compactRecallImageDescription(gist)
				candidate.Tags = tags
				candidate.Tagged = true
				t.runtime.saveRecallImageDescription(&recallImageTarget{
					contentSHA256:     candidate.Hash,
					description:       candidate.Description,
					descriptionSource: "vision",
					sourceMessageIDs:  []string{candidate.MessageID},
				}, candidate.SourceEvent)
				t.runtime.saveStickerTags(candidate.Hash, candidate.Description, tags)
				t.runtime.refreshMessageImageSearchText(ctx, candidate.SourceEvent)
			}
		}()
	}
	for index := range candidates {
		if strings.TrimSpace(candidates[index].Description) == "" && candidates[index].Hash != "" {
			jobs <- index
		}
	}
	close(jobs)
	workers.Wait()
}

// tagCandidatesInBackground 给已有通用描述、但还没有表情包标签的候选补标签。
// 这一轮 Agent 先用通用描述挑，不等识图；标好后下次检索就能按标签命中。
func (t *dianaStickerTool) tagCandidatesInBackground(ctx context.Context, candidates []stickerCandidate) {
	if t.runtime.stickerTagStore() == nil {
		return
	}
	var pending []stickerCandidate
	for _, candidate := range candidates {
		if !candidate.FromAssets || candidate.Tagged || candidate.Hash == "" {
			continue
		}
		if _, running := t.runtime.stickerTagging.LoadOrStore(candidate.Hash, true); running {
			continue
		}
		pending = append(pending, candidate)
	}
	if len(pending) == 0 {
		return
	}
	tagCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stickerBackgroundTagTTL)
	go func() {
		defer recoverGoroutinePanic("sticker_tool.go:tag")
		defer cancel()
		for _, candidate := range pending {
			annotation, err := t.runtime.describeStickerImage(tagCtx, t.event, candidate.Path)
			if err == nil {
				gist, tags := parseStickerAnnotation(annotation)
				t.runtime.saveStickerTags(candidate.Hash, compactRecallImageDescription(gist), tags)
			}
			t.runtime.stickerTagging.Delete(candidate.Hash)
		}
	}()
}

// pruneStickerLibrary 在收到带表情包的消息后，把这个会话的表情包库压回上限。
func (r *Runtime) pruneStickerLibrary(ctx context.Context, store MessageHistoryStore, event MessageEvent) {
	pruner, ok := store.(StickerLibraryPruner)
	if !ok || !eventHasSticker(event) {
		return
	}
	_, settings, enabled := r.pluginWithSettingsForEvent(stickerPluginID, event)
	if !enabled {
		return
	}
	capacity := settings.Int(stickerSettingLibraryLimit, 1000)
	if _, err := pruner.PruneStickerAssets(ctx, sessionKey(event), capacity); err != nil {
		log.Printf("diana sticker library prune failed: %v", err)
	}
}

func normalizeStickerSummary(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	return strings.TrimSpace(value)
}

func stickerSearchItems(candidates []stickerCandidate) []stickerSearchItem {
	items := make([]stickerSearchItem, 0, len(candidates))
	for _, candidate := range candidates {
		scope := "current_conversation"
		if candidate.SharedGroup {
			scope = "shared_group"
		} else if candidate.SharedPrivate {
			scope = "shared_private"
		}
		items = append(items, stickerSearchItem{
			ID: candidate.ID, Name: candidate.Summary, Tags: candidate.Tags,
			Description: truncateRunes(candidate.Description, 240), Matched: candidate.Score > 0,
			MessageID: candidate.MessageID, Scope: scope,
		})
	}
	return items
}

func marshalStickerResult(result stickerToolResult) (string, error) {
	body, err := json.Marshal(result)
	return string(body), err
}

func secureRandomIndex(length int) int {
	if length <= 1 {
		return 0
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0
	}
	return int(binary.LittleEndian.Uint64(raw[:]) % uint64(length))
}
