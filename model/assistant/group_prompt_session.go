package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// GroupPromptSession contains only group-public history, its summary and tool names. Never
// persist retrieved memories, private thread state, credentials or tool objects.
// The archive remains authoritative for edits, deletions and context resets.
type GroupPromptSession struct {
	Generation  int64    `json:"generation"`
	Anchor      string   `json:"anchor,omitempty"`
	Checkpoint  string   `json:"checkpoint,omitempty"`
	LoadedTools []string `json:"loaded_tools,omitempty"`
	// ToolLastUsed 记着每个已加载工具最近一次加载或调用的时间（Unix 秒），
	// loadedTools 按它淘汰久未使用的工具。没有记录的视同很久没用。
	ToolLastUsed map[string]int64          `json:"tool_last_used,omitempty"`
	History      []GroupPromptHistoryEntry `json:"history,omitempty"`
}

// promptContextSummary reads the source at assembly time, not the earlier routing
// snapshot. Taking the session lock before reading prevents an older request from
// publishing its checkpoint after a newer one. Store I/O never holds r.mu.
func (r *Runtime) promptContextSummary(event MessageEvent, sessionThread string, budget int64) (string, bool) {
	s := r.groupPromptSession(event)
	r.mu.RLock()
	if s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.invalidated {
			r.mu.RUnlock()
			return "", false
		}
	}
	var summary string
	if r.structuredMemory == nil && strings.TrimSpace(sessionThread) == "" {
		var present bool
		summary, present = r.contextSummaries[sessionKey(event)]
		if !present && s != nil {
			summary = s.data.Checkpoint
		}
	}
	summary, recompressed := r.fitOlderSummaryToBudget(rawMessageWithoutImagePlaceholders(summary), budget)
	changed := s != nil && s.data.Checkpoint != summary
	if changed {
		s.data.Checkpoint = summary
	}
	r.mu.RUnlock()
	if changed {
		s.saveLocked()
		if s.invalidated {
			return "", false
		}
	}
	return summary, recompressed
}

type GroupPromptHistoryEntry struct {
	Key        string        `json:"key"`
	SourceHash string        `json:"source_hash"`
	Messages   []llm.Message `json:"messages"`
}

// Save must atomically reject a stale generation after ResetContextHistory.
type GroupPromptSessionStore interface {
	LoadGroupPromptSession(context.Context, string, string) (GroupPromptSession, error)
	SaveGroupPromptSession(context.Context, string, string, GroupPromptSession) (bool, error)
}

type groupPromptSession struct {
	mu                  sync.Mutex
	key, session        string
	store               GroupPromptSessionStore
	data                GroupPromptSession
	loaded, invalidated bool
	historyRevision     uint64
}

// The short legacy history still drives background memory extraction. Its
// compaction must not evict a warm prompt's raw messages.
type groupPromptHistoryBuffer struct {
	Session string
	Events  []MessageEvent
}

func groupPromptSessionKey(event MessageEvent) string {
	// sessionKey alone is not sufficient when a Runtime serves multiple bots or
	// when two platforms use the same numeric group ID.
	return fmt.Sprintf("%q/%q/%q/%q", event.Platform, event.ProfileID, event.SelfID, sessionKey(event))
}

func samePromptGroupScope(current, item MessageEvent) bool {
	return !item.crossGroupContext &&
		(item.GroupID == "" || item.GroupID == current.GroupID) &&
		(item.ProfileID == "" || item.ProfileID == current.ProfileID) &&
		(item.Platform == "" || item.Platform == current.Platform) &&
		(item.SelfID == "" || item.SelfID == current.SelfID) &&
		(item.ContextNamespace == "" || item.ContextNamespace == current.ContextNamespace)
}

