// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"time"
)

func (r *Runtime) memoryQueryForEvent(event MessageEvent, text string, limit int) StructuredMemoryQuery {
	cfg := r.effectiveConfigForEvent(event)
	return StructuredMemoryQuery{
		SubjectUserID: event.UserID, Session: sessionKey(event), GroupID: event.GroupID,
		Text: text, SearchTerms: structuredMemorySearchTerms(text, 48), Now: time.Now(), MaxCandidates: limit,
		ExcludeKinds:       []MemoryKind{MemoryKindThread},
		CrossGroup:         boolValue(cfg.CrossGroupMemoryEnabled, false) && event.Kind == EventKindGroup,
		GroupSessionPrefix: groupHistorySessionPrefix(event), CrossPlatformGroupPrefixes: r.crossPlatformMemoryPrefixes(event, cfg),
	}
}

// Exact entity/topic links supply one bounded hop, never a recursive graph
// walk. The store re-applies visibility, namespace, status and expiry filters.
func (r *Runtime) expandMemoryAssociations(ctx context.Context, store StructuredMemoryStore, query StructuredMemoryQuery, event MessageEvent, items []StructuredMemoryItem) []StructuredMemoryItem {
	seeds := rankStructuredMemories(items, event, query.Text, query.Now)
	linked := query
	linked.SharedPublicOnly = false
	linked.Text, linked.SearchTerms = "", nil
	linked.MaxCandidates = 24
	linked.Kinds = []MemoryKind{MemoryKindFact, MemoryKindSummary}
	scores := map[string]float64{}
	for _, seed := range seeds {
		if seed.SubjectUserID != "" || seed.Sensitive || seed.SourceGroupID == "" || seed.Visibility != MemoryVisibilitySession || (seed.Kind != MemoryKindFact && seed.Kind != MemoryKindSummary) {
			continue
		}
		// Core preferences and coincidentally important items are not query seeds.
		lexical, _, exact := structuredMemoryLexicalScore(seed, analyzeStructuredMemoryQuery(query.Text), nil, len(items))
		if lexical < 0.08 && exact == "" {
			continue
		}
		label, field := strings.TrimSpace(seed.Entity), "entity"
		if label == "" {
			label, field = strings.TrimSpace(seed.Topic), "topic"
		}
		if len([]rune(label)) < 2 || len([]rune(label)) > 80 {
			continue
		}
		key := field + ":" + strings.ToLower(label)
		if _, ok := scores[key]; ok {
			continue
		}
		scores[key] = seed.RetrievalScore * 0.6
		if field == "entity" {
			linked.RelatedEntities = append(linked.RelatedEntities, label)
		} else {
			linked.RelatedTopics = append(linked.RelatedTopics, label)
		}
		if len(scores) >= 3 {
			break
		}
	}
	if len(scores) == 0 {
		return items
	}
	neighbors, err := store.ListStructuredMemories(ctx, linked)
	if err != nil {
		return items
	}
	linkedItems := make([]StructuredMemoryItem, 0, len(neighbors))
	for _, neighbor := range neighbors {
		item := &neighbor
		if item.SubjectUserID != "" || item.Sensitive || item.SourceGroupID == "" || item.Visibility != MemoryVisibilitySession || (item.Kind != MemoryKindFact && item.Kind != MemoryKindSummary) {
			continue
		}
		for _, field := range []struct{ name, value string }{{"entity", item.Entity}, {"topic", item.Topic}} {
			if score := scores[field.name+":"+strings.ToLower(strings.TrimSpace(field.value))]; score > item.AssociationScore {
				item.AssociationScore, item.AssociationLabel = score, field.value
			}
		}
		if item.AssociationScore > 0 {
			linkedItems = append(linkedItems, *item)
		}
	}
	for index := range items {
		for _, linkedItem := range linkedItems {
			if items[index].ID != "" && items[index].ID == linkedItem.ID {
				items[index].AssociationScore = linkedItem.AssociationScore
				items[index].AssociationLabel = linkedItem.AssociationLabel
			}
		}
	}
	return mergeRetrievedMemoryCandidates(items, linkedItems)
}
