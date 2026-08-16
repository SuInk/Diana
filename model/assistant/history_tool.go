// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	dianaChatHistoryToolName       = "diana.chat_history"
	defaultChatHistoryRecentLimit  = 20
	maximumChatHistoryResultLimit  = 50
	defaultChatHistoryBefore       = 4
	defaultChatHistoryAfter        = 2
	maximumChatHistoryAroundRadius = 10
	defaultChatHistorySearchHours  = 24
	maximumChatHistorySearchHours  = 24 * 365 * 100
	maximumChatHistoryOutputRunes  = 7600
	chatHistoryLookupTimeout       = 3 * time.Second
)

type dianaChatHistoryTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaChatHistoryResult struct {
	OK              bool                   `json:"ok"`
	Action          string                 `json:"action"`
	Message         string                 `json:"message"`
	AnchorMessageID string                 `json:"anchor_message_id,omitempty"`
	Query           string                 `json:"query,omitempty"`
	Items           []dianaChatHistoryItem `json:"items"`
	Total           int                    `json:"total"`
	Limited         bool                   `json:"limited,omitempty"`
}

type dianaChatHistoryItem struct {
	MessageID               string   `json:"message_id,omitempty"`
	Time                    int64    `json:"event_time,omitempty"`
	LocalTime               string   `json:"local_time,omitempty"`
	Sender                  string   `json:"sender"`
	Text                    string   `json:"text,omitempty"`
	ContentTypes            []string `json:"content_types,omitempty"`
	ImageCount              int      `json:"image_count,omitempty"`
	ImageDescriptions       []string `json:"image_descriptions,omitempty"`
	VideoCount              int      `json:"video_count,omitempty"`
	FileCount               int      `json:"file_count,omitempty"`
	QuotedMessageID         string   `json:"quoted_message_id,omitempty"`
	QuotedSender            string   `json:"quoted_sender,omitempty"`
	QuotedText              string   `json:"quoted_text,omitempty"`
	QuotedImageCount        int      `json:"quoted_image_count,omitempty"`
	QuotedImageDescriptions []string `json:"quoted_image_descriptions,omitempty"`
	GroupID                 string   `json:"group_id,omitempty"`
}

func newDianaChatHistoryTool(runtime *Runtime, event MessageEvent) *dianaChatHistoryTool {
	return &dianaChatHistoryTool{runtime: runtime, event: event}
}

func (t *dianaChatHistoryTool) Name() string {
	return dianaChatHistoryToolName
}

func (t *dianaChatHistoryTool) Description() string {
	return `按需读取本地持久化聊天记录。当引用里的指代需要更早上文、短上下文不足，或用户询问长期历史时，必须先调用，不要直接声称看不到。around 读取当前会话某条消息前后记录；recent 读取当前会话最近记录；search 在 SQLite 中检索，hours、days、from_time 可选，all_time=true 检索全部历史。scope=current 仅当前会话；scope=all_groups 仅在管理员已开启跨群记忆时可用，并严格限定同一机器人命名空间。input: {"operation":"around|recent|search","message_id":"around 可选","query":"search 必填","scope":"current|all_groups","hours":24,"days":0,"all_time":false,"limit":20}`
}

func (t *dianaChatHistoryTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana chat history: runtime is not configured")
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		if t.event.Quoted != nil && strings.TrimSpace(t.event.Quoted.MessageID) != "" {
			operation = "around"
		} else {
			operation = "recent"
		}
	}

	var result dianaChatHistoryResult
	var err error
	switch operation {
	case "around", "context":
		result, err = t.around(ctx, input)
	case "recent", "list":
		result, err = t.recent(ctx, input)
	case "search", "find":
		result, err = t.search(ctx, input)
	default:
		return "", fmt.Errorf("operation 必须是 around、recent 或 search")
	}
	if err != nil {
		return "", err
	}
	return marshalDianaChatHistoryResult(result)
}

