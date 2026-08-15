package assistant

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

const (
	semanticReferenceDefaultLookback      = 24 * time.Hour
	semanticReferenceQuotedLookback       = 6 * time.Hour
	semanticReferenceQuotedLookahead      = 30 * time.Minute
	semanticReferenceMediaCandidateLimit  = 48
	semanticReferenceRecentCandidateLimit = 20
	semanticReferenceQuoteContextRadius   = 8
	semanticReferenceNearbyContextRadius  = 3
)

type semanticReferenceCandidate struct {
	MessageID                string   `json:"message_id"`
	Sender                   string   `json:"sender"`
	Text                     string   `json:"text,omitempty"`
	Content                  []string `json:"content_types,omitempty"`
	ImageCount               int      `json:"image_count,omitempty"`
	AudioCount               int      `json:"audio_count,omitempty"`
	VideoCount               int      `json:"video_count,omitempty"`
	FileCount                int      `json:"file_count,omitempty"`
	EventTime                int64    `json:"event_time,omitempty"`
	AgeSeconds               *int64   `json:"age_seconds,omitempty"`
	ReplyToMessageIDs        []string `json:"reply_to_message_ids,omitempty"`
	QuotedMessageID          string   `json:"quoted_message_id,omitempty"`
	SemanticSourceMessageID  string   `json:"semantic_source_message_id,omitempty"`
	SemanticSourceMessageIDs []string `json:"semantic_source_message_ids,omitempty"`
	NearbyContext            []string `json:"nearby_context,omitempty"`
	IsBotMessage             bool     `json:"is_bot_message,omitempty"`
	IsErrorWrapper           bool     `json:"is_error_wrapper,omitempty"`
}

type semanticReferenceDecision struct {
	MessageID  string   `json:"message_id,omitempty"`
	MessageIDs []string `json:"message_ids,omitempty"`
	Confidence float64  `json:"confidence"`
	Reason     string   `json:"reason,omitempty"`
}

