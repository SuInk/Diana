// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	crossGroupContextSearchLimit    = 40
	crossGroupContextResultLimit    = 4
	crossGroupMembershipCheckBudget = 3 * time.Second
)

// Cross-group context only carries related text whose original author is also
// a current member of the target group. This keeps the author inside the
// audience while avoiding source-group identifiers, media, and quote chains.
func (r *Runtime) crossGroupContextEvents(event MessageEvent, store MessageHistoryStore) []MessageEvent {
	// 这条链有六道过滤，任何一道不过都是静默返回 nil，从外面分不清「没命中」
	// 和「功能没生效」。调试模式下把整条漏斗记下来。
	traced := r.crossGroupTraceEnabled(event)
	trace := crossGroupContextTrace{}
	startedAt := time.Now()
	finish := func(selected []MessageEvent, reason string) []MessageEvent {
		if !traced {
			return selected
		}
		trace.SkipReason = reason
		trace.Selected = len(selected)
		trace.DurationMS = time.Since(startedAt).Milliseconds()
		r.recordCrossGroupContextTrace(event, trace)
		return selected
	}

	cfg := r.effectiveConfigForEvent(event)
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" || strings.TrimSpace(event.UserID) == "" {
		return finish(nil, "不是群聊消息或缺少群号、发送者")
	}
	if !boolValue(cfg.CrossGroupMemoryEnabled, false) {
		return finish(nil, "跨群记忆未启用")
	}
	searchStore, ok := store.(MessageHistorySearchStore)
	if !ok {
		return finish(nil, "当前消息存储不支持检索")
	}
	queryText := crossGroupContextQueryText(event)
	terms := structuredMemorySearchTerms(queryText, 16)
	trace.Terms = len(terms)
	if traced {
		sample := terms
		if len(sample) > 5 {
			sample = sample[:5]
		}
		trace.SampleTerms = append([]string(nil), sample...)
	}
	if !crossGroupQueryHasSignal(terms) && !r.semanticSearchActive(cfg) {
		return finish(nil, "查询词信号不足：需要至少一个 3 字词或两个 2 字词")
	}
	throughTime := event.Time
	if throughTime <= 0 {
		throughTime = time.Now().Unix()
	}
	loadCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	candidates, _, err := searchStore.SearchMessageEvents(loadCtx, MessageHistorySearchQuery{
		ExcludeSession: sessionKey(event),
		Session:        sessionKey(event),
		SessionPrefix:  groupHistorySessionPrefix(event),
		Text:           queryText,
		Terms:          terms,
		FromTime:       0,
		ThroughTime:    throughTime,
		Limit:          crossGroupContextSearchLimit,
		CrossSession:   true,
	})
	cancel()
	trace.KeywordCandidates = len(candidates)
	if err != nil {
		trace.KeywordStatus = "文字检索失败或超时"
		candidates = nil
	} else {
		trace.KeywordStatus = "已执行"
	}
	semantic, semanticStatus := r.crossGroupSemanticEvents(event, store, queryText, throughTime)
	trace.SemanticStatus, trace.SemanticCandidates = semanticStatus, len(semantic)
	semanticKeys := make(map[string]bool)
	scores := make(map[string]float64)
	for rank, item := range candidates {
		scores[crossGroupCandidateKey(item)] += 1 / float64(61+rank)
	}
	for rank, item := range semantic {
		key := crossGroupCandidateKey(item)
		semanticKeys[key] = true
		scores[key] += 1 / float64(61+rank)
	}
	candidates = mergeSearchResultsRRF(candidates, semantic, 2*crossGroupContextSearchLimit)
	if err != nil && len(candidates) == 0 {
		return finish(nil, "文字检索失败，语义检索未返回可用结果")
	}
	trace.Candidates = len(candidates)

	candidatesByAuthor := make(map[string][]MessageEvent)
	for _, candidate := range candidates {
		if candidate.Time > throughTime || candidate.Time < 0 {
			trace.DroppedTime++
			continue
		}
		groupID := strings.TrimSpace(candidate.GroupID)
		authorID := strings.TrimSpace(candidate.UserID)
		if candidate.Kind != EventKindGroup || groupID == "" || groupID == event.GroupID || authorID == "" {
			trace.DroppedSameGroup++
			continue
		}
		if candidate.Outbound {
			trace.DroppedOutbound++
			continue
		}
		if event.Platform != "" && candidate.Platform != "" && NormalizePlatformID(candidate.Platform) != NormalizePlatformID(event.Platform) {
			trace.DroppedPlatform++
			continue
		}
		exactQuery := len([]rune(queryText)) >= semanticMinTextRunes && strings.Contains(strings.ToLower(historyPlainText(candidate)), strings.ToLower(queryText))
		if !exactQuery && !crossGroupTopicOverlaps(terms, candidate) && !semanticKeys[crossGroupCandidateKey(candidate)] {
			trace.DroppedTopic++
			continue
		}
		_, ok := crossGroupTextContext(candidate)
		if !ok {
			trace.DroppedText++
			continue
		}
		// Relevance dominates; recency is only a small bonus, not an expiry.
		hits, total := 0, 0
		text := strings.ToLower(historyPlainText(candidate))
		for _, term := range terms {
			weight := len([]rune(term))
			total += weight
			if strings.Contains(text, strings.ToLower(term)) {
				hits += weight
			}
		}
		key := crossGroupCandidateKey(candidate)
		if total > 0 {
			scores[key] += 0.015 * float64(hits) / float64(total)
		}
		ageDays := float64(throughTime-candidate.Time) / (24 * 60 * 60)
		scores[key] += 0.0015 / (1 + ageDays/30)
		candidatesByAuthor[authorID] = append(candidatesByAuthor[authorID], candidate)
	}
	trace.Authors = len(candidatesByAuthor)
	if len(candidatesByAuthor) == 0 {
		return finish(nil, "")
	}
	allowed := r.crossGroupCurrentMembers(event, candidatesByAuthor)
	for _, ok := range allowed {
		if ok {
			trace.AllowedAuthors++
		}
	}
	selected := make([]MessageEvent, 0, crossGroupContextResultLimit)
	for authorID, events := range candidatesByAuthor {
		if allowed[authorID] {
			selected = append(selected, events...)
		} else {
			trace.DroppedMembership += len(events)
		}
	}
	sort.SliceStable(selected, func(left, right int) bool {
		l, rr := scores[crossGroupCandidateKey(selected[left])], scores[crossGroupCandidateKey(selected[right])]
		if l != rr {
			return l > rr
		}
		if selected[left].Time != selected[right].Time {
			return selected[left].Time > selected[right].Time
		}
		return crossGroupCandidateKey(selected[left]) < crossGroupCandidateKey(selected[right])
	})
	if len(selected) > crossGroupContextResultLimit {
		trace.DroppedLimit = len(selected) - crossGroupContextResultLimit
		selected = selected[:crossGroupContextResultLimit]
	}
	sort.SliceStable(selected, func(left, right int) bool { return selected[left].Time < selected[right].Time })
	for index, item := range selected {
		if traced {
			route := "文字"
			if semanticKeys[crossGroupCandidateKey(item)] {
				route = "语义或混合"
			}
			trace.SelectedMessages = append(trace.SelectedMessages, map[string]any{"message_id": item.MessageID, "group_id": item.GroupID, "time": item.Time, "route": route, "score": scores[crossGroupCandidateKey(item)]})
		}
		selected[index], _ = crossGroupTextContext(item)
	}
	return finish(selected, "")
}