func (t *dianaChatHistoryTool) around(ctx context.Context, input map[string]any) (dianaChatHistoryResult, error) {
	messageID := strings.TrimSpace(configToolString(input, "message_id"))
	if messageID == "" && t.event.Quoted != nil {
		messageID = strings.TrimSpace(t.event.Quoted.MessageID)
	}
	if messageID == "" {
		messageID = strings.TrimSpace(t.event.SemanticSourceMessageID)
	}
	if messageID == "" {
		return dianaChatHistoryResult{}, fmt.Errorf("around 需要 message_id；当前消息也没有 QQ 引用可作为默认锚点")
	}
	anchor, found := t.runtime.findSemanticReferenceEvent(ctx, t.event, messageID)
	if !found {
		return dianaChatHistoryResult{}, fmt.Errorf("当前会话中找不到消息 %s", messageID)
	}
	before := chatHistoryBoundedInt(input, "before", defaultChatHistoryBefore, maximumChatHistoryAroundRadius)
	after := chatHistoryBoundedInt(input, "after", defaultChatHistoryAfter, maximumChatHistoryAroundRadius)
	timeline, err := t.timeline(
		ctx,
		anchor.Time-int64(semanticReferenceQuotedLookback/time.Second),
		anchor.Time+int64(semanticReferenceQuotedLookahead/time.Second),
	)
	if err != nil {
		return dianaChatHistoryResult{}, err
	}
	timeline = mergeSemanticReferenceHistory(timeline, []MessageEvent{anchor})
	anchorIndex := -1
	for index := range timeline {
		if strings.TrimSpace(timeline[index].MessageID) == messageID {
			anchorIndex = index
			break
		}
	}
	if anchorIndex < 0 {
		return dianaChatHistoryResult{}, fmt.Errorf("当前会话中无法定位消息 %s 的相邻记录", messageID)
	}
	left := anchorIndex - before
	if left < 0 {
		left = 0
	}
	right := anchorIndex + after + 1
	if right > len(timeline) {
		right = len(timeline)
	}
	items := t.items(ctx, timeline[left:right])
	return dianaChatHistoryResult{
		OK:              true,
		Action:          "around",
		Message:         "已从当前会话的本地持久化记录读取引用消息前后文。",
		AnchorMessageID: messageID,
		Items:           items,
		Total:           len(items),
	}, nil
}

func (t *dianaChatHistoryTool) recent(ctx context.Context, input map[string]any) (dianaChatHistoryResult, error) {
	limit := chatHistoryPositiveInt(input, "limit", defaultChatHistoryRecentLimit, maximumChatHistoryResultLimit)
	t.runtime.mu.RLock()
	store := t.runtime.messageStore
	memory := append([]MessageEvent(nil), t.runtime.history[sessionKey(t.event)]...)
	t.runtime.mu.RUnlock()
	events := memory
	if store != nil {
		loadCtx, cancel := context.WithTimeout(ctx, chatHistoryLookupTimeout)
		stored, err := store.ListRecentMessageEvents(loadCtx, sessionKey(t.event), limit)
		cancel()
		if err != nil {
			return dianaChatHistoryResult{}, fmt.Errorf("读取当前会话最近记录失败: %w", err)
		}
		events = mergeMessageHistory(memory, stored, limit)
	} else if len(events) > limit {
		events = events[len(events)-limit:]
	}
	items := t.items(ctx, events)
	return dianaChatHistoryResult{
		OK:      true,
		Action:  "recent",
		Message: "已读取当前会话最近的本地聊天记录。",
		Items:   items,
		Total:   len(items),
		Limited: len(items) >= limit,
	}, nil
}