func (r *Runtime) enrichSemanticReference(ctx context.Context, event MessageEvent, text string) MessageEvent {
	if eventHasDirectReferenceContent(event) || quotedMessageHasReferenceContent(event.Quoted) {
		return event
	}
	candidates, events, anchorTime := r.semanticReferenceCandidates(ctx, event)
	if len(candidates) == 0 {
		return event
	}
	if event.Quoted != nil && !semanticCandidatesHaveMedia(candidates) {
		return event
	}
	payload, err := json.Marshal(map[string]any{
		"current_sender":        event.SenderNameOrID(),
		"current_text":          strings.TrimSpace(text),
		"current_event_time":    event.Time,
		"reference_anchor_time": anchorTime,
		"explicit_quote":        semanticQuotedCandidate(event.Quoted, r.effectiveConfigForEvent(event).BotQQ),
		"candidates":            candidates,
	})
	if err != nil {
		return event
	}
	callCtx, cancel := context.WithTimeout(ctx, semanticRouteTimeout)
	defer cancel()
	raw, err := r.runLLMRouterProvider(callCtx, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(callCtx, llm.GenerateRequest{Messages: []llm.Message{
			{
				Role: llm.RoleSystem,
				Content: strings.TrimSpace(`你是 QQ 对话的上下文指代判断器。判断当前消息具体在询问、评价、修改或接续哪一条或哪些历史消息。

规则：
1. 候选可以来自任何发送者；event_time 和 age_seconds 表示消息时间及距当前消息的秒数。结合时间判断是否仍属于当前话题，间隔过长且当前措辞没有明确指向时选择 none；不要默认只选当前发言者自己的消息。
2. 综合当前措辞、发送者名称、消息先后顺序和内容类型判断。像“这是什么”“他刚发的图”“上面那个视频”“前面说的方案”都可能指向历史消息。
3. explicit_quote 是用户在 QQ 中直接引用的消息。通常优先保持它；但 is_error_wrapper=true 表示它只是机器人产生的超时、失败或重试提示，不是原任务内容。此时要结合 semantic_source_message_id、semantic_source_message_ids、reply_to_message_ids、reference_anchor_time、候选时间顺序和 nearby_context，寻找该失败任务实际使用的图片、语音、视频或文件。
4. semantic_source_message_id 和 semantic_source_message_ids 是先前处理时持久化的真实来源关系，是强证据；但仍要结合当前措辞判断用户现在是否在指代它们。
5. candidates 已由长期消息时间线检索并排序，不只包含短期聊天。nearby_context 是媒体前后的原始对话，用于分辨多张图片或多个任务。
6. 当前措辞明确指向多条消息或多份媒体，例如“这几张图”“刚才连发的三张”“上面那些”，必须把属于同一批次且被共同指代的所有候选都选出，不能只选其中一条。message_ids 按原消息从旧到新排列。
7. 只能返回候选中真实存在的 message_id，不能编造。当前消息确实是在“重试/再看”一个错误包装消息，并且能定位到其底层媒体时，应返回底层媒体消息；无法可靠判断时返回空数组。
8. 只输出 JSON，不要输出 Markdown 或解释。为兼容旧调用，单条也必须放进 message_ids。

输出格式：{"message_ids":["候选ID1","候选ID2"],"confidence":0到1,"reason":"简短理由"}`),
			},
			{Role: llm.RoleUser, Content: "请判断当前消息指向哪个历史候选：\n" + string(payload)},
		}})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		r.recordSemanticReference(ctx, event, nil, 0, err)
		return event
	}
	decision, ok := parseSemanticReferenceDecision(raw)
	if !ok || len(decision.MessageIDs) == 0 || decision.Confidence < 0.55 {
		return event
	}
	selectedCandidates := make(map[string]bool, len(decision.MessageIDs))
	for _, messageID := range decision.MessageIDs {
		if _, found := events[messageID]; found {
			selectedCandidates[messageID] = true
		}
	}
	selectedSourceIDs := make([]string, 0, len(decision.MessageIDs))
	var primaryQuoted *QuotedMessage
	for _, candidate := range candidates {
		if !selectedCandidates[candidate.MessageID] {
			continue
		}
		selected := events[candidate.MessageID]
		quoted := r.semanticSelectedQuoted(ctx, event, selected)
		if quoted == nil || (event.Quoted != nil && !quotedMessageHasReferenceContent(quoted)) {
			continue
		}
		quoted.Semantic = true
		if primaryQuoted == nil {
			primaryQuoted = quoted
		}
		selectedSourceIDs = appendUniqueStrings(selectedSourceIDs, quoted.MessageID)
	}
	if primaryQuoted == nil || len(selectedSourceIDs) == 0 {
		return event
	}
	setQuotedSemanticSourceMessageIDs(primaryQuoted, selectedSourceIDs)
	event.Quoted = primaryQuoted
	setEventSemanticSourceMessageIDs(&event, selectedSourceIDs)
	r.updateRememberedSemanticReference(event)
	r.recordSemanticReference(ctx, event, selectedSourceIDs, decision.Confidence, nil)
	return event
}

func quotedMessageHasReferenceContent(quoted *QuotedMessage) bool {
	return quoted != nil && segmentsHaveReferenceContent(quoted.Segments)
}

func segmentsHaveReferenceContent(segments []MessageSegment) bool {
	for _, segment := range segments {
		switch segment.Type {
		case "image", "video", "record", "forward", "json", "xml":
			return true
		case "file":
			return true
		}
	}
	return false
}

func semanticCandidatesHaveMedia(candidates []semanticReferenceCandidate) bool {
	for _, candidate := range candidates {
		if candidate.ImageCount > 0 || candidate.AudioCount > 0 || candidate.VideoCount > 0 || candidate.FileCount > 0 {
			return true
		}
	}
	return false
}

func semanticQuotedCandidate(quoted *QuotedMessage, botQQ string) any {
	if quoted == nil {
		return nil
	}
	candidate := semanticReferenceCandidate{
		MessageID:                quoted.MessageID,
		Sender:                   firstNonEmpty(quoted.SenderName, quoted.UserID),
		Text:                     truncateRunesFromStart(quotedPlainText(quoted), 280),
		EventTime:                0,
		ReplyToMessageIDs:        replyReferenceIDs(quoted.Segments),
		SemanticSourceMessageID:  strings.TrimSpace(quoted.SemanticSourceMessageID),
		SemanticSourceMessageIDs: quotedSemanticSourceMessageIDs(quoted),
		IsBotMessage:             strings.TrimSpace(botQQ) != "" && strings.TrimSpace(quoted.UserID) == strings.TrimSpace(botQQ),
	}
	countSemanticReferenceSegments(&candidate, quoted.Segments)
	finalizeSemanticReferenceCandidate(&candidate)
	candidate.IsErrorWrapper = candidate.IsBotMessage && semanticErrorWrapperText(candidate.Text)
	return candidate
}