func (r *Runtime) groupPromptSession(event MessageEvent) *groupPromptSession {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return nil
	}
	key := groupPromptSessionKey(event)
	r.mu.Lock()
	if r.groupPromptSessions == nil {
		r.groupPromptSessions = make(map[string]*groupPromptSession)
	}
	s := r.groupPromptSessions[key]
	if s != nil {
		s.mu.Lock()
		invalidated := s.invalidated
		s.mu.Unlock()
		if invalidated {
			s = nil
		}
	}
	if s == nil {
		store, _ := r.messageStore.(GroupPromptSessionStore)
		s = &groupPromptSession{key: key, session: sessionKey(event), store: store}
		r.groupPromptSessions[key] = s
	}
	r.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		if s.store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			data, err := s.store.LoadGroupPromptSession(ctx, s.key, s.session)
			cancel()
			if err != nil {
				log.Printf("diana group prompt session load failed: %v", err)
				return nil // retry next turn; do not overwrite a snapshot we could not read
			}
			s.data = data
		}
		s.loaded = true
	}
	if s.invalidated {
		return nil
	}
	return s
}

func (s *groupPromptSession) saveLocked() {
	if s.store == nil || s.invalidated {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ok, err := s.store.SaveGroupPromptSession(ctx, s.key, s.session, s.data)
	if err != nil {
		log.Printf("diana group prompt session save failed: %v", err)
		return
	}
	if !ok {
		s.invalidated = true
	}
}

// 群里加载过的工具以前只增不减：模型某一轮用过一次的工具，契约（description 加
// schema）此后每轮都带着。线上一个活跃群攒到 20 个、近 2 万字，比人设还长。现在
// 超过 loadedToolIdleTTL 没用过的退回按需目录，再用时模型重新 tool_search 一次；
// 同时最多保留 maxPersistedLoadedTools 个最近用过的。淘汰只在工具跨过期限时发生，
// 前缀缓存只在那一轮断一次。
const (
	loadedToolIdleTTL       = 48 * time.Hour
	maxPersistedLoadedTools = 8
	// loadedToolTouchInterval 内重复调用不再刷新时间，免得每轮都写库。
	loadedToolTouchInterval = time.Hour
)

var groupPromptSessionNow = time.Now

func (s *groupPromptSession) loadedTools() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalidated {
		return nil
	}
	if kept := s.data.liveLoadedTools(groupPromptSessionNow()); len(kept) != len(s.data.LoadedTools) {
		s.data.LoadedTools = kept
		for name := range s.data.ToolLastUsed {
			if !slices.Contains(kept, name) {
				delete(s.data.ToolLastUsed, name)
			}
		}
		s.saveLocked()
	}
	return append([]string(nil), s.data.LoadedTools...)
}

// liveLoadedTools 去掉过期的工具，超出上限时只留最近用过的那几个，其余保持原顺序：
// 契约按这个顺序渲染，顺序不动前缀才稳。
func (d GroupPromptSession) liveLoadedTools(now time.Time) []string {
	cutoff := now.Add(-loadedToolIdleTTL).Unix()
	live := make([]string, 0, len(d.LoadedTools))
	for _, name := range d.LoadedTools {
		if d.ToolLastUsed[name] > cutoff {
			live = append(live, name)
		}
	}
	if len(live) <= maxPersistedLoadedTools {
		return live
	}
	recent := slices.Clone(live)
	slices.SortStableFunc(recent, func(a, b string) int {
		return cmp.Compare(d.ToolLastUsed[b], d.ToolLastUsed[a])
	})
	keep := make(map[string]bool, maxPersistedLoadedTools)
	for _, name := range recent[:maxPersistedLoadedTools] {
		keep[name] = true
	}
	return slices.DeleteFunc(live, func(name string) bool { return !keep[name] })
}

func (s *groupPromptSession) rememberTools(names []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalidated {
		return
	}
	seen := make(map[string]bool, len(s.data.LoadedTools))
	for _, name := range s.data.LoadedTools {
		seen[name] = true
	}
	now := groupPromptSessionNow().Unix()
	changed := false
	for _, name := range names {
		if name != "" && !seen[name] {
			seen[name] = true
			s.data.LoadedTools = append(s.data.LoadedTools, name)
			s.touchToolLocked(name, now)
			changed = true
		}
	}
	if changed {
		s.saveLocked()
	}
}