func (t *dianaChatHistoryTool) search(ctx context.Context, input map[string]any) (dianaChatHistoryResult, error) {
	query := strings.TrimSpace(configToolString(input, "query"))
	if query == "" {
		return dianaChatHistoryResult{}, fmt.Errorf("search 的 query 不能为空")
	}
	limit := chatHistoryPositiveInt(input, "limit", defaultChatHistoryRecentLimit, maximumChatHistoryResultLimit)
	throughTime := t.event.Time
	if throughTime <= 0 {
		throughTime = time.Now().Unix()
	}
	if raw := intFromAny(input["through_time"]); raw > 0 {
		throughTime = int64(raw)
	}
	fromTime := throughTime - int64(defaultChatHistorySearchHours*time.Hour/time.Second)
	switch {
	case chatHistoryBool(input, "all_time"):
		fromTime = 0
	case intFromAny(input["from_time"]) > 0:
		fromTime = int64(intFromAny(input["from_time"]))
	case intFromAny(input["days"]) > 0:
		days := chatHistoryPositiveInt(input, "days", 1, maximumChatHistorySearchHours/24)
		fromTime = throughTime - int64(time.Duration(days)*24*time.Hour/time.Second)
	default:
		hours := chatHistoryPositiveInt(input, "hours", defaultChatHistorySearchHours, maximumChatHistorySearchHours)
		fromTime = throughTime - int64(time.Duration(hours)*time.Hour/time.Second)
	}
	scope := strings.ToLower(strings.TrimSpace(configToolString(input, "scope")))
	crossGroup := scope == "all_groups" || scope == "cross_group" || scope == "groups"
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	if crossGroup && !boolValue(cfg.CrossGroupMemoryEnabled, false) {
		return dianaChatHistoryResult{}, fmt.Errorf("跨群记忆尚未启用，不能检索其他群")
	}

	t.runtime.mu.RLock()
	store := t.runtime.messageStore
	t.runtime.mu.RUnlock()
	if searchStore, ok := store.(MessageHistorySearchStore); ok {
		loadCtx, cancel := context.WithTimeout(ctx, chatHistoryLookupTimeout)
		matched, total, err := searchStore.SearchMessageEvents(loadCtx, MessageHistorySearchQuery{
			Session:       sessionKey(t.event),
			SessionPrefix: groupHistorySessionPrefix(t.event),
			Text:          query,
			Terms:         structuredMemorySearchTerms(query, 48),
			FromTime:      fromTime,
			ThroughTime:   throughTime,
			Limit:         limit,
			CrossSession:  crossGroup,
		})
		cancel()
		if err != nil {
			return dianaChatHistoryResult{}, fmt.Errorf("检索持久化聊天记录失败: %w", err)
		}
		label := "当前会话"
		if crossGroup {
			label = "同一机器人的所有群"
		}
		return dianaChatHistoryResult{
			OK: true, Action: "search", Message: "已在" + label + "的本地持久化记录中完成检索，结果按时间从新到旧排列。",
			Query: query, Items: t.items(ctx, matched), Total: total, Limited: total > len(matched),
		}, nil
	}
	if crossGroup {
		return dianaChatHistoryResult{}, fmt.Errorf("当前历史存储不支持跨群检索")
	}
	timeline, err := t.timeline(ctx, fromTime, throughTime)
	if err != nil {
		return dianaChatHistoryResult{}, err
	}
	normalizedQuery := strings.ToLower(query)
	matched := make([]MessageEvent, 0, min(limit, len(timeline)))
	total := 0
	for index := len(timeline) - 1; index >= 0; index-- {
		item := timeline[index]
		searchable := strings.ToLower(strings.Join([]string{
			item.MessageID,
			item.SenderNameOrID(),
			historyToolEventText(item),
			quotedPlainText(item.Quoted),
		}, "\n"))
		if !strings.Contains(searchable, normalizedQuery) {
			continue
		}
		total++
		if len(matched) < limit {
			matched = append(matched, item)
		}
	}
	return dianaChatHistoryResult{
		OK:      true,
		Action:  "search",
		Message: "已在当前会话的本地持久化记录中完成检索，结果按时间从新到旧排列。",
		Query:   query,
		Items:   t.items(ctx, matched),
		Total:   total,
		Limited: total > len(matched),
	}, nil
}

