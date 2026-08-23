// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/google/uuid"
)

// 词典的持久化。三张表：
//   - glossary_entries 是词条当前状态，(scope_key, normalized_term) 唯一——同一个
//     作用域里一个词只有一条，改释义是修订而不是新增，否则同一个梗会攒出好几份
//     互相矛盾的解释。
//   - glossary_aliases 把别名摊平成行，让「这段话里出现了哪个词条」能直接在 SQL
//     里用 instr 匹配，不用把整本词典读进内存。
//   - glossary_revisions 是修订史，每次新建、更新、作废、恢复都留一行。词典要能
//     被反复修订，就必须能看出上一版是什么、谁改的、为什么改。
//
// 时间统一存 UnixNano：同一秒内连改两次是常事，秒级精度会让排序失真。
const glossarySchema = `
CREATE TABLE IF NOT EXISTS glossary_entries (
  id TEXT PRIMARY KEY,
  scope_key TEXT NOT NULL,
  term TEXT NOT NULL,
  normalized_term TEXT NOT NULL,
  meaning TEXT NOT NULL,
  example TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  aliases TEXT NOT NULL DEFAULT '[]',
  author_user_id TEXT NOT NULL DEFAULT '',
  author_name TEXT NOT NULL DEFAULT '',
  editor_user_id TEXT NOT NULL DEFAULT '',
  editor_name TEXT NOT NULL DEFAULT '',
  source_session TEXT NOT NULL DEFAULT '',
  source_message_id TEXT NOT NULL DEFAULT '',
  usage_count INTEGER NOT NULL DEFAULT 0,
  last_used_at INTEGER,
  version INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL CHECK (status IN ('active', 'deleted')),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS glossary_aliases (
  entry_id TEXT NOT NULL,
  normalized_alias TEXT NOT NULL,
  PRIMARY KEY (entry_id, normalized_alias)
);

CREATE TABLE IF NOT EXISTS glossary_revisions (
  id TEXT PRIMARY KEY,
  entry_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  action TEXT NOT NULL,
  meaning TEXT NOT NULL DEFAULT '',
  example TEXT NOT NULL DEFAULT '',
  aliases TEXT NOT NULL DEFAULT '[]',
  note TEXT NOT NULL DEFAULT '',
  editor_user_id TEXT NOT NULL DEFAULT '',
  editor_name TEXT NOT NULL DEFAULT '',
  recorded_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_glossary_entries_scope_term ON glossary_entries(scope_key, normalized_term);
CREATE INDEX IF NOT EXISTS idx_glossary_entries_scope_status ON glossary_entries(scope_key, status, usage_count DESC, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_glossary_aliases_alias ON glossary_aliases(normalized_alias);
CREATE INDEX IF NOT EXISTS idx_glossary_revisions_entry ON glossary_revisions(entry_id, version DESC);
`

const (
	// glossaryRevisionLimit 是单条词条返回的修订记录条数。修订史用来看「最近怎么
	// 变的」，不是审计日志。
	glossaryRevisionLimit = 10
	// glossaryLookupLimit 是自动命中的兜底上限。
	glossaryLookupLimit = 12
	// glossaryListLimit 是列表查询的兜底上限。
	glossaryListLimit = 50
)

const (
	glossaryActionCreated  = "created"
	glossaryActionUpdated  = "updated"
	glossaryActionDeleted  = "deleted"
	glossaryActionRestored = "restored"
)

const glossaryEntryColumns = `id, scope_key, term, meaning, example, note, aliases,
	author_user_id, author_name, editor_user_id, editor_name,
	usage_count, last_used_at, version, status, created_at, updated_at`

func (s *SQLiteStore) migrateGlossary() error {
	_, err := s.db.Exec(glossarySchema)
	if err != nil {
		return fmt.Errorf("create glossary schema: %w", err)
	}
	return nil
}