func eventHasDirectReferenceContent(event MessageEvent) bool {
	for _, segment := range event.Segments {
		switch segment.Type {
		case "image", "video", "file", "record", "forward", "json", "xml":
			return true
		}
	}
	return false
}

func (r *Runtime) semanticReferenceCandidates(ctx context.Context, event MessageEvent) ([]semanticReferenceCandidate, map[string]MessageEvent, int64) {
	history, anchorTime := r.semanticReferenceHistory(ctx, event)
	indexes := semanticReferenceCandidateIndexes(history, event, anchorTime)
	candidates := make([]semanticReferenceCandidate, 0, len(indexes))
	events := make(map[string]MessageEvent, len(indexes))
	botQQ := r.effectiveConfigForEvent(event).BotQQ
	for _, index := range indexes {
		item := history[index]
		messageID := strings.TrimSpace(item.MessageID)
		if messageID == "" || messageID == event.MessageID {
			continue
		}
		candidate := semanticReferenceCandidateFromEvent(item, event, botQQ)
		if eventContainsSemanticReferenceContent(item) {
			candidate.NearbyContext = semanticReferenceNearbyContext(history, index)
		}
		if len(candidate.Content) == 0 {
			continue
		}
		candidates = append(candidates, candidate)
		events[messageID] = item
	}
	return candidates, events, anchorTime
}

func (r *Runtime) hasDurableMediaBeyondRecentContext(ctx context.Context, event MessageEvent) bool {
	recent := r.contextHistory(event)
	recentIDs := make(map[string]bool, len(recent))
	for _, item := range recent {
		if id := strings.TrimSpace(item.MessageID); id != "" {
			recentIDs[id] = true
		}
	}
	history, _ := r.semanticReferenceHistory(ctx, event)
	for _, item := range history {
		if !segmentsHaveReferenceContent(item.Segments) && !quotedMessageHasReferenceContent(item.Quoted) {
			continue
		}
		id := strings.TrimSpace(item.MessageID)
		if id != "" && !recentIDs[id] {
			return true
		}
	}
	return false
}

func semanticReferenceCandidateFromEvent(item, current MessageEvent, botQQ string) semanticReferenceCandidate {
	candidate := semanticReferenceCandidate{
		MessageID:                strings.TrimSpace(item.MessageID),
		Sender:                   strings.TrimSpace(item.SenderNameOrID()),
		Text:                     truncateRunesFromStart(historyPlainText(item), 280),
		EventTime:                item.Time,
		AgeSeconds:               proactiveReplyMessageAge(current.Time, item.Time),
		ReplyToMessageIDs:        replyReferenceIDs(item.Segments),
		SemanticSourceMessageID:  strings.TrimSpace(item.SemanticSourceMessageID),
		SemanticSourceMessageIDs: eventSemanticSourceMessageIDs(item),
		IsBotMessage:             strings.TrimSpace(botQQ) != "" && strings.TrimSpace(item.UserID) == strings.TrimSpace(botQQ),
	}
	countSemanticReferenceSegments(&candidate, item.Segments)
	if item.Quoted != nil {
		candidate.QuotedMessageID = strings.TrimSpace(item.Quoted.MessageID)
		if candidate.SemanticSourceMessageID == "" {
			candidate.SemanticSourceMessageID = strings.TrimSpace(item.Quoted.SemanticSourceMessageID)
		}
		candidate.SemanticSourceMessageIDs = uniqueNonEmptyStrings(append(candidate.SemanticSourceMessageIDs, quotedSemanticSourceMessageIDs(item.Quoted)...)...)
		countSemanticReferenceSegments(&candidate, item.Quoted.Segments)
	}
	finalizeSemanticReferenceCandidate(&candidate)
	candidate.IsErrorWrapper = candidate.IsBotMessage && semanticErrorWrapperText(candidate.Text)
	return candidate
}

