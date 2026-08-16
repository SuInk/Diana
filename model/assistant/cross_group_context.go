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
	crossGroupContextLookback       = 72 * time.Hour
	crossGroupContextSearchLimit    = 40
	crossGroupContextResultLimit    = 4
	crossGroupMembershipCheckBudget = 3 * time.Second
)

// Cross-group context only carries related text whose original author is also
// a current member of the target group. This keeps the author inside the
// audience while avoiding source-group identifiers, media, and quote chains.
func (r *Runtime) crossGroupContextEvents(event MessageEvent, store MessageHistoryStore) []MessageEvent {
	cfg := r.effectiveConfigForEvent(event)
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" || strings.TrimSpace(event.UserID) == "" ||
		!boolValue(cfg.CrossGroupMemoryEnabled, false) {
		return nil
	}
	searchStore, ok := store.(MessageHistorySearchStore)
	if !ok {
		return nil
	}
	queryText := crossGroupContextQueryText(event)
	terms := structuredMemorySearchTerms(queryText, 16)
	if !crossGroupQueryHasSignal(terms) {
		return nil
	}
	throughTime := event.Time
	if throughTime <= 0 {
		throughTime = time.Now().Unix()
	}
	loadCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	candidates, _, err := searchStore.SearchMessageEvents(loadCtx, MessageHistorySearchQuery{
		Session:       sessionKey(event),
		SessionPrefix: groupHistorySessionPrefix(event),
		Text:          queryText,
		Terms:         terms,
		FromTime:      throughTime - int64(crossGroupContextLookback/time.Second),
		ThroughTime:   throughTime,
		Limit:         crossGroupContextSearchLimit,
		CrossSession:  true,
	})
	cancel()
	if err != nil {
		return nil
	}

	candidatesByAuthor := make(map[string][]MessageEvent)
	for _, candidate := range candidates {
		groupID := strings.TrimSpace(candidate.GroupID)
		authorID := strings.TrimSpace(candidate.UserID)
		if candidate.Kind != EventKindGroup || groupID == "" || groupID == event.GroupID || candidate.Outbound ||
			authorID == "" ||
			(event.Platform != "" && candidate.Platform != "" && NormalizePlatformID(candidate.Platform) != NormalizePlatformID(event.Platform)) ||
			!crossGroupTopicOverlaps(terms, candidate) {
			continue
		}
		clean, ok := crossGroupTextContext(candidate)
		if !ok {
			continue
		}
		candidatesByAuthor[authorID] = append(candidatesByAuthor[authorID], clean)
	}
	if len(candidatesByAuthor) == 0 {
		return nil
	}
	allowed := r.crossGroupCurrentMembers(event, candidatesByAuthor)
	selected := make([]MessageEvent, 0, crossGroupContextResultLimit)
	for authorID, events := range candidatesByAuthor {
		if allowed[authorID] {
			selected = append(selected, events...)
		}
	}
	sort.SliceStable(selected, func(left, right int) bool { return selected[left].Time > selected[right].Time })
	if len(selected) > crossGroupContextResultLimit {
		selected = selected[:crossGroupContextResultLimit]
	}
	sort.SliceStable(selected, func(left, right int) bool { return selected[left].Time < selected[right].Time })
	return selected
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
	if text == "" || semanticErrorWrapperText(text) {
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
				allowed[authorID] = true
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
