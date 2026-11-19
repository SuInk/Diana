package assistant

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

// requestContextLimit 返回机器人或群显式配置的单次请求上下文上限，0 表示跟随模型窗口。
// 以前留空时落到 64K 的默认上限，再按各层份额切；现在照 Pi，窗口就是模型窗口，
// 只在快满时压缩一次（见 context_compaction.go），不再预先限额。
func (c BotConfig) requestContextLimit() int64 {
	return max(0, c.MaxContextTokens)
}

type historyContextTurn struct {
	events       []MessageEvent
	estimated    int64
	lastUserID   string
	lastTime     int64
	hasAssistant bool
}

// visiblePromptHistory 只留本群（或本会话）的历史，并去掉当前这条消息自己。
func visiblePromptHistory(event MessageEvent, history []MessageEvent) []MessageEvent {
	out := history[:0]
	for _, item := range history {
		if event.Kind == EventKindGroup && !samePromptGroupScope(event, item) {
			continue
		}
		if strings.TrimSpace(event.MessageID) != "" && item.MessageID == event.MessageID {
			continue
		}
		out = append(out, item)
	}
	return out
}

// promptContextHistory 返回这一轮要带上的会话历史：从锚（会话起点或上次压缩后
// 第一条保留的消息）开始的全部历史，不按预算裁剪。体积由压缩控制，见
// context_compaction.go。
func (r *Runtime) promptContextHistory(event MessageEvent, cfg BotConfig) []MessageEvent {
	window := r.promptCompactionWindow(event, cfg)
	candidateLimit := historyCandidateLimit(window)
	session := sessionKey(event)
	anchorKey := compactionAnchorKey(event)
	if anchor := r.groupPromptSession(event).anchor(); anchor != "" {
		r.setHistoryWindowAnchor(anchorKey, anchor)
	}
	anchor := r.historyWindowAnchor(anchorKey)
	checkpoint := r.compactionState(event)

	r.mu.RLock()
	memory := append([]MessageEvent(nil), r.history[session]...)
	if event.Kind == EventKindGroup {
		if buffer, ok := r.groupPromptHistory[groupPromptSessionKey(event)]; ok {
			memory = append([]MessageEvent(nil), buffer.Events...)
		}
	}
	store := r.messageStore
	r.mu.RUnlock()

	var history []MessageEvent
	// 机器人很久没说话、群里一直在聊时，锚之后的历史会多过按阈值换算的条数。锚（或
	// 摘要之后的那段）没读全就加倍往前读，直到接上或存储里已经没有更早的。
	for limit := candidateLimit; ; limit *= 2 {
		stored := []MessageEvent(nil)
		if store != nil {
			loadCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			var err error
			stored, err = listContextMessageEvents(loadCtx, store, session, limit)
			cancel()
			if err != nil {
				log.Printf("diana context history load failed: %v", err)
			}
		}
		history = visiblePromptHistory(event, mergeMessageHistory(memory, stored, limit))
		if store == nil || len(stored) < limit || historyCoversCheckpoint(history, anchor, checkpoint) {
			break
		}
	}
	selected := r.sessionHistoryWindow(event, history, cfg.BotAccount, window)
	if !boolValue(cfg.CrossGroupMemoryEnabled, false) {
		return selected
	}
	// The session window covers this session only. Preserve the cross-group
	// references already selected during routing instead of silently losing them.
	var crossGroup []MessageEvent
	if event.replyHistoryLoaded {
		for _, item := range event.replyHistory {
			if item.crossGroupContext {
				crossGroup = append(crossGroup, item)
			}
		}
	} else if store != nil {
		crossGroup = r.crossGroupContextEvents(event, store)
	}
	return mergeCrossGroupContextHistory(selected, crossGroup)
}

// historyWindowAnchorIndex 返回锚在历史里的下标；锚为空或已经不在历史里（被撤回、
// 清空了上下文）返回 -1，调用方按冷启动重新锚定。
func historyWindowAnchorIndex(window []MessageEvent, anchor string) int {
	if anchor == "" {
		return -1
	}
	for index, event := range window {
		if messageHistoryDedupeKey(event) == anchor {
			return index
		}
	}
	return -1
}

func (r *Runtime) historyWindowAnchor(session string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.historyWindowAnchors[session]
}

