// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	dianaChatHistoryToolName        = "diana.chat_history"
	defaultChatHistoryRecentLimit   = 20
	maximumChatHistoryResultLimit   = 50
	defaultChatHistoryBefore        = 10
	defaultChatHistoryAfter         = 10
	maximumChatHistoryAroundRadius  = 10
	defaultChatHistorySearchHours   = 24
	maximumChatHistorySearchHours   = 24 * 365 * 100
	defaultChatHistoryRangeLimit    = 60
	maximumChatHistoryRangeLimit    = 200
	defaultChatHistoryOverviewLimit = 24
	maximumChatHistoryOverviewLimit = 40
	chatHistoryOverviewTextRunes    = 140
	maximumChatHistoryOutputRunes   = 7600
	chatHistoryLookupTimeout        = 3 * time.Second
)

type dianaChatHistoryTool struct {
	runtime *Runtime
	event   MessageEvent
	// recallSink 收集本轮读到的撤回记录，供回复阶段按既有链路投递转发卡片。
	recallSink *recallDisclosureSink
}

type dianaChatHistoryResult struct {
	ReturnedCount     int      `json:"returned_count"`
	OmittedCount      int      `json:"omitted_count"`
	RemainingCount    int      `json:"remaining_count"`
	Offset            int      `json:"offset"`
	ClippedCount      int      `json:"clipped_count"`
	Truncated         bool     `json:"truncated"`
	TruncationReasons []string `json:"truncation_reasons,omitempty"`
	HasMore           bool     `json:"has_more"`
	SearchComplete    bool     `json:"search_complete"`
	TotalIsExact      bool     `json:"total_is_exact"`
	NextCursor        string   `json:"next_cursor,omitempty"`
	Order             string   `json:"order,omitempty"`
	Guidance          string   `json:"guidance,omitempty"`
	searchPage        *historySearchCursor
	rangeTimes        []int64
	OK                bool                   `json:"ok"`
	Action            string                 `json:"action"`
	Message           string                 `json:"message"`
	AnchorMessageID   string                 `json:"anchor_message_id,omitempty"`
	Query             string                 `json:"query,omitempty"`
	Window            string                 `json:"window,omitempty"`
	Items             []dianaChatHistoryItem `json:"items"`
	Total             int                    `json:"total"`
	Limited           bool                   `json:"limited,omitempty"`
	// NextFromTime 在时间段没读完时给出续读起点，让模型能一段段读完再总结。
	NextFromTime int64 `json:"next_from_time,omitempty"`
}