func (t *dianaChatHistoryTool) timeline(ctx context.Context, fromTime, throughTime int64) ([]MessageEvent, error) {
	if fromTime < 0 {
		fromTime = 0
	}
	t.runtime.mu.RLock()
	store := t.runtime.messageStore
	memory := append([]MessageEvent(nil), t.runtime.history[sessionKey(t.event)]...)
	t.runtime.mu.RUnlock()
	filteredMemory := make([]MessageEvent, 0, len(memory))
	for _, event := range memory {
		if event.Kind == EventKindNotice || (event.Time > 0 && (event.Time < fromTime || event.Time > throughTime)) {
			continue
		}
		filteredMemory = append(filteredMemory, event)
	}
	if timelineStore, ok := store.(MessageTimelineStore); ok {
		loadCtx, cancel := context.WithTimeout(ctx, chatHistoryLookupTimeout)
		stored, err := timelineStore.ListMessageEventsBetween(loadCtx, sessionKey(t.event), fromTime, throughTime)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("读取当前会话持久化时间线失败: %w", err)
		}
		return mergeSemanticReferenceHistory(stored, filteredMemory), nil
	}
	return mergeSemanticReferenceHistory(filteredMemory), nil
}

func chatHistoryBoundedInt(input map[string]any, key string, fallback, maximum int) int {
	raw, exists := input[key]
	if !exists {
		return fallback
	}
	value := intFromAny(raw)
	if value < 0 {
		value = 0
	}
	if value > maximum {
		value = maximum
	}
	return value
}

func chatHistoryPositiveInt(input map[string]any, key string, fallback, maximum int) int {
	value := chatHistoryBoundedInt(input, key, fallback, maximum)
	if value <= 0 {
		return fallback
	}
	return value
}

func chatHistoryBool(input map[string]any, key string) bool {
	value, ok := input[key]
	if !ok {
		return false
	}
	switch raw := value.(type) {
	case bool:
		return raw
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
		return err == nil && parsed
	default:
		return intFromAny(raw) != 0
	}
}