func (r *Runtime) setHistoryWindowAnchor(session string, anchor string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if anchor == "" {
		delete(r.historyWindowAnchors, session)
		return
	}
	if r.historyWindowAnchors == nil {
		r.historyWindowAnchors = make(map[string]string)
	}
	r.historyWindowAnchors[session] = anchor
}

// promptContextWindowTokens 取本轮可能被选中的配置档里最小的那个请求上下文上限：
// 编排必须对每个候选模型都成立。以前它以 DefaultContextWindowTokens 起算、且只
// 往下收，于是无论模型多大窗口都出不了 16K——那个常量是「没有配置档时的兜底」，
// 不该当成所有模型的天花板。
// promptChatProfiles 是本轮可能被选中的对话模型配置档，挑选顺序与真正发请求时一致：
// 角色绑定 → 分组 → 当前激活配置。只看角色绑定的话，没配模型角色的部署会一路回落到
// 兜底常量，等于从来没看过实际在用的模型窗口。带图的轮次也由对话模型回答。
func (r *Runtime) promptChatProfiles() []llm.Profile {
	r.mu.RLock()
	store := r.llmStore
	r.mu.RUnlock()
	if store == nil {
		return nil
	}
	set := store.Profiles().WithDefaults()
	profiles, _ := r.roleBoundProfiles("", set, llm.GroupChat)
	if len(profiles) == 0 {
		profiles = llmProfilesInGroup(set, llm.GroupChat)
	}
	if len(profiles) == 0 {
		if current, ok := set.FirstProfile(); ok {
			profiles = []llm.Profile{current}
		}
	}
	return profiles
}

func (r *Runtime) promptContextWindowTokens(event MessageEvent, cfg BotConfig) int64 {
	r.mu.RLock()
	store := r.llmStore
	r.mu.RUnlock()
	if store == nil {
		// 没有配置档时用兜底常量当窗口，但仍要过下面那道配置收紧：这里以前直接
		// 返回常量，于是机器人或群配的 max_context_tokens 在这条路径上完全不生效。
		return clampContextWindowToConfig(llm.DefaultContextWindowTokens, cfg)
	}
	profiles := r.promptChatProfiles()
	window := int64(0)
	for _, profile := range profiles {
		providerWindow := profile.Config.MaxContextTokensWithDefault()
		// 从超限报错里学到过真实窗口就按它算，否则每轮都会先撞一次超限再压缩。
		if learned, ok := llm.LearnedContextLimit(profile.Config, profile.Config.Model); ok && (providerWindow <= 0 || learned < providerWindow) {
			providerWindow = learned
		}
		if providerWindow <= 0 {
			continue
		}
		if window == 0 || providerWindow < window {
			window = providerWindow
		}
	}
	if window <= 0 {
		window = llm.DefaultContextWindowTokens
	}
	return clampContextWindowToConfig(window, cfg)
}

// promptContextWindowFallback 报告对话模型的窗口是不是没人填、用的兜底常量：配置档里
// 一个都没写窗口，机器人也没设上限。界面要把这种窗口标出来，免得被当成模型的真实上限。
func (r *Runtime) promptContextWindowFallback(cfg BotConfig) bool {
	if cfg.MaxContextTokens > 0 {
		return false
	}
	r.mu.RLock()
	store := r.llmStore
	r.mu.RUnlock()
	if store == nil {
		return true
	}
	set := store.Profiles().WithDefaults()
	profiles, _ := r.roleBoundProfiles("", set, llm.GroupChat)
	if len(profiles) == 0 {
		profiles = llmProfilesInGroup(set, llm.GroupChat)
	}
	if len(profiles) == 0 {
		if current, ok := set.FirstProfile(); ok {
			profiles = []llm.Profile{current}
		}
	}
	for _, profile := range profiles {
		cfg := profile.Config.WithDefaults()
		if _, source := cfg.ResolveContextWindowTokens(); source != llm.ContextWindowSourceFallback || cfg.MaxContextTokens > 0 {
			return false
		}
	}
	return true
}

// clampContextWindowToConfig 让机器人/群显式配置的上限只能收紧：模型窗口是能力上限，
// 配置里那个数是这个机器人愿意在单次请求上花多少。没配就跟随模型窗口。
func clampContextWindowToConfig(window int64, cfg BotConfig) int64 {
	if limit := cfg.requestContextLimit(); limit > 0 && limit < window {
		return limit
	}
	return window
}