type dianaChatHistoryItem struct {
	MessageID               string                 `json:"message_id,omitempty"`
	Time                    int64                  `json:"event_time,omitempty"`
	LocalTime               string                 `json:"local_time,omitempty"`
	Sender                  string                 `json:"sender"`
	TextTruncated           bool                   `json:"text_truncated,omitempty"`
	MediaDetailsOmitted     bool                   `json:"media_details_omitted,omitempty"`
	SenderUserID            string                 `json:"sender_user_id,omitempty"`
	SenderRole              string                 `json:"sender_role,omitempty"`
	Text                    string                 `json:"text,omitempty"`
	ContentTypes            []string               `json:"content_types,omitempty"`
	ImageCount              int                    `json:"image_count,omitempty"`
	ImageDescriptions       []string               `json:"image_descriptions,omitempty"`
	VideoCount              int                    `json:"video_count,omitempty"`
	FileCount               int                    `json:"file_count,omitempty"`
	QuotedMessageID         string                 `json:"quoted_message_id,omitempty"`
	QuotedSender            string                 `json:"quoted_sender,omitempty"`
	QuotedSenderUserID      string                 `json:"quoted_sender_user_id,omitempty"`
	QuotedSenderRole        string                 `json:"quoted_sender_role,omitempty"`
	QuotedText              string                 `json:"quoted_text,omitempty"`
	QuotedTextTruncated     bool                   `json:"quoted_text_truncated,omitempty"`
	QuotedImageCount        int                    `json:"quoted_image_count,omitempty"`
	QuotedImageDescriptions []string               `json:"quoted_image_descriptions,omitempty"`
	GroupID                 string                 `json:"group_id,omitempty"`
	ContextBefore           []dianaChatHistoryItem `json:"context_before,omitempty"`
	ContextAfter            []dianaChatHistoryItem `json:"context_after,omitempty"`
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
	message := fmt.Sprintf("已读取本群最近 24 小时的 %d 条撤回记录；你只需要围绕它们写一句说明，不要逐条复述，也要讲清这些消息已被撤回。", len(items))
	if normalizeRecallReplyMode(t.runtime.effectiveConfigForEvent(t.event).RecallReplyMode) == RecallReplyModeOriginalForward {
		message = fmt.Sprintf("已读取本群最近 24 小时的 %d 条撤回记录，原文将在本轮回复时另行以合并转发卡片发出；你只需要围绕它们写一句说明，不要逐条复述，也要讲清这些消息已被撤回。", len(items))
	}
	return dianaChatHistoryResult{
		OK:      true,
		Action:  "recalls",
		Message: message,
		Window:  chatHistoryWindowLabel(referenceTime-int64(recallDefaultWindow/time.Second), referenceTime),
		Items:   items,
		Total:   len(items),
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
		"operation": toolEnumParam("要执行的操作：around 读某条消息前后的记录；recent 读当前会话最近记录；overview 均匀抽取整个时间段的代表消息，用户要求总结或回顾一天/数小时且消息很多时优先使用；range 按时间段逐条精确列出，适合核对细节，没读完需分页；search 按关键词检索；recalls 读本群最近 24 小时被撤回的消息。",
			"around", "recent", "overview", "range", "search", "recalls"),
		"message_id":   toolStringParam("around 可选：以哪条消息为中心；省略时以当前消息为中心。"),
		"before":       toolIntParam("around 可选：读取锚点之前多少条消息。", 0, maximumChatHistoryAroundRadius),
		"after":        toolIntParam("around 可选：读取锚点之后多少条消息。", 0, maximumChatHistoryAroundRadius),
		"query":        toolStringParam("search 必填：检索关键词。"),
		"order":        toolEnumParam("search 排序：newest 最新优先（默认），oldest 最早优先（未指定起始范围时查全部历史），两者支持精确分页；relevance 保留关键词与语义相关性召回，但无法穷尽或分页，不能用于证明最早。", "newest", "oldest", "relevance"),
		"cursor":       toolStringParam("search 或 range 续查：原样传回 next_cursor；search 保持 query、scope、order 相同。游标固定首次查询的时间范围，优先于 next_from_time。"),
		"group_id":     toolStringParam("around 可选：搜索命中的来源 group_id；跨群展开需要开启跨群记忆，仅可访问同一机器人命名空间。"),
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
	case "overview", "summary", "digest":
		result, err = t.overview(ctx, input)
	case "range", "timeline", "between", "window":
		result, err = t.window(ctx, input)
	case "search", "find":
		result, err = t.search(ctx, input)
	case "recalls", "recall":
		result, err = t.recalls(ctx)
	default:
		return "", fmt.Errorf("operation 必须是 around、recent、overview、range、search 或 recalls")
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(result.Message) != "" {
		result.Message = strings.TrimSpace(result.Message) + " " + t.idNotice()
	}
	return marshalDianaChatHistoryResult(result)
}

// overview 用固定数量的均匀样本覆盖整个时间窗。它不是逐条记录，而是给“昨天发生
// 了什么”这类概括问题一张完整地图，避免 range 从零点开始分页、上下文先被前半夜
// 填满，模型还没翻到下午就被迫总结。
func (t *dianaChatHistoryTool) overview(ctx context.Context, input map[string]any) (dianaChatHistoryResult, error) {
	fromTime, throughTime := t.resolveWindow(input)
	if fromTime > throughTime {
		fromTime, throughTime = throughTime, fromTime
	}
	timeline, err := t.timeline(ctx, fromTime, throughTime)
	if err != nil {
		return dianaChatHistoryResult{}, err
	}
	total := len(timeline)
	if total == 0 {
		return dianaChatHistoryResult{
			OK: true, Action: "overview", Window: chatHistoryWindowLabel(fromTime, throughTime),
			Message: "这个时间段在本地记录里没有消息，可能当时没人说话，或机器人那会儿不在这个会话里；不要凭空编造内容。",
			Items:   []dianaChatHistoryItem{},
		}, nil
	}
	limit := chatHistoryPositiveInt(input, "limit", defaultChatHistoryOverviewLimit, maximumChatHistoryOverviewLimit)
	sampled := evenlySampleChatHistory(timeline, limit)
	items := t.items(ctx, sampled)
	for index := range items {
		items[index].Text = truncateChatHistoryText(items[index].Text, chatHistoryOverviewTextRunes)
		items[index].QuotedText = truncateChatHistoryText(items[index].QuotedText, chatHistoryOverviewTextRunes/2)
		items[index].ImageDescriptions = nil
		items[index].QuotedImageDescriptions = nil
	}
	return dianaChatHistoryResult{
		OK: true, Action: "overview", Window: chatHistoryWindowLabel(fromTime, throughTime),
		Message: fmt.Sprintf("已从整个时间段的 %d 条记录中按时间均匀抽取 %d 条代表消息，样本覆盖开头、中段和结尾，可据此概括全天；这是概览而非完整逐条清单，需要核对具体说法时再用 range 或 search。", total, len(items)),
		Items:   items, Total: total, Limited: total > len(items),
	}, nil
}

func evenlySampleChatHistory(events []MessageEvent, limit int) []MessageEvent {
	if limit <= 0 || len(events) <= limit {
		return append([]MessageEvent(nil), events...)
	}
	if limit == 1 {
		return []MessageEvent{events[len(events)-1]}
	}
	sampled := make([]MessageEvent, 0, limit)
	lastIndex := -1
	for index := 0; index < limit; index++ {
		position := index * (len(events) - 1) / (limit - 1)
		if position == lastIndex {
			continue
		}
		sampled = append(sampled, events[position])
		lastIndex = position
	}
	return sampled
}

func (t *dianaChatHistoryTool) around(ctx context.Context, input map[string]any) (dianaChatHistoryResult, error) {
	if groupID := strings.TrimSpace(configToolString(input, "group_id")); groupID != "" && groupID != t.event.GroupID {
		if t.event.Kind != EventKindGroup || !boolValue(t.runtime.effectiveConfigForEvent(t.event).CrossGroupMemoryEnabled, false) {
			return dianaChatHistoryResult{}, fmt.Errorf("跨群展开需要在群聊中开启跨群记忆")
		}
		scoped := *t
		scoped.event.GroupID = groupID
		scoped.event.replyHistory = nil
		scoped.event.replyHistoryLoaded = false
		scoped.event.Quoted = nil
		scoped.event.SemanticSourceMessageID = ""
		return scoped.around(ctx, input)
	}
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
	for i := range items {
		items[i].Text = historyToolEventText(timeline[left+i])
		items[i].TextTruncated = false
		if quoted := timeline[left+i].Quoted; quoted != nil {
			items[i].QuotedText = historyToolQuotedText(quoted)
			items[i].QuotedTextTruncated = false
		}
	}
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
		return t.window(ctx, input)
	}
	if len([]rune(query)) > 512 {
		return dianaChatHistoryResult{}, fmt.Errorf("检索关键词过长，请缩短至 512 字以内")
	}
	limit := chatHistoryPositiveInt(input, "limit", defaultChatHistoryRecentLimit, maximumChatHistoryResultLimit)
	order := firstNonEmpty(strings.TrimSpace(configToolString(input, "order")), "newest")
	if order != "newest" && order != "oldest" && order != "relevance" {
		return dianaChatHistoryResult{}, fmt.Errorf("order 必须是 newest、oldest 或 relevance")
	}
	if order == "relevance" && configToolString(input, "cursor") != "" {
		return dianaChatHistoryResult{}, fmt.Errorf("相关性召回不能精确分页，请使用 oldest 或 newest")
	}
	scope := firstNonEmpty(strings.TrimSpace(configToolString(input, "scope")), "current")
	crossGroup := scope == "all_groups" || scope == "cross_group" || scope == "groups"
	if crossGroup {
		scope = "all_groups"
	} else if scope != "current" {
		return dianaChatHistoryResult{}, fmt.Errorf("scope 必须是 current 或 all_groups")
	}
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	if crossGroup && (t.event.Kind != EventKindGroup || !boolValue(cfg.CrossGroupMemoryEnabled, false)) {
		return dianaChatHistoryResult{}, fmt.Errorf("跨群记忆尚未启用或当前不是群聊，不能检索其他群")
	}
	from, through := t.resolveWindow(input)
	if order == "oldest" && !hasChatHistoryTimeValue(input, "from_time") && intFromAny(input["days"]) <= 0 && intFromAny(input["hours"]) <= 0 {
		from = 0
	}
	page := historySearchCursor{Version: 1, Scope: historySearchScope(sessionKey(t.event), query, scope, order), From: from, Through: through}
	if cursor := configToolString(input, "cursor"); cursor != "" {
		decoded, err := decodeHistorySearchCursor(cursor)
		if err != nil || decoded.Scope != page.Scope {
			return dianaChatHistoryResult{}, fmt.Errorf("无效续查游标，必须保持会话、query、scope 和 order 相同")
		}
		page = decoded
	}
	if page.From > page.Through {
		return dianaChatHistoryResult{}, fmt.Errorf("起始时间不得晚于结束时间")
	}
	t.runtime.mu.RLock()
	store := t.runtime.messageStore
	t.runtime.mu.RUnlock()
	var matched []MessageEvent
	total := 0
	if searchStore, ok := store.(MessageHistorySearchStore); ok {
		loadCtx, cancel := context.WithTimeout(ctx, chatHistoryLookupTimeout)
		var err error
		matched, total, err = searchStore.SearchMessageEvents(loadCtx, MessageHistorySearchQuery{
			Session: sessionKey(t.event), SessionPrefix: groupHistorySessionPrefix(t.event),
			Text: query, Terms: structuredMemorySearchTerms(query, 48),
			FromTime: page.From, ThroughTime: page.Through, Limit: limit,
			CrossSession: crossGroup, Sort: order, Offset: page.Offset,
		})
		cancel()
		if err != nil {
			return dianaChatHistoryResult{}, fmt.Errorf("检索持久化聊天记录失败: %w", err)
		}
	} else {
		if crossGroup {
			return dianaChatHistoryResult{}, fmt.Errorf("当前历史存储不支持跨群检索")
		}
		timeline, err := t.timeline(ctx, page.From, page.Through)
		if err != nil {
			return dianaChatHistoryResult{}, err
		}
		sort.SliceStable(timeline, func(i, j int) bool {
			if timeline[i].Time == timeline[j].Time {
				if order == "oldest" {
					return timeline[i].MessageID < timeline[j].MessageID
				}
				return timeline[i].MessageID > timeline[j].MessageID
			}
			if order == "oldest" {
				return timeline[i].Time < timeline[j].Time
			}
			return timeline[i].Time > timeline[j].Time
		})
		for _, event := range timeline {
			searchable := strings.Join([]string{event.MessageID, event.UserID, event.SenderNameOrID(), historyToolEventText(event), quotedPlainText(event.Quoted), t.runtime.messageImageDescriptionText(ctx, event)}, "\n")
			if !strings.Contains(strings.ToLower(searchable), strings.ToLower(query)) {
				continue
			}
			if total >= page.Offset && len(matched) < limit {
				matched = append(matched, event)
			}
			total++
		}
	}
	if order == "relevance" {
		if semantic := t.runtime.semanticSearchEvents(ctx, t.event, query, page.From, page.Through, crossGroup); len(semantic) > 0 {
			matched = mergeSearchResultsRRF(matched, semantic, limit)
			total = max(total, len(matched))
		}
	}
	items := t.searchSnippets(ctx, matched, query)
	label := "当前会话"
	if crossGroup {
		label = "同一机器人的所有群"
	}
	result := dianaChatHistoryResult{
		OK: true, Action: "search", Query: query, Order: order, Items: items, Total: total,
		Message: "已在" + label + "内按指定时间顺序进行关键词检索，返回精简命中；图片仅保留相关片段。",
		Window:  chatHistoryWindowLabel(page.From, page.Through), searchPage: &page,
		Guidance: "需要原文或前后文时调用 around，传 message_id；跨群命中同时传 group_id。has_more=true 时继续使用 next_cursor，保持 query、scope、order 相同。search_complete 仅表示当前关键词与范围已枚举完，不等于事件事实完整；未核对原文和完整范围时只能说目前查到最早，不能断言最早就是。时间分页采用关键词匹配，不混入不具备完整总数的语义候选。",
	}
	if order == "relevance" {
		result.searchPage = nil
		result.Limited = true
		result.Message = "已召回相关候选，关键词与可用语义结果按相关性融合；total 不是完整总数。"
		result.Guidance = "相关性召回不可穷尽，search_complete=false、total_is_exact=false；不要断言最早或没有更多。追溯起点改用 oldest 配合 all_time，按 next_cursor 继续；查看所选候选用 around。"
	}
	return result, nil
}