// UpsertGlossaryEntry 新建或修订一条词条。已存在（含已作废的）就是修订：版本 +1、
// 状态回到 active，旧内容进修订史。作废过的词又被重新解释时，它该复活而不是被
// 当成新词——修订史是同一条线索。
func (s *SQLiteStore) UpsertGlossaryEntry(ctx context.Context, request assistant.GlossaryUpsertRequest) (assistant.GlossaryEntry, bool, error) {
	if s == nil || s.db == nil {
		return assistant.GlossaryEntry{}, false, errors.New("glossary: store is not configured")
	}
	scope := strings.TrimSpace(request.ScopeKey)
	term := strings.TrimSpace(request.Term)
	normalized := assistant.NormalizeGlossaryTerm(term)
	meaning := strings.TrimSpace(request.Meaning)
	if scope == "" || normalized == "" || meaning == "" {
		return assistant.GlossaryEntry{}, false, errors.New("glossary: scope, term and meaning are required")
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now()
	}
	stamp := now.UTC().UnixNano()
	aliases := assistant.NormalizeGlossaryAliases(term, request.Aliases)
	aliasJSON, err := json.Marshal(aliases)
	if err != nil {
		return assistant.GlossaryEntry{}, false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := glossaryEntryByTerm(ctx, tx, scope, normalized)
	if err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	entryID := existing.ID
	version := 1
	action := glossaryActionCreated
	if found {
		entryID = existing.ID
		version = existing.Version + 1
		action = glossaryActionUpdated
	} else {
		entryID = uuid.NewString()
	}

	if found {
		if _, err := tx.ExecContext(ctx, `
UPDATE glossary_entries
SET term = ?, meaning = ?, example = ?, note = ?, aliases = ?,
    editor_user_id = ?, editor_name = ?, version = ?, status = 'active', updated_at = ?
WHERE id = ?
`, term, meaning, strings.TrimSpace(request.Example), strings.TrimSpace(request.Note), string(aliasJSON),
			strings.TrimSpace(request.EditorUserID), strings.TrimSpace(request.EditorName), version, stamp, entryID); err != nil {
			return assistant.GlossaryEntry{}, false, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO glossary_entries (
  id, scope_key, term, normalized_term, meaning, example, note, aliases,
  author_user_id, author_name, editor_user_id, editor_name,
  source_session, source_message_id, usage_count, version, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 'active', ?, ?)
`, entryID, scope, term, normalized, meaning, strings.TrimSpace(request.Example), strings.TrimSpace(request.Note), string(aliasJSON),
			strings.TrimSpace(request.EditorUserID), strings.TrimSpace(request.EditorName),
			strings.TrimSpace(request.EditorUserID), strings.TrimSpace(request.EditorName),
			strings.TrimSpace(request.SourceSession), strings.TrimSpace(request.SourceMessageID),
			version, stamp, stamp); err != nil {
			return assistant.GlossaryEntry{}, false, err
		}
	}

	if err := replaceGlossaryAliases(ctx, tx, entryID, normalized, aliases); err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	if err := recordGlossaryRevision(ctx, tx, entryID, version, action, meaning,
		strings.TrimSpace(request.Example), string(aliasJSON), strings.TrimSpace(request.Note),
		strings.TrimSpace(request.EditorUserID), strings.TrimSpace(request.EditorName), stamp); err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	entry, _, err := glossaryEntryByTerm(ctx, tx, scope, normalized)
	if err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	return entry, !found, nil
}

// DeleteGlossaryEntry 软删除。修订史留着，restore 能原地救回来。
func (s *SQLiteStore) DeleteGlossaryEntry(ctx context.Context, scopeKey, term, editorUserID, editorName, note string, now time.Time) (assistant.GlossaryEntry, bool, error) {
	return s.setGlossaryStatus(ctx, scopeKey, term, editorUserID, editorName, note, now,
		string(assistant.GlossaryStatusDeleted), glossaryActionDeleted)
}

// RestoreGlossaryEntry 撤销一次软删除。
func (s *SQLiteStore) RestoreGlossaryEntry(ctx context.Context, scopeKey, term, editorUserID, editorName string, now time.Time) (assistant.GlossaryEntry, bool, error) {
	return s.setGlossaryStatus(ctx, scopeKey, term, editorUserID, editorName, "", now,
		string(assistant.GlossaryStatusActive), glossaryActionRestored)
}

func (s *SQLiteStore) setGlossaryStatus(ctx context.Context, scopeKey, term, editorUserID, editorName, note string, now time.Time, status, action string) (assistant.GlossaryEntry, bool, error) {
	if s == nil || s.db == nil {
		return assistant.GlossaryEntry{}, false, errors.New("glossary: store is not configured")
	}
	scope := strings.TrimSpace(scopeKey)
	normalized := assistant.NormalizeGlossaryTerm(term)
	if scope == "" || normalized == "" {
		return assistant.GlossaryEntry{}, false, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	stamp := now.UTC().UnixNano()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := glossaryEntryByTerm(ctx, tx, scope, normalized)
	if err != nil || !found {
		return assistant.GlossaryEntry{}, false, err
	}
	version := existing.Version + 1
	if _, err := tx.ExecContext(ctx, `
UPDATE glossary_entries
SET status = ?, note = ?, editor_user_id = ?, editor_name = ?, version = ?, updated_at = ?
WHERE id = ?
`, status, strings.TrimSpace(note), strings.TrimSpace(editorUserID), strings.TrimSpace(editorName),
		version, stamp, existing.ID); err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	aliasJSON, err := json.Marshal(existing.Aliases)
	if err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	if err := recordGlossaryRevision(ctx, tx, existing.ID, version, action, existing.Meaning,
		existing.Example, string(aliasJSON), strings.TrimSpace(note),
		strings.TrimSpace(editorUserID), strings.TrimSpace(editorName), stamp); err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	entry, _, err := glossaryEntryByTerm(ctx, tx, scope, normalized)
	if err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	return entry, true, nil
}

// LookupGlossaryEntries 回答「这段话里出现了哪些词条」。匹配在 SQL 里用 instr 做：
// 词条和别名都很短，而消息只有一条，反过来把整本词典读进内存才是浪费。
func (s *SQLiteStore) LookupGlossaryEntries(ctx context.Context, query assistant.GlossaryQuery) ([]assistant.GlossaryEntry, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	scopes := glossaryScopeArgs(query.ScopeKeys)
	if len(scopes) == 0 {
		return nil, nil
	}
	text := assistant.NormalizeGlossaryTerm(query.Text)
	terms := make([]string, 0, len(query.Terms))
	for _, term := range query.Terms {
		if normalized := assistant.NormalizeGlossaryTerm(term); normalized != "" {
			terms = append(terms, normalized)
		}
	}
	if text == "" && len(terms) == 0 {
		return nil, nil
	}

	conditions := make([]string, 0, 2)
	args := append([]any{}, scopes...)
	if text != "" {
		conditions = append(conditions,
			`instr(?, e.normalized_term) > 0`,
			`EXISTS (SELECT 1 FROM glossary_aliases a WHERE a.entry_id = e.id AND instr(?, a.normalized_alias) > 0)`)
		args = append(args, text, text)
	}
	if len(terms) > 0 {
		conditions = append(conditions,
			`e.normalized_term IN (`+placeholders(len(terms))+`)`,
			`EXISTS (SELECT 1 FROM glossary_aliases a WHERE a.entry_id = e.id AND a.normalized_alias IN (`+placeholders(len(terms))+`))`)
		for range 2 {
			for _, term := range terms {
				args = append(args, term)
			}
		}
	}

	statement := `SELECT ` + glossaryEntryColumns + ` FROM glossary_entries e
WHERE e.scope_key IN (` + placeholders(len(scopes)) + `) AND e.status = 'active'
  AND (` + strings.Join(conditions, " OR ") + `)
ORDER BY e.usage_count DESC, e.updated_at DESC
LIMIT ?`
	args = append(args, glossaryQueryLimit(query.Limit, glossaryLookupLimit))
	entries, err := queryGlossaryEntries(ctx, s.db, statement, args...)
	if err != nil {
		return nil, err
	}
	assistant.SortGlossaryEntriesByScope(entries, query.ScopeKeys)
	return entries, nil
}

// ListGlossaryEntries 按作用域翻词典，Text 非空时按关键词过滤。
func (s *SQLiteStore) ListGlossaryEntries(ctx context.Context, query assistant.GlossaryQuery) ([]assistant.GlossaryEntry, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	scopes := glossaryScopeArgs(query.ScopeKeys)
	if len(scopes) == 0 {
		return nil, nil
	}
	statement := `SELECT ` + glossaryEntryColumns + ` FROM glossary_entries e
WHERE e.scope_key IN (` + placeholders(len(scopes)) + `)`
	args := append([]any{}, scopes...)
	if !query.IncludeDeleted {
		statement += ` AND e.status = 'active'`
	}
	if keyword := assistant.NormalizeGlossaryTerm(query.Text); keyword != "" {
		statement += ` AND (instr(lower(e.term), ?) > 0 OR instr(lower(e.meaning), ?) > 0
		  OR EXISTS (SELECT 1 FROM glossary_aliases a WHERE a.entry_id = e.id AND instr(a.normalized_alias, ?) > 0))`
		args = append(args, keyword, keyword, keyword)
	}
	statement += ` ORDER BY e.usage_count DESC, e.updated_at DESC LIMIT ?`
	args = append(args, glossaryQueryLimit(query.Limit, glossaryListLimit))
	entries, err := queryGlossaryEntries(ctx, s.db, statement, args...)
	if err != nil {
		return nil, err
	}
	assistant.SortGlossaryEntriesByScope(entries, query.ScopeKeys)
	return entries, nil
}

// GlossaryEntryDetail 返回单条词条及最近的修订记录。
func (s *SQLiteStore) GlossaryEntryDetail(ctx context.Context, scopeKey, term string) (assistant.GlossaryEntry, bool, error) {
	if s == nil || s.db == nil {
		return assistant.GlossaryEntry{}, false, nil
	}
	scope := strings.TrimSpace(scopeKey)
	normalized := assistant.NormalizeGlossaryTerm(term)
	if scope == "" || normalized == "" {
		return assistant.GlossaryEntry{}, false, nil
	}
	entry, found, err := glossaryEntryByTerm(ctx, s.db, scope, normalized)
	if err != nil || !found {
		return assistant.GlossaryEntry{}, false, err
	}
	revisions, err := glossaryRevisions(ctx, s.db, entry.ID)
	if err != nil {
		return assistant.GlossaryEntry{}, false, err
	}
	entry.Revisions = revisions
	return entry, true, nil
}

// TouchGlossaryEntries 记一次命中：用得多的词条排前面，长期没人用的自然沉底。
func (s *SQLiteStore) TouchGlossaryEntries(ctx context.Context, ids []string, at time.Time) error {
	if s == nil || s.db == nil || len(ids) == 0 {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	args := []any{at.UTC().UnixNano()}
	for _, id := range ids {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			args = append(args, trimmed)
		}
	}
	if len(args) == 1 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE glossary_entries
SET usage_count = usage_count + 1, last_used_at = ?
WHERE id IN (`+placeholders(len(args)-1)+`)`, args...)
	return err
}

// glossaryQuerier 让读取逻辑既能跑在事务里也能跑在连接池上。
type glossaryQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func glossaryEntryByTerm(ctx context.Context, db glossaryQuerier, scope, normalized string) (assistant.GlossaryEntry, bool, error) {
	entries, err := queryGlossaryEntries(ctx, db,
		`SELECT `+glossaryEntryColumns+` FROM glossary_entries e WHERE e.scope_key = ? AND e.normalized_term = ? LIMIT 1`,
		scope, normalized)
	if err != nil || len(entries) == 0 {
		return assistant.GlossaryEntry{}, false, err
	}
	return entries[0], true, nil
}

func queryGlossaryEntries(ctx context.Context, db glossaryQuerier, statement string, args ...any) ([]assistant.GlossaryEntry, error) {
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("query glossary entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make([]assistant.GlossaryEntry, 0, 8)
	for rows.Next() {
		var entry assistant.GlossaryEntry
		var aliases string
		var lastUsed sql.NullInt64
		var createdAt, updatedAt int64
		var status string
		if err := rows.Scan(&entry.ID, &entry.ScopeKey, &entry.Term, &entry.Meaning, &entry.Example, &entry.Note,
			&aliases, &entry.AuthorUserID, &entry.AuthorName, &entry.EditorUserID, &entry.EditorName,
			&entry.UsageCount, &lastUsed, &entry.Version, &status, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan glossary entry: %w", err)
		}
		entry.Status = assistant.GlossaryStatus(status)
		entry.Aliases = decodeGlossaryAliases(aliases)
		if lastUsed.Valid {
			entry.LastUsedAt = time.Unix(0, lastUsed.Int64).UTC()
		}
		entry.CreatedAt = time.Unix(0, createdAt).UTC()
		entry.UpdatedAt = time.Unix(0, updatedAt).UTC()
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate glossary entries: %w", err)
	}
	return entries, nil
}

func glossaryRevisions(ctx context.Context, db glossaryQuerier, entryID string) ([]assistant.GlossaryRevision, error) {
	rows, err := db.QueryContext(ctx, `
SELECT version, action, meaning, example, aliases, note, editor_user_id, editor_name, recorded_at
FROM glossary_revisions WHERE entry_id = ? ORDER BY version DESC LIMIT ?`, entryID, glossaryRevisionLimit)
	if err != nil {
		return nil, fmt.Errorf("query glossary revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	revisions := make([]assistant.GlossaryRevision, 0, glossaryRevisionLimit)
	for rows.Next() {
		var revision assistant.GlossaryRevision
		var action, aliases string
		var recordedAt int64
		if err := rows.Scan(&revision.Version, &action, &revision.Meaning, &revision.Example, &aliases,
			&revision.Note, &revision.EditorUserID, &revision.EditorName, &recordedAt); err != nil {
			return nil, fmt.Errorf("scan glossary revision: %w", err)
		}
		revision.Aliases = decodeGlossaryAliases(aliases)
		revision.RecordedAt = time.Unix(0, recordedAt).UTC()
		// action 拼进 note，调用方不必再认一个字段：修订史是给人读的。
		revision.Note = glossaryRevisionNote(action, revision.Note)
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate glossary revisions: %w", err)
	}
	return revisions, nil
}

func glossaryRevisionNote(action, note string) string {
	label := map[string]string{
		glossaryActionCreated:  "新建",
		glossaryActionUpdated:  "更新",
		glossaryActionDeleted:  "作废",
		glossaryActionRestored: "恢复",
	}[action]
	if label == "" {
		label = action
	}
	if strings.TrimSpace(note) == "" {
		return label
	}
	return label + "：" + note
}

func replaceGlossaryAliases(ctx context.Context, tx *sql.Tx, entryID, normalizedTerm string, aliases []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM glossary_aliases WHERE entry_id = ?`, entryID); err != nil {
		return err
	}
	// 词条本身也进别名表，匹配时只查一张表就够。
	values := append([]string{normalizedTerm}, aliases...)
	for _, value := range values {
		normalized := assistant.NormalizeGlossaryTerm(value)
		if normalized == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO glossary_aliases (entry_id, normalized_alias) VALUES (?, ?)`,
			entryID, normalized); err != nil {
			return err
		}
	}
	return nil
}

func recordGlossaryRevision(ctx context.Context, tx *sql.Tx, entryID string, version int, action, meaning, example, aliases, note, editorUserID, editorName string, stamp int64) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO glossary_revisions (
  id, entry_id, version, action, meaning, example, aliases, note, editor_user_id, editor_name, recorded_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), entryID, version, action, meaning, example, aliases, note, editorUserID, editorName, stamp)
	return err
}

func decodeGlossaryAliases(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var aliases []string
	if err := json.Unmarshal([]byte(raw), &aliases); err != nil {
		return nil
	}
	return aliases
}

func glossaryScopeArgs(scopes []string) []any {
	args := make([]any, 0, len(scopes))
	seen := map[string]bool{}
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		args = append(args, scope)
	}
	return args
}

func glossaryQueryLimit(requested, fallback int) int {
	if requested <= 0 || requested > fallback {
		return fallback
	}
	return requested
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}