// touchTools 在一轮回复结束后刷新本轮实际调用过的已加载工具。
func (s *groupPromptSession) touchTools(steps []agent.Step) {
	if s == nil || len(steps) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalidated {
		return
	}
	now := groupPromptSessionNow().Unix()
	changed := false
	for _, step := range steps {
		if step.Skipped || !slices.Contains(s.data.LoadedTools, step.Tool) {
			continue
		}
		if now-s.data.ToolLastUsed[step.Tool] >= int64(loadedToolTouchInterval/time.Second) {
			s.touchToolLocked(step.Tool, now)
			changed = true
		}
	}
	if changed {
		s.saveLocked()
	}
}

func (s *groupPromptSession) touchToolLocked(name string, now int64) {
	if s.data.ToolLastUsed == nil {
		s.data.ToolLastUsed = map[string]int64{}
	}
	s.data.ToolLastUsed[name] = now
}

func (s *groupPromptSession) anchor() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalidated {
		return ""
	}
	return s.data.Anchor
}

func (s *groupPromptSession) rememberAnchor(anchor string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalidated || s.data.Anchor == anchor {
		return
	}
	s.data.Anchor = anchor
	s.saveLocked()
}

// historyRenderVersion 进渲染缓存的源哈希：历史的渲染形态变了就加一，已存的条目
// 下一轮按新形态重渲染，不会新旧两种形态混在一份请求里。2 起机器人发言是 send 调用。
const historyRenderVersion = 2

// stableGroupHistory reuses each rendered public event across turns/restarts.
// The previous ordering wins for surviving entries; late backfills append.
// Removed/edited events deliberately invalidate the prefix instead of retaining
// withdrawn content. Memory retrieval is never part of this journal.
func (r *Runtime) stableGroupHistory(ctx context.Context, event MessageEvent, cfg BotConfig, history []MessageEvent, direct bool, skip map[string]bool) ([]llm.Message, []llm.Message) {
	s := r.groupPromptSession(event)
	var previous []GroupPromptHistoryEntry
	var revision uint64
	if s != nil {
		s.mu.Lock()
		s.historyRevision++
		revision = s.historyRevision
		previous = append(previous, s.data.History...)
		s.mu.Unlock()
	}
	old := make(map[string]GroupPromptHistoryEntry, len(previous))
	for _, entry := range previous {
		old[entry.Key] = entry
	}
	var localHistory []MessageEvent
	for _, item := range history {
		if !item.crossGroupContext && (event.Kind != EventKindGroup || samePromptGroupScope(event, item)) {
			localHistory = append(localHistory, item)
		}
	}
	groups, recent := historyContextMetadata(localHistory, event.Time, cfg.BotAccount)
	entries := make(map[string]GroupPromptHistoryEntry, len(history))
	var order []string
	var volatile []llm.Message
	for _, item := range history {
		if item.MessageID != "" && (item.MessageID == event.MessageID || skip[item.MessageID]) {
			continue
		}
		if item.crossGroupContext {
			if boolValue(cfg.CrossGroupMemoryEnabled, false) {
				volatile = append(volatile, r.renderPromptHistoryEvent(ctx, event, item, cfg, direct)...)
			}
			continue
		}
		if event.Kind == EventKindGroup && !samePromptGroupScope(event, item) {
			continue
		}
		key := messageHistoryDedupeKey(item)
		if key == "" {
			continue
		}
		// Source data, not asynchronous image descriptions or request-relative
		// timestamps, controls replacement. Edits must become visible immediately.
		source, _ := json.Marshal(struct {
			Event  MessageEvent
			Reply  string
			Direct bool
			Bot    string
			Render int
		}{withoutReplyRuntimeState(item), item.botReply, direct, cfg.BotAccount, historyRenderVersion})
		hash := promptCacheHash(string(source))
		entry, ok := old[key]
		if !ok || entry.SourceHash != hash {
			entry = GroupPromptHistoryEntry{Key: key, SourceHash: hash, Messages: r.renderPromptHistoryEvent(ctx, event, item, cfg, direct)}
		}
		if _, exists := entries[key]; !exists {
			order = append(order, key)
		}
		entries[key] = entry
	}
	var next []GroupPromptHistoryEntry
	for _, entry := range previous {
		if current, ok := entries[entry.Key]; ok {
			next = append(next, current)
			delete(entries, entry.Key)
		}
	}
	for _, key := range order {
		if entry, ok := entries[key]; ok {
			next = append(next, entry)
			delete(entries, key)
		}
	}
	if s != nil {
		s.mu.Lock()
		before, _ := json.Marshal(s.data.History)
		after, _ := json.Marshal(next)
		if !s.invalidated && s.historyRevision == revision && string(before) != string(after) {
			s.data.History = next
			s.saveLocked()
		}
		s.mu.Unlock()
	}
	var stable []llm.Message
	for _, entry := range next {
		for _, message := range entry.Messages {
			message.Priority = llm.MessagePriorityHistory
			if recent[entry.Key] {
				message.Priority = llm.MessagePriorityRecentHistory
			}
			message.ContextGroup = groups[entry.Key]
			stable = append(stable, message)
		}
	}
	return stable, volatile
}