// window 按时间段完整列出当前会话的消息。search 只能按关键词命中，回答
// 「总结昨天 12 点到 17 点」这类请求时没有关键词可用，需要的是整段记录。
func (t *dianaChatHistoryTool) window(ctx context.Context, input map[string]any) (dianaChatHistoryResult, error) {
	fromTime, throughTime := t.resolveWindow(input)
	page := historySearchCursor{Version: 1, Scope: historySearchScope(sessionKey(t.event), "", "range", "oldest"), From: fromTime, Through: throughTime}
	if token := configToolString(input, "cursor"); token != "" {
		decoded, err := decodeHistorySearchCursor(token)
		if err != nil || decoded.Scope != page.Scope {
			return dianaChatHistoryResult{}, fmt.Errorf("无效时间线游标")
		}
		page = decoded
	}
	if page.From > page.Through {
		return dianaChatHistoryResult{}, fmt.Errorf("起始时间不得晚于结束时间")
	}
	limit := chatHistoryPositiveInt(input, "limit", defaultChatHistoryRangeLimit, maximumChatHistoryRangeLimit)
	timeline, err := t.timeline(ctx, page.From, page.Through)
	if err != nil {
		return dianaChatHistoryResult{}, err
	}
	items := t.items(ctx, timeline)
	times := make([]int64, len(items))
	for i := range items {
		times[i] = items[i].Time
	}
	total := len(items)
	start := min(page.Offset, total)
	items = items[start:min(start+limit, total)]
	message := "已按时间从旧到新读取本地记录；是否完整以 search_complete、has_more 和 truncated 为准。"
	if total == 0 {
		message = "这个时间段在本地记录里没有消息，可能当时没人说话，或机器人那会儿不在这个会话里；不要凭空编造内容。"
	}
	return dianaChatHistoryResult{
		OK: true, Action: "range", Order: "oldest", Items: items, Total: total,
		Window: chatHistoryWindowLabel(page.From, page.Through), searchPage: &page, rangeTimes: times,
		Message:  message,
		Guidance: "有 next_cursor 时使用 range 和 cursor 续查，游标固定时间范围且不会跳过同一秒的消息。next_from_time 仅在时间边界无重复时提供。若需概括整个时间段可用 overview；truncated=true 时细节不完整，不得据此断言没有其他记录。",
	}, nil
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
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	cfg.BotAccount = firstNonEmpty(cfg.BotAccount, t.event.SelfID)
	for _, event := range events {
		if event.Kind == EventKindNotice {
			continue
		}
		item := chatHistoryItem(event, cfg)
		if item.ImageCount > 0 {
			item.ImageDescriptions = t.runtime.historyImageCachedSegmentDescriptions(ctx, event.Segments)
		}
		if item.QuotedImageCount > 0 && event.Quoted != nil {
			item.QuotedImageDescriptions = t.runtime.historyImageCachedSegmentDescriptions(ctx, event.Quoted.Segments)
		}
		t.runtime.enqueueHistoryImageDescriptionsNow(event)
		items = append(items, item)
	}
	return items
}

