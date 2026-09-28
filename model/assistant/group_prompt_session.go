package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// GroupPromptSession contains only group-public history and tool names. Never
// persist retrieved memories, private thread state, credentials or tool objects.
// The archive remains authoritative for edits, deletions and context resets.
type GroupPromptSession struct {
	Generation  int64                     `json:"generation"`
	Anchor      string                    `json:"anchor,omitempty"`
	Checkpoint  string                    `json:"checkpoint,omitempty"`
	LoadedTools []string                  `json:"loaded_tools,omitempty"`
	History     []GroupPromptHistoryEntry `json:"history,omitempty"`
}

func (s *groupPromptSession) rememberCheckpoint(checkpoint string) string {
	checkpoint = strings.TrimSpace(checkpoint)
	if s == nil {
		return checkpoint
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalidated {
		return checkpoint
	}
	if checkpoint != "" && s.data.Checkpoint != checkpoint {
		s.data.Checkpoint = checkpoint
		s.saveLocked()
	}
	return s.data.Checkpoint
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

func (s *groupPromptSession) loadedTools() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalidated {
		return nil
	}
	return append([]string(nil), s.data.LoadedTools...)
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
	changed := false
	for _, name := range names {
		if name != "" && !seen[name] {
			seen[name] = true
			s.data.LoadedTools = append(s.data.LoadedTools, name)
			changed = true
		}
	}
	if changed {
		s.saveLocked()
	}
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

// stableGroupHistory reuses each rendered public event across turns/restarts.
// The previous ordering wins for surviving entries; late backfills append.
// Removed/edited events deliberately invalidate the prefix instead of retaining
// withdrawn content. Memory retrieval is never part of this journal.
func (r *Runtime) stableGroupHistory(ctx context.Context, event MessageEvent, cfg BotConfig, history []MessageEvent, direct bool, skip map[string]bool) ([]llm.Message, []llm.Message) {
	stable, volatile, _ := r.stableGroupHistoryKeepingTurn(ctx, event, cfg, history, direct, skip)
	return stable, volatile
}

// stableGroupHistoryKeepingTurn 同 stableGroupHistory，另外返回本轮的当前消息和同轮
// 补充里有哪几条留在了日志里。
//
// 当前消息和同轮补充在尾部单独成块，本来不进历史。可几条消息并发处理、或者某一轮
// 排队晚了的时候，它们早已作为别的轮次的历史写进了日志。这时再把它抽掉，日志中间
// 就少一行；下一轮它不再是当前消息，又按晚到追加回末尾。两轮的前缀都从这一行起
// 作废——2026-09-28 线上同一个群 20:54:17、20:54:19 两条几乎同时到，两轮各自抽掉
// 自己那条，历史在同一个位置先后换成对方。
//
// 所以已经在日志里的原样留下，由尾部注解说明「上面那条就是它」；还没进日志的照旧
// 不进，下一轮按晚到追加到末尾，行首的时间还是它的原始时间。
func (r *Runtime) stableGroupHistoryKeepingTurn(ctx context.Context, event MessageEvent, cfg BotConfig, history []MessageEvent, direct bool, skip map[string]bool) ([]llm.Message, []llm.Message, []MessageEvent) {
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
	entries := make(map[string]GroupPromptHistoryEntry, len(history))
	var kept []MessageEvent
	keepJournaled := func(item MessageEvent) {
		key := messageHistoryDedupeKey(item)
		if key == "" {
			return
		}
		if _, done := entries[key]; done {
			return
		}
		// 不重新渲染：留下的是上一轮发出去的那几个字节，下一轮才能接着命中。
		if entry, ok := old[key]; ok {
			entries[key] = entry
			kept = append(kept, item)
		}
	}
	var localHistory []MessageEvent
	currentInHistory := false
	for _, item := range history {
		if item.MessageID != "" && (item.MessageID == event.MessageID || skip[item.MessageID]) {
			currentInHistory = currentInHistory || item.MessageID == event.MessageID
			keepJournaled(item)
		}
		if !item.crossGroupContext && (event.Kind != EventKindGroup || samePromptGroupScope(event, item)) {
			localHistory = append(localHistory, item)
		}
	}
	// promptContextHistory 已经把当前消息从历史里滤掉了，这里单独补看一次。
	if !currentInHistory && strings.TrimSpace(event.MessageID) != "" {
		before := len(kept)
		keepJournaled(event)
		if len(kept) > before {
			// 留下的当前消息也要参与分轮，否则它没有轮次、优先级落到最低，
			// 预算一紧先被裁掉，前缀照样断。
			localHistory = append(localHistory, event)
			sort.SliceStable(localHistory, func(i, j int) bool { return localHistory[i].Time < localHistory[j].Time })
		}
	}
	groups, recent := historyContextMetadata(localHistory, event.Time, cfg.BotAccount)
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
		}{withoutReplyRuntimeState(item), item.botReply, direct, cfg.BotAccount})
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
	return stable, volatile, kept
}

func (r *Runtime) renderPromptHistoryEvent(ctx context.Context, current, item MessageEvent, cfg BotConfig, direct bool) []llm.Message {
	if strings.TrimSpace(item.botReply) != "" {
		return []llm.Message{{Role: llm.RoleAssistant, Content: item.botReply}}
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
		if text := strings.TrimSpace(historyPlainText(item)); text != "" {
			result = append(result, llm.Message{Role: llm.RoleAssistant, Content: text})
		}
		if !direct || historicalMediaCount(item) == 0 {
			return result
		}
	}
	text := historyPromptTextAt(item, current.Time, cfg)
	if direct && historicalMediaCount(item) > 0 {
		text = agentImageHistoryPromptTextWithDescriptions(item, current.Time, r.historyImageCachedDescriptions(ctx, item), cfg)
	}
	if strings.TrimSpace(text) != "" {
		result = append(result, llm.Message{Role: llm.RoleUser, Content: text, Priority: llm.MessagePriorityHistory})
	}
	return result
}