func countSemanticReferenceSegments(candidate *semanticReferenceCandidate, segments []MessageSegment) {
	for _, segment := range segments {
		switch segment.Type {
		case "video":
			candidate.VideoCount++
		case "image":
			if segment.Data["source_type"] != "video_frame" {
				candidate.ImageCount++
			}
		case "record":
			candidate.AudioCount++
		case "file":
			if videoFileSegment(segment) {
				candidate.VideoCount++
			} else {
				candidate.FileCount++
			}
		case "forward":
			candidate.Content = appendUniqueStrings(candidate.Content, "forward")
		}
	}
}

func finalizeSemanticReferenceCandidate(candidate *semanticReferenceCandidate) {
	if candidate.VideoCount > 0 {
		candidate.Content = appendUniqueStrings(candidate.Content, "video")
	}
	if candidate.ImageCount > 0 {
		candidate.Content = appendUniqueStrings(candidate.Content, "image")
	}
	if candidate.AudioCount > 0 {
		candidate.Content = appendUniqueStrings(candidate.Content, "audio")
	}
	if candidate.FileCount > 0 {
		candidate.Content = appendUniqueStrings(candidate.Content, "file")
	}
	if candidate.Text != "" {
		candidate.Content = appendUniqueStrings(candidate.Content, "text")
	}
}

func semanticErrorWrapperText(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "出错了：") ||
		strings.HasPrefix(text, "重试失败：") ||
		strings.Contains(text, "请求处理超时") ||
		strings.Contains(text, "模型服务响应超时") ||
		strings.Contains(text, "Agent 已达到最大步骤数")
}

func (r *Runtime) semanticReferenceHistory(ctx context.Context, event MessageEvent) ([]MessageEvent, int64) {
	recent := r.contextHistory(event)
	anchorTime := event.Time
	if anchorTime <= 0 {
		anchorTime = time.Now().Unix()
	}

	var anchorEvent MessageEvent
	if event.Quoted != nil && strings.TrimSpace(event.Quoted.MessageID) != "" {
		if record, ok := r.findSemanticReferenceEvent(ctx, event, event.Quoted.MessageID); ok {
			anchorEvent = record
			if record.Time > 0 {
				anchorTime = record.Time
			}
			recent = append(recent, record)
		}
	}

	fromTime := anchorTime - int64(semanticReferenceDefaultLookback/time.Second)
	throughTime := anchorTime
	if event.Quoted != nil {
		fromTime = anchorTime - int64(semanticReferenceQuotedLookback/time.Second)
		throughTime = anchorTime + int64(semanticReferenceQuotedLookahead/time.Second)
		if event.Time > 0 && throughTime > event.Time {
			throughTime = event.Time
		}
	}
	if fromTime < 0 {
		fromTime = 0
	}

	r.mu.RLock()
	store := r.messageStore
	r.mu.RUnlock()
	var timeline []MessageEvent
	if historyStore, ok := store.(MessageTimelineStore); ok {
		loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		loaded, err := historyStore.ListMessageEventsBetween(loadCtx, sessionKey(event), fromTime, throughTime)
		cancel()
		if err == nil {
			timeline = loaded
		}
	}

	sourceIDs := append([]string(nil), eventSemanticSourceMessageIDs(event)...)
	sourceIDs = append(sourceIDs, quotedSemanticSourceMessageIDs(event.Quoted)...)
	sourceIDs = append(sourceIDs, eventSemanticSourceMessageIDs(anchorEvent)...)
	sourceIDs = append(sourceIDs, quotedSemanticSourceMessageIDs(anchorEvent.Quoted)...)
	for _, sourceID := range dedupeStrings(sourceIDs) {
		if record, ok := r.findSemanticReferenceEvent(ctx, event, sourceID); ok {
			timeline = append(timeline, record)
		}
	}
	return mergeSemanticReferenceHistory(timeline, recent), anchorTime
}

func (r *Runtime) findSemanticReferenceEvent(ctx context.Context, event MessageEvent, messageID string) (MessageEvent, bool) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return MessageEvent{}, false
	}
	session := sessionKey(event)
	r.mu.RLock()
	for i := len(r.history[session]) - 1; i >= 0; i-- {
		if r.history[session][i].MessageID == messageID {
			record := r.history[session][i]
			r.mu.RUnlock()
			return record, true
		}
	}
	store := r.messageStore
	r.mu.RUnlock()
	lookup, ok := store.(MessageEventLookupStore)
	if !ok {
		return MessageEvent{}, false
	}
	loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	record, found, err := lookup.FindMessageEvent(loadCtx, session, messageID)
	if err != nil || !found {
		return MessageEvent{}, false
	}
	return record, true
}

