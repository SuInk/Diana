// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

func sharedPublicMemoryScope(query assistant.StructuredMemoryQuery) (string, []any) {
	if query.CurrentSessionOnly {
		return "0", nil
	}
	var prefixes []string
	if query.CrossGroup && strings.TrimSpace(query.GroupSessionPrefix) != "" {
		prefixes = append(prefixes, strings.TrimSpace(query.GroupSessionPrefix))
	}
	for _, prefix := range query.CrossPlatformGroupPrefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix != "group:" && strings.HasSuffix(prefix, ":group:") {
			prefixes = append(prefixes, prefix)
		}
	}
	if len(prefixes) == 0 {
		return "0", nil
	}
	args := []any{strings.TrimSpace(query.Session), strings.TrimSpace(query.Session)}
	clauses := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		clauses = append(clauses, `source_session LIKE ? ESCAPE '\'`)
		args = append(args, escapeMessageHistoryLike(prefix)+"%")
	}
	return `scope_key != ? AND source_session != ?
		AND visibility = 'session' AND sensitive = 0 AND COALESCE(subject_user_id, '') = ''
		AND COALESCE(source_group_id, '') != '' AND kind IN ('fact', 'summary')
		AND (` + strings.Join(clauses, " OR ") + `)`, args
}
