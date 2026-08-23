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
	defaultChatHistoryRangeLimit   = 60
	maximumChatHistoryRangeLimit   = 200
	// range 自己先按预算裁剪，好把「还没读完、从哪接着读」写进结果里；留一点
	// 余量给这两个字段本身。
	chatHistoryRangeReserveRunes  = 320
	maximumChatHistoryOutputRunes = 7600
	chatHistoryLookupTimeout      = 3 * time.Second
)

type dianaChatHistoryTool struct {
	runtime *Runtime
	event   MessageEvent
	// recallSink 收集本轮读到的撤回记录，供回复阶段按既有链路投递转发卡片。
	recallSink *recallDisclosureSink
}

type dianaChatHistoryResult struct {
	OK              bool                   `json:"ok"`
	Action          string                 `json:"action"`
	Message         string                 `json:"message"`
	AnchorMessageID string                 `json:"anchor_message_id,omitempty"`
	Query           string                 `json:"query,omitempty"`
	Window          string                 `json:"window,omitempty"`
	Items           []dianaChatHistoryItem `json:"items"`
	Total           int                    `json:"total"`
	Limited         bool                   `json:"limited,omitempty"`
	// NextFromTime 在时间段没读完时给出续读起点，让模型能一段段读完再总结。
	NextFromTime int64 `json:"next_from_time,omitempty"`
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

// withRecallSink 绑定本轮的撤回响应收集器。只有正式回复路径需要它：读到撤回记录后
// 转发卡片仍由回复阶段按既有链路投递。
func (t *dianaChatHistoryTool) withRecallSink(sink *recallDisclosureSink) *dianaChatHistoryTool {
	if t != nil {
		t.recallSink = sink
	}
	return t
}

// recalls 读本群最近窗口内的撤回记录。
//
// 这条路以前不是工具：插件用词表扫消息里有没有「撤回」加「谁/什么/看看」，命中就
// 劫持整条回复。判断用户想不想看撤回记录是语义问题，本项目一律交给模型。
func (t *dianaChatHistoryTool) recalls(ctx context.Context) (dianaChatHistoryResult, error) {
	if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		return dianaChatHistoryResult{}, fmt.Errorf("撤回记录只在群聊中可用")
	}
	plugin, ok := t.runtime.messageHistoryPlugin()
	if !ok {
		return dianaChatHistoryResult{}, fmt.Errorf("消息历史插件未启用，无法读取撤回记录")
	}
	t.runtime.mu.RLock()
	channel := t.runtime.channel
	t.runtime.mu.RUnlock()
	response, recalls, referenceTime := plugin.RecallDisclosureResponse(
		ctx, channel, t.event, t.runtime.contextHistory(t.event), t.runtime.recallHistory(t.event))
	if response == nil {
		return dianaChatHistoryResult{}, fmt.Errorf("撤回记录只在群聊中可用")
	}
	// 交回本轮，由回复阶段沿用原有的转发卡片与自动撤回投递。
	t.recallSink.add(*response)
	if len(recalls) == 0 {
		return dianaChatHistoryResult{
			OK:      true,
			Action:  "recalls",
			Message: "最近 24 小时没有记录到群消息撤回。",
			Items:   []dianaChatHistoryItem{},
		}, nil
	}
	items := t.items(ctx, t.runtime.enrichRecallImageDescriptions(ctx, t.event, recalls))
	return dianaChatHistoryResult{
		OK:     true,
		Action: "recalls",
		Message: fmt.Sprintf("已读取本群最近 24 小时的 %d 条撤回记录，原文会另行以合并转发卡片发出；"+
			"你只需要围绕它们写一句说明，不要逐条复述，也要讲清这些消息已被撤回。", len(items)),
		Window: chatHistoryWindowLabel(referenceTime-int64(recallDefaultWindow/time.Second), referenceTime),
		Items:  items,
		Total:  len(items),
	}, nil
}

func newDianaChatHistoryTool(runtime *Runtime, event MessageEvent) *dianaChatHistoryTool {
	return &dianaChatHistoryTool{runtime: runtime, event: event}
}

func (t *dianaChatHistoryTool) Name() string {
	return dianaChatHistoryToolName
}

func (t *dianaChatHistoryTool) Description() string {
	return `按需读取本地持久化聊天记录。引用里的指代需要更早上文、短上下文不够、或用户询问长期历史时必须先调用，不要直接声称看不到。`
}