func mergeSemanticReferenceHistory(groups ...[]MessageEvent) []MessageEvent {
	merged := make([]MessageEvent, 0)
	positions := map[string]int{}
	for _, group := range groups {
		for _, event := range group {
			key := messageHistoryDedupeKey(event)
			if key != "" {
				if index, ok := positions[key]; ok {
					merged[index] = event
					continue
				}
				positions[key] = len(merged)
			}
			merged = append(merged, event)
		}
	}
	sort.SliceStable(merged, func(left, right int) bool {
		return merged[left].Time < merged[right].Time
	})
	return merged
}

func semanticReferenceCandidateIndexes(history []MessageEvent, event MessageEvent, anchorTime int64) []int {
	include := map[int]bool{}
	start := len(history) - semanticReferenceRecentCandidateLimit
	if start < 0 {
		start = 0
	}
	for index := start; index < len(history); index++ {
		include[index] = true
	}

	importantIDs := map[string]bool{}
	addImportantID := func(messageID string) {
		if messageID = strings.TrimSpace(messageID); messageID != "" {
			importantIDs[messageID] = true
		}
	}
	if event.Quoted != nil {
		addImportantID(event.Quoted.MessageID)
		for _, messageID := range quotedSemanticSourceMessageIDs(event.Quoted) {
			addImportantID(messageID)
		}
	}
	for _, messageID := range eventSemanticSourceMessageIDs(event) {
		addImportantID(messageID)
	}
	for index, item := range history {
		if !importantIDs[strings.TrimSpace(item.MessageID)] {
			continue
		}
		left := index - semanticReferenceQuoteContextRadius
		if left < 0 {
			left = 0
		}
		right := index + semanticReferenceQuoteContextRadius
		if right >= len(history) {
			right = len(history) - 1
		}
		for nearby := left; nearby <= right; nearby++ {
			include[nearby] = true
		}
	}

	mediaIndexes := make([]int, 0)
	for index, item := range history {
		if segmentsHaveReferenceContent(item.Segments) || quotedMessageHasReferenceContent(item.Quoted) {
			mediaIndexes = append(mediaIndexes, index)
		}
	}
	sort.SliceStable(mediaIndexes, func(left, right int) bool {
		leftDistance := semanticReferenceTimeDistance(history[mediaIndexes[left]].Time, anchorTime)
		rightDistance := semanticReferenceTimeDistance(history[mediaIndexes[right]].Time, anchorTime)
		return leftDistance < rightDistance
	})
	if len(mediaIndexes) > semanticReferenceMediaCandidateLimit {
		mediaIndexes = mediaIndexes[:semanticReferenceMediaCandidateLimit]
	}
	for _, index := range mediaIndexes {
		include[index] = true
	}

	indexes := make([]int, 0, len(include))
	for index := range include {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes
}

func eventContainsSemanticReferenceContent(event MessageEvent) bool {
	if segmentsHaveReferenceContent(event.Segments) || quotedMessageHasReferenceContent(event.Quoted) {
		return true
	}
	return len(eventSemanticSourceMessageIDs(event)) > 0
}

func semanticReferenceTimeDistance(eventTime, anchorTime int64) int64 {
	distance := eventTime - anchorTime
	if distance < 0 {
		return -distance
	}
	return distance
}

func semanticReferenceNearbyContext(history []MessageEvent, index int) []string {
	left := index - semanticReferenceNearbyContextRadius
	if left < 0 {
		left = 0
	}
	right := index + semanticReferenceNearbyContextRadius
	if right >= len(history) {
		right = len(history) - 1
	}
	contextLines := make([]string, 0, right-left)
	for nearby := left; nearby <= right; nearby++ {
		if nearby == index {
			continue
		}
		item := history[nearby]
		text := truncateRunesFromStart(historyPlainText(item), 180)
		if text == "" {
			continue
		}
		contextLines = append(contextLines, strings.Join([]string{
			"message_id=" + strings.TrimSpace(item.MessageID),
			"sender=" + strings.TrimSpace(item.SenderNameOrID()),
			"text=" + text,
		}, " "))
	}
	return contextLines
}

func (r *Runtime) semanticSelectedQuoted(ctx context.Context, event, selected MessageEvent) *QuotedMessage {
	if segmentsHaveReferenceContent(selected.Segments) {
		return quotedMessageFromHistory(selected)
	}
	if quotedMessageHasReferenceContent(selected.Quoted) {
		quoted := *selected.Quoted
		return &quoted
	}
	sourceIDs := append([]string(nil), eventSemanticSourceMessageIDs(selected)...)
	sourceIDs = append(sourceIDs, quotedSemanticSourceMessageIDs(selected.Quoted)...)
	for _, sourceID := range dedupeStrings(sourceIDs) {
		if source, ok := r.findSemanticReferenceEvent(ctx, event, sourceID); ok && segmentsHaveReferenceContent(source.Segments) {
			return quotedMessageFromHistory(source)
		}
	}
	return quotedMessageFromHistory(selected)
}

func (r *Runtime) updateRememberedSemanticReference(event MessageEvent) {
	if strings.TrimSpace(event.MessageID) == "" || len(eventSemanticSourceMessageIDs(event)) == 0 {
		return
	}
	event = withoutReplyRuntimeState(event)
	session := sessionKey(event)
	r.mu.Lock()
	for index := len(r.history[session]) - 1; index >= 0; index-- {
		if r.history[session][index].MessageID != event.MessageID {
			continue
		}
		r.history[session][index] = event
		break
	}
	r.mu.Unlock()
	r.persistMessageEvent(event)
}

func (r *Runtime) messageEventWithLatestSemanticSource(event MessageEvent) MessageEvent {
	if strings.TrimSpace(event.MessageID) == "" || len(eventSemanticSourceMessageIDs(event)) > 0 {
		return event
	}
	session := sessionKey(event)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for index := len(r.history[session]) - 1; index >= 0; index-- {
		stored := r.history[session][index]
		if stored.MessageID != event.MessageID || len(eventSemanticSourceMessageIDs(stored)) == 0 {
			continue
		}
		setEventSemanticSourceMessageIDs(&event, eventSemanticSourceMessageIDs(stored))
		return event
	}
	return event
}

func parseSemanticReferenceDecision(raw string) (semanticReferenceDecision, bool) {
	raw = strings.TrimSpace(stripJSONCodeFence(raw))
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return semanticReferenceDecision{}, false
	}
	var decision semanticReferenceDecision
	if err := json.Unmarshal([]byte(raw[start:end+1]), &decision); err != nil {
		return semanticReferenceDecision{}, false
	}
	decision.MessageID = strings.TrimSpace(decision.MessageID)
	decision.MessageIDs = uniqueNonEmptyStrings(decision.MessageIDs...)
	if len(decision.MessageIDs) == 0 && decision.MessageID != "" && decision.MessageID != "none" {
		decision.MessageIDs = []string{decision.MessageID}
	}
	filtered := decision.MessageIDs[:0]
	for _, messageID := range decision.MessageIDs {
		if messageID != "none" {
			filtered = append(filtered, messageID)
		}
	}
	decision.MessageIDs = filtered
	if len(decision.MessageIDs) > 0 {
		decision.MessageID = decision.MessageIDs[0]
	}
	if decision.Confidence < 0 || decision.Confidence > 1 {
		return semanticReferenceDecision{}, false
	}
	return decision, true
}

func (r *Runtime) recordSemanticReference(ctx context.Context, event MessageEvent, messageIDs []string, confidence float64, routeErr error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	messageIDs = uniqueNonEmptyStrings(messageIDs...)
	messageID := ""
	if len(messageIDs) > 0 {
		messageID = messageIDs[0]
	}
	entry := applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "qqbot.semantic_reference",
		Message: "LLM 已完成上下文指代判断",
		Actor:   qqEventActor(event),
		Target:  messageID,
		Metadata: map[string]any{
			"group_id":    event.GroupID,
			"user_id":     event.UserID,
			"message_id":  messageID,
			"message_ids": append([]string(nil), messageIDs...),
			"confidence":  confidence,
		},
	}
	if routeErr != nil {
		entry.Kind = applog.KindError
		entry.Level = applog.LevelError
		entry.Message = "LLM 上下文指代判断失败"
		entry.Detail = routeErr.Error()
	}
	_ = writer.AppendLog(ctx, entry)
}
