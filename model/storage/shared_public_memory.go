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
	args := []any{strings.TrimSpace(query.Session), strings.TrimSpace(query.Session)}
	var clauses []string
	if clause, clauseArgs, ok := crossGroupMemorySessionClause(query); ok {
		clauses = append(clauses, clause)
		args = append(args, clauseArgs...)
	}
	for _, prefix := range query.CrossPlatformGroupPrefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix != "group:" && strings.HasSuffix(prefix, ":group:") {
			clauses = append(clauses, `source_session LIKE ? ESCAPE '\'`)
			args = append(args, escapeMessageHistoryLike(prefix)+"%")
		}
	}
	if len(clauses) == 0 {
		return "0", nil
	}
	return `scope_key != ? AND source_session != ?
		AND visibility = 'session' AND sensitive = 0 AND COALESCE(subject_user_id, '') = ''
		AND COALESCE(source_group_id, '') != '' AND kind IN ('fact', 'summary')
		AND (` + strings.Join(clauses, " OR ") + `)`, args
}

// crossGroupMemorySessionClause 是「其他群的会话记忆从哪些会话取」。没开跨群时 ok=false。
// CrossGroupRestricted 时只取当前发言者核实过也在的那几个群；一个都没有也是 ok=false，
// 其他群的记忆一条不取——不能退回到整个前缀。
func crossGroupMemorySessionClause(query assistant.StructuredMemoryQuery) (string, []any, bool) {
	prefix := strings.TrimSpace(query.GroupSessionPrefix)
	if !query.CrossGroup || prefix == "" {
		return "", nil, false
	}
	if !query.CrossGroupRestricted {
		return `source_session LIKE ? ESCAPE '\'`, []any{escapeMessageHistoryLike(prefix) + "%"}, true
	}
	var sessions []any
	for _, session := range query.CrossGroupSessions {
		if session = strings.TrimSpace(session); strings.HasPrefix(session, prefix) {
			sessions = append(sessions, session)
		}
	}
	if len(sessions) == 0 {
		return "", nil, false
	}
	return `source_session IN (` + placeholders(len(sessions)) + `)`, sessions, true
}