func selectRecentHistoryTurns(history []MessageEvent, current MessageEvent, botAccount string, budget int64) []MessageEvent {
	if len(history) == 0 || budget <= 0 {
		return nil
	}
	turns := groupHistoryContextTurns(history, current.Time, botAccount)
	remaining := budget
	first := len(turns)
	for index := len(turns) - 1; index >= 0; index-- {
		turn := turns[index]
		if turn.estimated > remaining {
			// 最新的一轮再大也要带上：丢了它模型就像没看见刚发的消息。
			if first == len(turns) && index == len(turns)-1 {
				first = index
			}
			break
		}
		remaining -= turn.estimated
		first = index
	}
	if first == len(turns) {
		return nil
	}
	selected := make([]MessageEvent, 0)
	for _, turn := range turns[first:] {
		selected = append(selected, turn.events...)
	}
	return selected
}

func groupHistoryContextTurns(history []MessageEvent, currentTime int64, botAccount string) []historyContextTurn {
	turns := make([]historyContextTurn, 0, len(history))
	for _, event := range history {
		assistantEvent := strings.TrimSpace(event.botReply) != "" || assistantHistoryEvent(event, botAccount)
		cost := estimateHistoryContextEventTokens(event, currentTime, assistantEvent)
		if assistantEvent && len(turns) > 0 && !turns[len(turns)-1].hasAssistant {
			last := &turns[len(turns)-1]
			last.events = append(last.events, event)
			last.estimated += cost
			last.hasAssistant = true
			last.lastTime = max(last.lastTime, event.Time)
			continue
		}
		if !assistantEvent && len(turns) > 0 {
			last := &turns[len(turns)-1]
			closeInTime := event.Time > 0 && last.lastTime > 0 && event.Time-last.lastTime >= 0 && event.Time-last.lastTime <= 120
			if !last.hasAssistant && closeInTime && strings.TrimSpace(last.lastUserID) == strings.TrimSpace(event.UserID) {
				last.events = append(last.events, event)
				last.estimated += cost
				last.lastTime = event.Time
				continue
			}
		}
		turns = append(turns, historyContextTurn{
			events:       []MessageEvent{event},
			estimated:    cost,
			lastUserID:   event.UserID,
			lastTime:     event.Time,
			hasAssistant: assistantEvent,
		})
	}
	return turns
}

func estimateHistoryContextEventTokens(event MessageEvent, currentTime int64, assistantEvent bool) int64 {
	text := ""
	if strings.TrimSpace(event.botReply) != "" {
		text = event.botReply
	} else if assistantEvent {
		text = historyPlainText(event)
		if note, ok := replyToolTraceNote(event.ToolCalls); ok {
			text += note.Content
		}
	} else {
		text = historyPromptTextAt(event, currentTime)
	}
	// The final prompt adds message framing, and image summaries can be filled in
	// after selection. Reserve enough space without charging full vision tokens,
	// because historical images are represented by text until explicitly loaded.
	cost := llm.EstimateTextTokens(text) + 8
	cost += int64(historicalStillImageCount(event)) * 192
	if cost < minimumHistoryCandidateTokens {
		cost = minimumHistoryCandidateTokens
	}
	return cost
}