// InputSchema 声明参数契约。哪个 operation 配哪些参数写在字段说明里，
// 比在散文里列一遍内联 JSON 更不容易看漏。
func (t *dianaChatHistoryTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作：around 读某条消息前后的记录；recent 读当前会话最近记录；range 按时间段完整列出消息，用户要求总结或回顾某个时间段（「昨天 12 点到 17 点」）时用它，不要改用 search 猜关键词；search 按关键词检索；recalls 读本群最近 24 小时被撤回的消息，用户想知道谁撤回了什么时用它。",
			"around", "recent", "range", "search", "recalls"),
		"message_id":   toolStringParam("around 可选：以哪条消息为中心；省略时以当前消息为中心。"),
		"query":        toolStringParam("search 必填：检索关键词。"),
		"from_time":    toolStringParam(`range 与 search 的起始时间。接受 Unix 秒，也接受本地时间字符串 "2006-01-02 15:04" 或 "2006-01-02"。range 一次读不完时结果会给出 next_from_time，用它继续读完整个时间段再总结。`),
		"through_time": toolStringParam(`range 与 search 的结束时间，写法同 from_time。`),
		"scope": toolEnumParam("检索范围。current 仅当前会话；all_groups 只有 search 支持，且需要管理员已开启跨群记忆，并严格限定在同一机器人命名空间内。",
			"current", "all_groups"),
		"hours":    toolIntParam("search 可选：只检索最近多少小时。", 1, 24*365),
		"days":     toolIntParam("search 可选：只检索最近多少天。", 1, 365),
		"all_time": toolBoolParam("search 可选：置 true 时检索全部历史，忽略 hours 和 days。"),
		"limit":    toolIntParam("返回条数，默认 "+itoa(defaultChatHistoryRecentLimit)+"。", 1, maximumChatHistoryResultLimit),
	})
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
	case "range", "timeline", "between", "window":
		result, err = t.window(ctx, input)
	case "search", "find":
		result, err = t.search(ctx, input)
	case "recalls", "recall":
		result, err = t.recalls(ctx)
	default:
		return "", fmt.Errorf("operation 必须是 around、recent、range、search 或 recalls")
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
		return dianaChatHistoryResult{}, fmt.Errorf("around 需要 message_id；当前消息也没有引用可作为默认锚点")
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
		// 「总结某个时间段」这类请求没有关键词可检索。与其报错让模型以为
		// 记录是空的，不如直接按时间段列出来。
		return t.window(ctx, input)
	}
	limit := chatHistoryPositiveInt(input, "limit", defaultChatHistoryRecentLimit, maximumChatHistoryResultLimit)
	fromTime, throughTime := t.resolveWindow(input)
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
		// 语义召回与词面结果做 RRF 融合;未启用或失败时 semantic 为 nil,词面结果原样返回。
		if semantic := t.runtime.semanticSearchEvents(ctx, t.event, query, fromTime, throughTime, crossGroup); len(semantic) > 0 {
			matched = mergeSearchResultsRRF(matched, semantic, limit)
			if len(matched) > total {
				total = len(matched)
			}
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

// window 按时间段完整列出当前会话的消息。search 只能按关键词命中，回答
// 「总结昨天 12 点到 17 点」这类请求时没有关键词可用，需要的是整段记录。
func (t *dianaChatHistoryTool) window(ctx context.Context, input map[string]any) (dianaChatHistoryResult, error) {
	fromTime, throughTime := t.resolveWindow(input)
	if fromTime > throughTime {
		fromTime, throughTime = throughTime, fromTime
	}
	limit := chatHistoryPositiveInt(input, "limit", defaultChatHistoryRangeLimit, maximumChatHistoryRangeLimit)
	timeline, err := t.timeline(ctx, fromTime, throughTime)
	if err != nil {
		return dianaChatHistoryResult{}, err
	}
	items := t.items(ctx, timeline)
	total := len(items)
	// 从旧到新截断：配合 next_from_time 就能一段段往后读完整个时间段。
	truncated := false
	if len(items) > limit {
		items = items[:limit]
		truncated = true
	}
	items, budgetTruncated := fitChatHistoryItems(items, maximumChatHistoryOutputRunes-chatHistoryRangeReserveRunes)
	truncated = truncated || budgetTruncated

	result := dianaChatHistoryResult{
		OK:     true,
		Action: "range",
		Window: chatHistoryWindowLabel(fromTime, throughTime),
		Items:  items,
		Total:  total,
	}
	switch {
	case total == 0:
		result.Message = "这个时间段在本地记录里没有消息，可能当时没人说话，或机器人那会儿不在这个会话里；不要凭空编造内容。"
	case truncated:
		result.Limited = true
		if last := items[len(items)-1]; last.Time > 0 {
			result.NextFromTime = last.Time + 1
		}
		result.Message = fmt.Sprintf("已按时间从旧到新读取该时间段的前 %d 条（共 %d 条）。用 next_from_time 作为 from_time 继续读完剩下的再总结，不要只凭这一批下结论。", len(items), total)
	default:
		result.Message = "已按时间从旧到新读取该时间段的全部本地记录。"
	}
	return result, nil
}

// resolveWindow 解析检索时间窗。from_time、through_time 既收 Unix 秒也收本地
// 时间字符串——让模型把「昨天 12 点」直接写成字面时间，比让它算时间戳稳。
func (t *dianaChatHistoryTool) resolveWindow(input map[string]any) (fromTime, throughTime int64) {
	throughTime = t.event.Time
	if throughTime <= 0 {
		throughTime = time.Now().Unix()
	}
	if value, dateOnly, ok := chatHistoryTimeValue(input, "through_time"); ok {
		throughTime = value
		if dateOnly {
			// 只给日期时按整天算，否则「through=昨天」会截在零点。
			throughTime += int64(24*time.Hour/time.Second) - 1
		}
	}
	switch {
	case chatHistoryBool(input, "all_time"):
		fromTime = 0
	case hasChatHistoryTimeValue(input, "from_time"):
		value, _, _ := chatHistoryTimeValue(input, "from_time")
		fromTime = value
	case intFromAny(input["days"]) > 0:
		days := chatHistoryPositiveInt(input, "days", 1, maximumChatHistorySearchHours/24)
		fromTime = throughTime - int64(time.Duration(days)*24*time.Hour/time.Second)
	default:
		hours := chatHistoryPositiveInt(input, "hours", defaultChatHistorySearchHours, maximumChatHistorySearchHours)
		fromTime = throughTime - int64(time.Duration(hours)*time.Hour/time.Second)
	}
	if fromTime < 0 {
		fromTime = 0
	}
	return fromTime, throughTime
}

var chatHistoryTimeLayouts = []struct {
	layout   string
	dateOnly bool
}{
	{"2006-01-02 15:04:05", false},
	{"2006-01-02T15:04:05", false},
	{"2006-01-02 15:04", false},
	{"2006-01-02T15:04", false},
	{"2006/01/02 15:04", false},
	{"2006-01-02", true},
	{"2006/01/02", true},
}

func hasChatHistoryTimeValue(input map[string]any, key string) bool {
	_, _, ok := chatHistoryTimeValue(input, key)
	return ok
}

// chatHistoryTimeValue 把 Unix 秒或本地时间字符串解析成时间戳。
func chatHistoryTimeValue(input map[string]any, key string) (value int64, dateOnly, ok bool) {
	raw, exists := input[key]
	if !exists || raw == nil {
		return 0, false, false
	}
	if text, isText := raw.(string); isText {
		text = strings.TrimSpace(text)
		if text == "" {
			return 0, false, false
		}
		if seconds, err := strconv.ParseInt(text, 10, 64); err == nil && seconds > 0 {
			return seconds, false, true
		}
		if parsed, err := time.Parse(time.RFC3339, text); err == nil {
			return parsed.Unix(), false, true
		}
		for _, candidate := range chatHistoryTimeLayouts {
			if parsed, err := time.ParseInLocation(candidate.layout, text, time.Local); err == nil {
				return parsed.Unix(), candidate.dateOnly, true
			}
		}
		return 0, false, false
	}
	if seconds := intFromAny(raw); seconds > 0 {
		return int64(seconds), false, true
	}
	return 0, false, false
}

func chatHistoryWindowLabel(fromTime, throughTime int64) string {
	layout := "2006-01-02 15:04:05"
	from := "最早"
	if fromTime > 0 {
		from = time.Unix(fromTime, 0).Local().Format(layout)
	}
	return from + " ~ " + time.Unix(throughTime, 0).Local().Format(layout)
}

// fitChatHistoryItems 按输出预算保留靠前（时间靠旧）的条目，丢弃放不下的尾部。
func fitChatHistoryItems(items []dianaChatHistoryItem, budget int) ([]dianaChatHistoryItem, bool) {
	truncated := false
	for len(items) > 0 {
		body, err := json.MarshalIndent(items, "", "  ")
		if err != nil || len([]rune(string(body))) <= budget {
			return items, truncated
		}
		items = items[:len(items)-1]
		truncated = true
	}
	return items, truncated
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
	return toolInputBool(input, key)
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