func chatHistoryItem(event MessageEvent, configs ...BotConfig) dianaChatHistoryItem {
	item := dianaChatHistoryItem{
		MessageID:    event.MessageID,
		Time:         event.Time,
		Sender:       event.SenderNameOrID(),
		SenderUserID: strings.TrimSpace(event.UserID),
		SenderRole:   historySenderRole(event, configs...),
		Text:         truncateChatHistoryText(historyToolEventText(event), 420),
		GroupID:      strings.TrimSpace(event.GroupID),
	}
	if event.Time > 0 {
		item.LocalTime = time.Unix(event.Time, 0).Local().Format("2006-01-02 15:04:05 -07:00")
	}
	item.TextTruncated = item.Text != strings.TrimSpace(historyToolEventText(event))
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
		item.QuotedSenderUserID = strings.TrimSpace(event.Quoted.UserID)
		item.QuotedSenderRole = historySenderRole(quotedHistoryIdentityEvent(event), configs...)
		item.QuotedText = truncateChatHistoryText(historyToolQuotedText(event.Quoted), 280)
		item.QuotedTextTruncated = item.QuotedText != strings.TrimSpace(historyToolQuotedText(event.Quoted))
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

// chatHistoryIDNotice 跟着每个结果一起回去。系统提示词里已经有同样的规则，
// 但模型是在读完这份结果、手里正攥着一串 message_id 的时候动笔的，就近再说
// 一遍比隔着几千 token 的规则管用。
const chatHistoryIDNotice = "message_id 只用于继续调用历史工具，不得写进给用户的回复；指认某条消息时用时间、发送者和内容描述。" + historyIdentityNotice

// chatHistoryQuoteNotice 在禁令之外给出真正该做的动作：把那条消息引用出来。
const chatHistoryQuoteNotice = "要让用户直接定位到某条消息，在回复最开头写 " + replyMarkerPrefix + "该消息的 message_id]，客户端会渲染成引用框。"

// idNotice 按本会话的引用配置决定提醒内容：引用被关掉时只留禁令，不教一个
// 注定会被丢掉的动作。
func (t *dianaChatHistoryTool) idNotice() string {
	if t == nil || t.runtime == nil {
		return chatHistoryIDNotice
	}
	if replyReferenceMode(t.runtime.effectiveConfigForEvent(t.event)) == ReplyDecorationOff {
		return chatHistoryIDNotice
	}
	return chatHistoryIDNotice + " " + chatHistoryQuoteNotice
}

func marshalDianaChatHistoryResult(result dianaChatHistoryResult) (string, error) {
	return marshalHistoryResultWithBudget(result)
}
