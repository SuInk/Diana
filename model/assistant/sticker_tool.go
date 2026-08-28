// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	dianaStickerToolName             = "diana.sticker"
	maximumStickerDescriptionLookups = 128
)

type dianaStickerTool struct {
	runtime  *Runtime
	event    MessageEvent
	settings SettingValues
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
	SharedGroup   bool
	SharedPrivate bool
}

type stickerSearchItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MessageID   string `json:"source_message_id"`
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
	return &dianaStickerTool{runtime: runtime, event: event, settings: settings}
}

func (t *dianaStickerTool) Name() string { return dianaStickerToolName }

func (t *dianaStickerTool) Description() string {
	return `从当前群或私聊历史的表情包库中检索并发送一张表情包。用户明确要“发表情包”、希望用表情回应，或当前语境适合只用表情包回应时使用。` +
		`通常先 operation=search 查看候选的 id、名称和图片描述，再 operation=send 传 sticker_id；用户已经说清楚情绪或表情名时也可直接 send 并传 query。` +
		`发送由工具完成，成功后不要声称还要上传，也不要把候选的 source_message_id 或内部 id 告诉用户。不得把普通历史图片当表情包发送。`
}

func (t *dianaStickerTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation":  toolEnumParam("search 只返回候选；send 发送一张。", "search", "send"),
		"query":      toolStringParam("想表达的情绪、动作或表情名称，例如“无语”“开心”“投降”。search 可留空查看最近候选；send 未提供 sticker_id 时按 query 选择。"),
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
	candidates, err := t.candidates(ctx, query)
	if err != nil {
		return "", err
	}

	switch operation {
	case "search":
		limit := t.settings.Int(stickerSettingSearchResults, 8)
		if len(candidates) > limit {
			candidates = candidates[:limit]
		}
		items := stickerSearchItems(candidates)
		message := fmt.Sprintf("找到 %d 个当前会话表情包候选；请结合名称和描述选择。", len(items))
		if len(items) == 0 {
			message = "当前会话的表情包库里没有匹配候选。"
		}
		return marshalStickerResult(stickerToolResult{OK: true, Action: "searched", Message: message, Query: query, Candidates: items})
	case "send":
		var selected *stickerCandidate
		if stickerID != "" {
			for index := range candidates {
				if candidates[index].ID == stickerID {
					selected = &candidates[index]
					break
				}
			}
			if selected == nil {
				return marshalStickerResult(stickerToolResult{Action: "not_sent", Message: "这个 sticker_id 不属于当前会话的可用表情包；请重新 search。", Query: query})
			}
		} else if len(candidates) > 0 {
			index := 0
			if query == "" {
				index = secureRandomIndex(len(candidates))
			}
			selected = &candidates[index]
		}
		if selected == nil {
			return marshalStickerResult(stickerToolResult{Action: "not_sent", Message: "当前会话的表情包库里没有匹配候选，没有发送。", Query: query})
		}
		if _, err := os.Stat(selected.Path); err != nil {
			return "", fmt.Errorf("表情包缓存文件不可用: %w", err)
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

func (t *dianaStickerTool) candidates(ctx context.Context, query string) ([]stickerCandidate, error) {
	limit := t.settings.Int(stickerSettingHistoryLimit, 1000)
	shareGroups := t.settings.Bool(stickerSettingCrossGroup, false)
	sharePrivate := t.settings.Bool(stickerSettingCrossPrivate, false)
	t.runtime.mu.RLock()
	store := t.runtime.messageStore
	inMemory := append([]MessageEvent(nil), t.runtime.history[sessionKey(t.event)]...)
	t.runtime.mu.RUnlock()
	events := inMemory
	if store != nil {
		var loaded []MessageEvent
		var err error
		if stickerStore, ok := store.(StickerHistoryStore); ok {
			loaded, err = stickerStore.ListRecentStickerEvents(ctx, StickerHistoryQuery{
				Session:          sessionKey(t.event),
				ContextNamespace: strings.TrimSpace(t.event.ContextNamespace),
				ProfileID:        strings.TrimSpace(t.event.ProfileID),
				ShareGroups:      shareGroups,
				SharePrivate:     sharePrivate,
				Limit:            limit,
			})
		} else {
			loaded, err = store.ListRecentMessageEvents(ctx, sessionKey(t.event), limit)
		}
		if err != nil {
			return nil, fmt.Errorf("读取表情包历史失败: %w", err)
		}
		events = loaded
	}
	if len(events) > limit {
		events = events[len(events)-limit:]
	}

	includeGeneric := t.settings.Bool(stickerSettingIncludeGeneric, true)
	seen := map[string]bool{}
	candidates := make([]stickerCandidate, 0)
	for eventIndex := len(events) - 1; eventIndex >= 0; eventIndex-- {
		event := events[eventIndex]
		for segmentIndex, segment := range event.Segments {
			if segment.Type != "image" {
				continue
			}
			summary := normalizeStickerSummary(segment.Data["summary"])
			if summary == "" || summary == "图片" || (!includeGeneric && summary == "动画表情") {
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
				SharedGroup:   event.Kind == EventKindGroup && sessionKey(event) != sessionKey(t.event),
				SharedPrivate: event.Kind == EventKindPrivate && sessionKey(event) != sessionKey(t.event),
			})
		}
	}

	queryLower := strings.ToLower(strings.TrimSpace(query))
	for index := range candidates {
		summary := strings.ToLower(candidates[index].Summary)
		if queryLower != "" {
			switch {
			case summary == queryLower:
				candidates[index].Score += 100
			case strings.Contains(summary, queryLower) || strings.Contains(queryLower, summary):
				candidates[index].Score += 60
			}
		}
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
		if queryLower != "" && strings.Contains(strings.ToLower(candidates[index].Description), queryLower) {
			candidates[index].Score += 30
		}
	}
	if queryLower != "" {
		filtered := candidates[:0]
		for _, candidate := range candidates {
			if candidate.Score > 0 {
				filtered = append(filtered, candidate)
			}
		}
		candidates = filtered
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].EventTime > candidates[j].EventTime
	})
	return candidates, nil
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