// historyReplyMessages 把机器人自己发过的一句话写成 send 调用加「已发送」，不写成
// assistant 纯文本，原因见 llm.HistoryReplyCallIDPrefix。调用 ID 由这条历史的去重键
// 算出，逐轮不变，前缀缓存照常命中。
func historyReplyMessages(item MessageEvent, text string) []llm.Message {
	key := messageHistoryDedupeKey(item)
	if key == "" {
		key = strconv.FormatInt(item.Time, 10) + "|" + text
	}
	return agent.HistoryReplyMessages(promptCacheHash(key), text)
}

func (r *Runtime) renderPromptHistoryEvent(ctx context.Context, current, item MessageEvent, cfg BotConfig, direct bool) []llm.Message {
	if strings.TrimSpace(item.botReply) != "" {
		return historyReplyMessages(item, item.botReply)
	}
	if isPokeHistoryEvent(item) {
		text := pokeHistoryPromptText(item, cfg.BotAccount, current.SelfID, item.SelfID)
		return []llm.Message{{Role: llm.RoleUser, Content: text, Priority: llm.MessagePriorityHistory}}
	}
	if isSubscriptionPushEvent(item) {
		// 订阅推送不当 assistant 发言给模型看，否则模型会照着卡片格式自己拼一张。
		if text := subscriptionPushHistoryPromptText(item); text != "" {
			return []llm.Message{{Role: llm.RoleUser, Content: text, Priority: llm.MessagePriorityHistory}}
		}
		return nil
	}
	var result []llm.Message
	if !item.crossGroupContext && assistantHistoryEvent(item, firstNonEmpty(strings.TrimSpace(cfg.BotAccount), strings.TrimSpace(current.SelfID))) {
		if text := strings.TrimSpace(assistantHistoryPlainText(item)); text != "" {
			result = append(result, historyReplyMessages(item, text)...)
			if note, ok := replyToolTraceNote(item.ToolCalls); ok {
				result = append(result, note)
			}
		}
		if !direct || historicalMediaCount(item) == 0 {
			return result
		}
	}
	text := historyPromptTextAt(item, current.Time, cfg)
	if direct && historicalMediaCount(item) > 0 {
		text = agentImageHistoryPromptTextWithDescriptions(item, current.Time, r.historyPromptImageDescriptions(ctx, item), cfg)
	}
	if strings.TrimSpace(text) != "" {
		result = append(result, llm.Message{Role: llm.RoleUser, Content: text, Priority: llm.MessagePriorityHistory})
	}
	return result
}
