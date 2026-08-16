// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import "encoding/json"

func webSearchRunMetadataFromInput(tool string, input map[string]any) map[string]any {
	if tool != WebSearchToolName {
		return nil
	}
	candidates, err := webSearchCandidates(input, defaultWebSearchMaxQueries)
	if err != nil {
		return map[string]any{"strategy": "bounded_query_exploration", "input_status": "invalid"}
	}
	queries := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		queries = append(queries, map[string]any{
			"hash":             candidate.Hash,
			"length":           webSearchQueryLength(candidate.Query),
			"language":         candidate.Language,
			"constraint_count": candidate.ConstraintCount,
			"strategy":         candidate.Strategy,
		})
	}
	return map[string]any{
		"strategy": "bounded_query_exploration",
		"queries":  queries,
	}
}

func webSearchRunMetadataFromOutput(tool, output string, runErr error) map[string]any {
	if tool != WebSearchToolName {
		return nil
	}
	if runErr != nil {
		return map[string]any{"status": "provider_error"}
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return map[string]any{"status": "invalid_result"}
	}
	providers := make([]map[string]any, 0, len(result.Providers))
	for _, provider := range result.Providers {
		providers = append(providers, map[string]any{
			"provider": provider.Provider,
			"type":     provider.Type,
			"status":   provider.Status,
			"outcome":  provider.Outcome,
			"reason":   provider.Reason,
			"attempts": provider.Attempts,
		})
	}
	queries := make([]map[string]any, 0, len(result.Queries))
	for _, query := range result.Queries {
		queries = append(queries, map[string]any{
			"hash":             query.Hash,
			"length":           webSearchQueryLength(query.Query),
			"language":         query.Language,
			"constraint_count": query.ConstraintCount,
			"strategy":         query.Strategy,
			"status":           query.Status,
			"outcome":          query.Outcome,
			"reason":           query.Reason,
		})
	}
	attempts := make([]map[string]any, 0, len(result.Attempts))
	for _, attempt := range result.Attempts {
		attempts = append(attempts, map[string]any{
			"provider":     attempt.Provider,
			"query_hash":   attempt.QueryHash,
			"status":       attempt.Status,
			"error_code":   attempt.ErrorCode,
			"result_count": attempt.ResultCount,
			"duration_ms":  attempt.DurationMS,
		})
	}
	return map[string]any{
		"status":       result.Status,
		"stop_reason":  result.StopReason,
		"strategy":     result.Strategy,
		"source_count": len(result.Sources),
		"queries":      queries,
		"providers":    providers,
		"attempts":     attempts,
		"budget":       result.Budget,
	}
}

func mergeRunMetadata(base, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	merged := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}