func chatHistoryReferenceOutsideContext(event MessageEvent, history []MessageEvent) bool {
	references := append([]string(nil), replyReferenceIDs(event.Segments)...)
	references = append(references, eventSemanticSourceMessageIDs(event)...)
	if event.Quoted != nil {
		references = append(references, event.Quoted.MessageID)
		references = append(references, quotedSemanticSourceMessageIDs(event.Quoted)...)
	}
	for _, messageID := range dedupeStrings(references) {
		messageID = strings.TrimSpace(messageID)
		if messageID == "" {
			continue
		}
		found := false
		for _, item := range history {
			if strings.TrimSpace(item.MessageID) == messageID {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func chatHistoryItems(events []MessageEvent) []dianaChatHistoryItem {
	items := make([]dianaChatHistoryItem, 0, len(events))
	for _, event := range events {
		if event.Kind == EventKindNotice {
			continue
		}
		items = append(items, chatHistoryItem(event))
	}
	return items
}

func (t *dianaChatHistoryTool) items(ctx context.Context, events []MessageEvent) []dianaChatHistoryItem {
	items := make([]dianaChatHistoryItem, 0, len(events))
	for _, event := range events {
		if event.Kind == EventKindNotice {
			continue
		}
		item := chatHistoryItem(event)
		if item.ImageCount > 0 {
			item.ImageDescriptions = t.runtime.historyImageCachedSegmentDescriptions(ctx, event.Segments)
		}
		if item.QuotedImageCount > 0 && event.Quoted != nil {
			item.QuotedImageDescriptions = t.runtime.historyImageCachedSegmentDescriptions(ctx, event.Quoted.Segments)
		}
		t.runtime.enqueueHistoryImageDescriptions(event)
		items = append(items, item)
	}
	return items
}

func chatHistoryItem(event MessageEvent) dianaChatHistoryItem {
	item := dianaChatHistoryItem{
		MessageID: event.MessageID,
		Time:      event.Time,
		Sender:    event.SenderNameOrID(),
		Text:      truncateChatHistoryText(historyToolEventText(event), 420),
		GroupID:   strings.TrimSpace(event.GroupID),
	}
	if event.Time > 0 {
		item.LocalTime = time.Unix(event.Time, 0).Local().Format("2006-01-02 15:04:05 -07:00")
	}
	for _, segment := range event.Segments {
		switch segment.Type {
		case "text":
			if strings.TrimSpace(segment.Data["text"]) != "" {
				item.ContentTypes = appendUniqueStrings(item.ContentTypes, "text")
			}
		case "image":
			if segment.Data["source_type"] != "video_frame" {
				item.ImageCount++
				item.ContentTypes = appendUniqueStrings(item.ContentTypes, "image")
			}
		case "video":
			item.VideoCount++
			item.ContentTypes = appendUniqueStrings(item.ContentTypes, "video")
		case "file":
			if videoFileSegment(segment) {
				item.VideoCount++
				item.ContentTypes = appendUniqueStrings(item.ContentTypes, "video")
			} else {
				item.FileCount++
				item.ContentTypes = appendUniqueStrings(item.ContentTypes, "file")
			}
		case "forward":
			item.ContentTypes = appendUniqueStrings(item.ContentTypes, "forward")
		}
	}
	if event.Quoted != nil {
		item.QuotedMessageID = strings.TrimSpace(event.Quoted.MessageID)
		item.QuotedSender = strings.TrimSpace(firstNonEmpty(event.Quoted.SenderName, event.Quoted.UserID))
		item.QuotedText = truncateChatHistoryText(historyToolQuotedText(event.Quoted), 280)
		for _, segment := range event.Quoted.Segments {
			if recallStillImageSegment(segment) {
				item.QuotedImageCount++
			}
		}
	}
	sort.Strings(item.ContentTypes)
	return item
}

func groupHistorySessionPrefix(event MessageEvent) string {
	prefix := strings.TrimSpace(event.ContextNamespace)
	if prefix != "" {
		prefix += ":"
	}
	return prefix + "group:"
}

func historyToolEventText(event MessageEvent) string {
	text := strings.TrimSpace(PlainText(event.Segments))
	if hasImageSegment(event.Segments) {
		text = rawMessageWithoutImagePlaceholders(text)
	}
	if text != "" {
		return text
	}
	labels := make([]string, 0, 4)
	for _, segment := range event.Segments {
		switch segment.Type {
		case "video":
			labels = appendUniqueStrings(labels, "[视频]")
		case "file":
			labels = appendUniqueStrings(labels, "[文件]")
		case "forward":
			labels = appendUniqueStrings(labels, "[合并转发]")
		}
	}
	if len(labels) > 0 {
		return strings.Join(labels, " ")
	}
	if hasImageSegment(event.Segments) {
		return ""
	}
	return strings.TrimSpace(event.RawMessage)
}

func historyToolQuotedText(quoted *QuotedMessage) string {
	if quoted == nil {
		return ""
	}
	text := strings.TrimSpace(PlainText(quoted.Segments))
	if hasImageSegment(quoted.Segments) {
		text = rawMessageWithoutImagePlaceholders(text)
	}
	if text != "" {
		return text
	}
	for _, segment := range quoted.Segments {
		switch segment.Type {
		case "video":
			return "[视频]"
		case "file":
			return "[文件]"
		case "forward":
			return "[合并转发]"
		}
	}
	if hasImageSegment(quoted.Segments) {
		return ""
	}
	return strings.TrimSpace(quoted.RawMessage)
}

func truncateChatHistoryText(text string, limit int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

func marshalDianaChatHistoryResult(result dianaChatHistoryResult) (string, error) {
	for {
		body, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", err
		}
		if len([]rune(string(body))) <= maximumChatHistoryOutputRunes || len(result.Items) == 0 {
			return string(body), nil
		}
		result.Items = result.Items[:len(result.Items)-1]
		result.Limited = true
	}
}