// recordPromptContextBudget 把这一轮上下文的体积写进调试记录：窗口、压缩阈值、
// 各类内容的估算 token，以及历史从哪一刻开始。没有任何裁剪可报，只有账。
func (r *Runtime) recordPromptContextBudget(ctx context.Context, event MessageEvent, cfg BotConfig, messages []llm.Message, history []MessageEvent, compacted string, semantic semanticReferencePromptContext, sources semanticReferenceContext) {
	if !debugModeEnabled(cfg) {
		return
	}
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	categoryTokens := map[string]int64{
		"system": 0, "plugin_and_tools": 0, "memory": 0,
		"summary": 0, "history": 0, "current": 0,
	}
	var total int64
	for _, message := range messages {
		cost := llm.EstimateMessageTokens(message)
		total += cost
		switch message.Priority {
		case llm.MessagePriorityCurrent:
			categoryTokens["current"] += cost
		case llm.MessagePriorityPlugin:
			categoryTokens["plugin_and_tools"] += cost
		case llm.MessagePriorityMemory:
			categoryTokens["memory"] += cost
		case llm.MessagePrioritySummary:
			categoryTokens["summary"] += cost
		case llm.MessagePrioritySystem:
			categoryTokens["system"] += cost
		default:
			categoryTokens["history"] += cost
		}
	}
	window := r.promptContextWindowTokens(event, cfg)
	earliest, latest := int64(0), int64(0)
	for _, item := range history {
		if item.Time <= 0 {
			continue
		}
		if earliest == 0 || item.Time < earliest {
			earliest = item.Time
		}
		if item.Time > latest {
			latest = item.Time
		}
	}
	metadata := map[string]any{
		"effective_context_window":  window,
		"compaction_threshold":      compactionThreshold(r.promptCompactionWindow(event, cfg)),
		"keep_recent_tokens":        compactionKeepRecent(r.promptCompactionWindow(event, cfg), 0),
		"estimated_tokens":          total,
		"category_tokens":           categoryTokens,
		"history_selected_messages": len(history),
		"history_selected_turns":    len(groupHistoryContextTurns(history, event.Time, cfg.BotAccount)),
		"history_earliest_time":     earliest,
		"history_latest_time":       latest,
		"compaction_summary":        strings.TrimSpace(r.compactionSummary(event)) != "",
		"compacted":                 compacted,
		"semantic_requested":        semantic.Requested,
		"semantic_resolved":         semantic.Resolved,
		"semantic_text_sources":     semantic.TextSources,
		"semantic_expected_images":  semantic.ExpectedImages,
		"semantic_attached_images":  sources.AttachedImageCount,
		"semantic_missing_sources":  sources.MissingSourceCount,
		"message_id":                event.MessageID,
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:     applog.KindDebug,
		Level:    applog.LevelInfo,
		Action:   "context_budget",
		Message:  "上下文已编排",
		Actor:    "chat_context",
		Target:   strings.TrimSpace(event.MessageID),
		Metadata: metadata,
	})
}

// ContextBudgetBreakdown 是某个会话当前的上下文概览：窗口多大、什么时候压缩、
// 压缩时保留多少近期原文。照 Pi，平时不分层限额，全部原样带上。
type ContextBudgetBreakdown struct {
	GroupID string `json:"group_id,omitempty"`
	// ContextWindow 是这个群实际会用到的模型窗口。
	ContextWindow int64 `json:"context_window"`
	// ContextWindowFallback 为 true 时窗口没人填，是兜底常量，不代表模型的真实上限。
	ContextWindowFallback bool `json:"context_window_fallback,omitempty"`
	// CompactionThreshold 是请求估算超过它就压缩一次的 token 数（窗口减 ReserveTokens）。
	CompactionThreshold int64 `json:"compaction_threshold"`
	ReserveTokens       int64 `json:"reserve_tokens"`
	// KeepRecentTokens 是压缩时原样保留的近期历史。
	KeepRecentTokens int64 `json:"keep_recent_tokens"`
	// HasSummary 表示这个会话已经压缩过，历史开头是一份摘要。
	HasSummary bool `json:"has_summary"`
}

// ContextBudgetBreakdownForGroup 按机器人和群算出当前的上下文概览。profileID 要带上：
// 几台机器人时不带就落回默认配置，窗口和分群覆盖都是别人的。
func (r *Runtime) ContextBudgetBreakdownForGroup(profileID, groupID string) ContextBudgetBreakdown {
	event := MessageEvent{Kind: EventKindGroup, GroupID: strings.TrimSpace(groupID), ProfileID: strings.TrimSpace(profileID)}
	if event.GroupID == "" {
		event.Kind = EventKindPrivate
	}
	cfg := r.effectiveConfigForEvent(event)
	window := r.promptContextWindowTokens(event, cfg)
	limits := r.promptCompactionWindow(event, cfg)
	return ContextBudgetBreakdown{
		GroupID:               event.GroupID,
		ContextWindow:         window,
		ContextWindowFallback: r.promptContextWindowFallback(cfg),
		CompactionThreshold:   compactionThreshold(limits),
		ReserveTokens:         compactionReserve(limits),
		KeepRecentTokens:      compactionKeepRecent(limits, 0),
		HasSummary:            event.GroupID != "" && strings.TrimSpace(r.compactionSummary(event)) != "",
	}
}
