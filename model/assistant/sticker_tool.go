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
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	dianaStickerToolName             = "diana.sticker"
	maximumStickerDescriptionLookups = 128
	stickerDescriptionWorkers        = 3
)

type dianaStickerTool struct {
	runtime  *Runtime
	event    MessageEvent
	settings SettingValues
	searchMu sync.Mutex
	searched map[string]stickerCandidate
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
	Path          string
	Hash          string
	MessageID     string
	EventTime     int64
	Score         int
	SemanticScore int
	SourceEvent   MessageEvent
	SharedGroup   bool
	SharedPrivate bool
}

type stickerSearchItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MessageID   string `json:"-"`
	Scope       string `json:"scope"`
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
		`必须先用 operation=search 和语义意图查询候选，再结合候选的名称与图片简介判断哪张最符合当前语境，最后用 operation=send 原样传回 sticker_id。不要只按关键词字面相同选择。` +
		`发送由工具完成，成功后不要声称还要上传，也不要把候选的 source_message_id 或内部 id 告诉用户。不得把普通历史图片当表情包发送。`
}

func (t *dianaStickerTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation":  toolEnumParam("search 只返回候选；send 发送一张。", "search", "send"),
		"query":      toolStringParam("search 的语义意图，例如“安慰一下对方”“对离谱发言表示无语”“开心庆祝”；可留空查看最近候选。旧调用可在 send 时传明确表情名称，但语义选图应先 search。"),
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
		candidates, err := t.candidates(ctx, query)
		if err != nil {
			return "", err
		}
		limit := t.settings.Int(stickerSettingSearchResults, 8)
		if len(candidates) > limit {
			candidates = candidates[:limit]
		}
		t.enrichCandidateDescriptions(ctx, candidates)
		rankStickerCandidates(candidates, query)
		t.rememberSearchCandidates(candidates)
		items := stickerSearchItems(candidates)
		message := fmt.Sprintf("找到 %d 个当前会话表情包候选；请按当前语义结合名称和图片简介选择，不要求查询词与候选文字完全一致。", len(items))
		if len(items) == 0 {
			message = "本轮没有可用候选；继续正常回应，不要向用户提及内部图库、索引、搜索或工具状态，也不要声称已经发送。"
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
			if len(candidates) == 0 || (query != "" && candidates[0].Score <= 0) {
				return marshalStickerResult(stickerToolResult{Action: "not_sent", Message: "没有足够匹配的候选；请先 search 查看简介，再传 sticker_id。本轮不发送表情，也不要向用户解释内部图库或搜索状态。", Query: query})
			}
			index := 0
			if query == "" {
				index = secureRandomIndex(len(candidates))
			}
			selected = &candidates[index]
		}
		if _, err := os.Stat(selected.Path); err != nil {
			return "", fmt.Errorf("表情包缓存文件不可用: %w", err)
		}
		if selected.Hash != "" && !stickerFileMatchesHash(selected.Path, selected.Hash) {
			return "", fmt.Errorf("表情包缓存内容校验失败")
		}
		if err := t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{ImageURLs: []string{selected.Path}})); err != nil {
			return "", fmt.Errorf("发送表情包失败: %w", err)
		}
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
	}

	for index := range candidates {
		if index < maximumStickerDescriptionLookups {
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
	rankStickerCandidates(candidates, query)
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

func rankStickerCandidates(candidates []stickerCandidate, query string) {
	queryLower := strings.ToLower(strings.TrimSpace(query))
	for index := range candidates {
		candidates[index].Score = candidates[index].SemanticScore
		if queryLower == "" {
			continue
		}
		summary := strings.ToLower(candidates[index].Summary)
		switch {
		case summary == queryLower:
			candidates[index].Score += 100
		case strings.Contains(summary, queryLower) || strings.Contains(queryLower, summary):
			candidates[index].Score += 60
		}
		if strings.Contains(strings.ToLower(candidates[index].Description), queryLower) {
			candidates[index].Score += 30
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].EventTime > candidates[j].EventTime
	})
}

func (t *dianaStickerTool) semanticCandidateScores(ctx context.Context, query string, shareGroups bool) map[string]int {
	scores := map[string]int{}
	if strings.TrimSpace(query) == "" {
		return scores
	}
	crossGroups := shareGroups && t.event.Kind == EventKindGroup
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
			defer workers.Done()
			for index := range jobs {
				candidate := &candidates[index]
				if strings.TrimSpace(candidate.Description) != "" || candidate.Hash == "" {
					continue
				}
				description, err := t.runtime.describeStickerImage(ctx, t.event, candidate.Path)
				if err != nil {
					continue
				}
				candidate.Description = compactRecallImageDescription(description)
				t.runtime.saveRecallImageDescription(&recallImageTarget{
					contentSHA256:     candidate.Hash,
					description:       candidate.Description,
					descriptionSource: "vision",
					sourceMessageIDs:  []string{candidate.MessageID},
				}, candidate.SourceEvent)
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
		items = append(items, stickerSearchItem{ID: candidate.ID, Name: candidate.Summary, Description: truncateRunes(candidate.Description, 240), MessageID: candidate.MessageID, Scope: scope})
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