func crossGroupContextQueryText(event MessageEvent) string {
	parts := []string{strings.TrimSpace(historyPlainText(event))}
	if event.Quoted != nil {
		parts = append(parts, quotedPlainText(event.Quoted))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func crossGroupQueryHasSignal(terms []string) bool {
	strong, ordinary := 0, 0
	for _, term := range terms {
		length := len([]rune(strings.TrimSpace(term)))
		if length >= 3 {
			strong++
		} else if length >= 2 {
			ordinary++
		}
	}
	return strong > 0 || ordinary >= 2
}

func crossGroupTopicOverlaps(queryTerms []string, candidate MessageEvent) bool {
	document := strings.ToLower(historyPlainText(candidate))
	strong, ordinary := 0, 0
	seen := make(map[string]struct{})
	for _, term := range queryTerms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" || !strings.Contains(document, term) {
			continue
		}
		if _, exists := seen[term]; exists {
			continue
		}
		seen[term] = struct{}{}
		if len([]rune(term)) >= 3 {
			strong++
		} else if len([]rune(term)) >= 2 {
			ordinary++
		}
	}
	return strong > 0 || ordinary >= 2
}

func crossGroupTextContext(event MessageEvent) (MessageEvent, bool) {
	text := strings.TrimSpace(historyPlainText(event))
	if text == "" {
		return MessageEvent{}, false
	}
	event.MessageID = ""
	event.RawMessage = text
	event.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}
	event.Quoted = nil
	event.SemanticSourceMessageID = ""
	event.SemanticSourceMessageIDs = nil
	event.crossGroupContext = true
	return event, true
}

func (r *Runtime) crossGroupCurrentMembers(event MessageEvent, candidatesByAuthor map[string][]MessageEvent) map[string]bool {
	ctx, cancel := context.WithTimeout(context.Background(), crossGroupMembershipCheckBudget)
	defer cancel()
	allowed := make(map[string]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for authorID := range candidatesByAuthor {
		authorID := authorID
		if r.members != nil {
			if _, cached := r.members.lookup(event.GroupID, authorID); cached {
				mu.Lock()
				allowed[authorID] = true
				mu.Unlock()
				continue
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			member, err := r.getGroupMemberInfoForEvent(ctx, event, event.GroupID, authorID)
			if err != nil || strings.TrimSpace(member.UserID) != authorID {
				return
			}
			if r.members != nil {
				r.members.store(event.GroupID, authorID, memberInfo{
					Level: parseGroupLevel(member.Level),
					Role:  strings.TrimSpace(member.Role),
					Title: strings.TrimSpace(member.Title),
					At:    time.Now(),
				})
			}
			mu.Lock()
			allowed[authorID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	return allowed
}

func mergeCrossGroupContextHistory(current, crossGroup []MessageEvent) []MessageEvent {
	if len(crossGroup) == 0 {
		return current
	}
	merged := make([]MessageEvent, 0, len(current)+len(crossGroup))
	merged = append(merged, current...)
	merged = append(merged, crossGroup...)
	sort.SliceStable(merged, func(left, right int) bool {
		if merged[left].Time == merged[right].Time {
			return !merged[left].crossGroupContext && merged[right].crossGroupContext
		}
		return merged[left].Time < merged[right].Time
	})
	return merged
}
