// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/google/uuid"
)

// 笔记本的持久化。三张表：
//   - notebook_entries 是条目当前状态，(scope_key, normalized_term) 唯一——同一个
//     作用域里一个词只有一条，改释义是修订而不是新增，否则同一个梗会攒出好几份
//     互相矛盾的解释。
//   - notebook_aliases 把别名摊平成行，让「这段话里出现了哪个条目」能直接在 SQL
//     里用 instr 匹配，不用把整本笔记本读进内存。
//   - notebook_revisions 是修订史，每次新建、更新、作废、恢复都留一行。笔记本要能
//     被反复修订，就必须能看出上一版是什么、谁改的、为什么改。
//
// 时间统一存 UnixNano：同一秒内连改两次是常事，秒级精度会让排序失真。
const notebookSchema = `
CREATE TABLE IF NOT EXISTS notebook_entries (
  id TEXT PRIMARY KEY,
  scope_key TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'term',
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

CREATE TABLE IF NOT EXISTS notebook_aliases (
  entry_id TEXT NOT NULL,
  normalized_alias TEXT NOT NULL,
  PRIMARY KEY (entry_id, normalized_alias)
);

CREATE TABLE IF NOT EXISTS notebook_revisions (
  id TEXT PRIMARY KEY,
  entry_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  action TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'term',
  meaning TEXT NOT NULL DEFAULT '',
  example TEXT NOT NULL DEFAULT '',
  aliases TEXT NOT NULL DEFAULT '[]',
  note TEXT NOT NULL DEFAULT '',
  editor_user_id TEXT NOT NULL DEFAULT '',
  editor_name TEXT NOT NULL DEFAULT '',
  recorded_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_notebook_entries_scope_term ON notebook_entries(scope_key, normalized_term);
CREATE INDEX IF NOT EXISTS idx_notebook_entries_scope_status ON notebook_entries(scope_key, status, usage_count DESC, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_notebook_aliases_alias ON notebook_aliases(normalized_alias);
CREATE INDEX IF NOT EXISTS idx_notebook_revisions_entry ON notebook_revisions(entry_id, version DESC);
`

const (
	// notebookRevisionLimit 是单条条目返回的修订记录条数。修订史用来看「最近怎么
	// 变的」，不是审计日志。
	notebookRevisionLimit = 10
	// notebookLookupLimit 是自动命中的兜底上限。
	notebookLookupLimit = 12
	// notebookListLimit 是列表查询的兜底上限。
	notebookListLimit = 50
)

const (
	notebookActionCreated  = "created"
	notebookActionUpdated  = "updated"
	notebookActionDeleted  = "deleted"
	notebookActionRestored = "restored"
)

const notebookEntryColumns = `id, scope_key, kind, term, meaning, example, note, aliases,
	author_user_id, author_name, editor_user_id, editor_name,
	usage_count, last_used_at, version, status, created_at, updated_at`

func (s *SQLiteStore) migrateNotebook() error {
	// 先把笔记本时代的表改名，再建表：顺序反了会先建出空的 notebook_entries，
	// 然后改名撞上同名表失败，老库里的条目就此变成孤儿数据。
	if err := s.renameLegacyGlossaryTables(); err != nil {
		return err
	}
	if _, err := s.db.Exec(notebookSchema); err != nil {
		return fmt.Errorf("create notebook schema: %w", err)
	}
	return s.addNotebookKindColumn()
}

// renameLegacyGlossaryTables 把 glossary_* 三张表改名成 notebook_*。
//
// 笔记本升级成笔记本是一次改名，不是一次重建：条目、别名、修订史全都要跟过来。
// 单纯换个 CREATE TABLE 名字会让老库的数据留在原地没人读，表现是「升级之后
// 笔记本空了」——而修订史正是这套东西最不能丢的部分。
func (s *SQLiteStore) renameLegacyGlossaryTables() error {
	for _, table := range []struct{ from, to string }{
		{"glossary_entries", "notebook_entries"},
		{"glossary_aliases", "notebook_aliases"},
		{"glossary_revisions", "notebook_revisions"},
	} {
		legacy, err := s.hasTable(table.from)
		if err != nil {
			return err
		}
		if !legacy {
			continue
		}
		current, err := s.hasTable(table.to)
		if err != nil {
			return err
		}
		if current {
			// 两张表同时存在只可能是升级中断在半路。这时不能猜哪张是真的，
			// 保持现状并留痕，让人来决定，比自动合并或自动丢弃安全。
			log.Printf("storage: %s 与 %s 同时存在，跳过改名，请人工确认后处理", table.from, table.to)
			continue
		}
		if _, err := s.db.Exec(`ALTER TABLE ` + table.from + ` RENAME TO ` + table.to); err != nil {
			return fmt.Errorf("rename %s: %w", table.from, err)
		}
	}
	return nil
}

// addNotebookKindColumn 给条目补上类型列。
//
// 老库里每一条都是条目，所以默认值就是 term——升级之后原来的笔记本内容读起来
// 和以前一模一样，只是从此可以再记别的东西。
func (s *SQLiteStore) addNotebookKindColumn() error {
	for _, table := range []string{"notebook_entries", "notebook_revisions"} {
		has, err := s.hasColumn(table, "kind")
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN kind TEXT NOT NULL DEFAULT 'term'`); err != nil {
			return fmt.Errorf("add %s.kind: %w", table, err)
		}
	}
	return nil
}

// hasTable 判断一张表在不在。
func (s *SQLiteStore) hasTable(table string) (bool, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// UpsertNotebookEntry 新建或修订一条条目。已存在（含已作废的）就是修订：版本 +1、
// 状态回到 active，旧内容进修订史。作废过的词又被重新解释时，它该复活而不是被
// 当成新词——修订史是同一条线索。
func (s *SQLiteStore) UpsertNotebookEntry(ctx context.Context, request assistant.NotebookUpsertRequest) (assistant.NotebookEntry, bool, error) {
	if s == nil || s.db == nil {
		return assistant.NotebookEntry{}, false, errors.New("notebook: store is not configured")
	}
	scope := strings.TrimSpace(request.ScopeKey)
	term := strings.TrimSpace(request.Term)
	normalized := assistant.NormalizeNotebookTitle(term)
	meaning := strings.TrimSpace(request.Meaning)
	if scope == "" || normalized == "" || meaning == "" {
		return assistant.NotebookEntry{}, false, errors.New("notebook: scope, term and meaning are required")
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now()
	}
	stamp := now.UTC().UnixNano()
	kind := assistant.NormalizeNotebookKind(string(request.Kind))
	aliases := assistant.NormalizeNotebookAliases(term, request.Aliases)
	aliasJSON, err := json.Marshal(aliases)
	if err != nil {
		return assistant.NotebookEntry{}, false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := notebookEntryByTerm(ctx, tx, scope, normalized)
	if err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	entryID := existing.ID
	version := 1
	action := notebookActionCreated
	if found {
		entryID = existing.ID
		version = existing.Version + 1
		action = notebookActionUpdated
	} else {
		entryID = uuid.NewString()
	}

	if found {
		if _, err := tx.ExecContext(ctx, `
UPDATE notebook_entries
SET kind = ?, term = ?, meaning = ?, example = ?, note = ?, aliases = ?,
    editor_user_id = ?, editor_name = ?, version = ?, status = 'active', updated_at = ?
WHERE id = ?
`, string(kind), term, meaning, strings.TrimSpace(request.Example), strings.TrimSpace(request.Note), string(aliasJSON),
			strings.TrimSpace(request.EditorUserID), strings.TrimSpace(request.EditorName), version, stamp, entryID); err != nil {
			return assistant.NotebookEntry{}, false, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO notebook_entries (
  id, scope_key, kind, term, normalized_term, meaning, example, note, aliases,
  author_user_id, author_name, editor_user_id, editor_name,
  source_session, source_message_id, usage_count, version, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 'active', ?, ?)
`, entryID, scope, string(kind), term, normalized, meaning, strings.TrimSpace(request.Example), strings.TrimSpace(request.Note), string(aliasJSON),
			strings.TrimSpace(request.EditorUserID), strings.TrimSpace(request.EditorName),
			strings.TrimSpace(request.EditorUserID), strings.TrimSpace(request.EditorName),
			strings.TrimSpace(request.SourceSession), strings.TrimSpace(request.SourceMessageID),
			version, stamp, stamp); err != nil {
			return assistant.NotebookEntry{}, false, err
		}
	}

	if err := replaceNotebookAliases(ctx, tx, entryID, normalized, aliases); err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	if err := recordNotebookRevision(ctx, tx, entryID, version, action, kind, meaning,
		strings.TrimSpace(request.Example), string(aliasJSON), strings.TrimSpace(request.Note),
		strings.TrimSpace(request.EditorUserID), strings.TrimSpace(request.EditorName), stamp); err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	entry, _, err := notebookEntryByTerm(ctx, tx, scope, normalized)
	if err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	return entry, !found, nil
}

// DeleteNotebookEntry 软删除。修订史留着，restore 能原地救回来。
func (s *SQLiteStore) DeleteNotebookEntry(ctx context.Context, scopeKey, term, editorUserID, editorName, note string, now time.Time) (assistant.NotebookEntry, bool, error) {
	return s.setNotebookStatus(ctx, scopeKey, term, editorUserID, editorName, note, now,
		string(assistant.NotebookStatusDeleted), notebookActionDeleted)
}

// RestoreNotebookEntry 撤销一次软删除。
func (s *SQLiteStore) RestoreNotebookEntry(ctx context.Context, scopeKey, term, editorUserID, editorName string, now time.Time) (assistant.NotebookEntry, bool, error) {
	return s.setNotebookStatus(ctx, scopeKey, term, editorUserID, editorName, "", now,
		string(assistant.NotebookStatusActive), notebookActionRestored)
}

func (s *SQLiteStore) setNotebookStatus(ctx context.Context, scopeKey, term, editorUserID, editorName, note string, now time.Time, status, action string) (assistant.NotebookEntry, bool, error) {
	if s == nil || s.db == nil {
		return assistant.NotebookEntry{}, false, errors.New("notebook: store is not configured")
	}
	scope := strings.TrimSpace(scopeKey)
	normalized := assistant.NormalizeNotebookTitle(term)
	if scope == "" || normalized == "" {
		return assistant.NotebookEntry{}, false, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	stamp := now.UTC().UnixNano()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := notebookEntryByTerm(ctx, tx, scope, normalized)
	if err != nil || !found {
		return assistant.NotebookEntry{}, false, err
	}
	version := existing.Version + 1
	if _, err := tx.ExecContext(ctx, `
UPDATE notebook_entries
SET status = ?, note = ?, editor_user_id = ?, editor_name = ?, version = ?, updated_at = ?
WHERE id = ?
`, status, strings.TrimSpace(note), strings.TrimSpace(editorUserID), strings.TrimSpace(editorName),
		version, stamp, existing.ID); err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	aliasJSON, err := json.Marshal(existing.Aliases)
	if err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	if err := recordNotebookRevision(ctx, tx, existing.ID, version, action, existing.Kind, existing.Meaning,
		existing.Example, string(aliasJSON), strings.TrimSpace(note),
		strings.TrimSpace(editorUserID), strings.TrimSpace(editorName), stamp); err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	entry, _, err := notebookEntryByTerm(ctx, tx, scope, normalized)
	if err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	return entry, true, nil
}

// LookupNotebookEntries 回答「这段话里出现了哪些条目」。匹配在 SQL 里用 instr 做：
// 条目和别名都很短，而消息只有一条，反过来把整本笔记本读进内存才是浪费。
func (s *SQLiteStore) LookupNotebookEntries(ctx context.Context, query assistant.NotebookQuery) ([]assistant.NotebookEntry, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	scopes := notebookScopeArgs(query.ScopeKeys)
	if len(scopes) == 0 {
		return nil, nil
	}
	text := assistant.NormalizeNotebookTitle(query.Text)
	terms := make([]string, 0, len(query.Terms))
	for _, term := range query.Terms {
		if normalized := assistant.NormalizeNotebookTitle(term); normalized != "" {
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
			`EXISTS (SELECT 1 FROM notebook_aliases a WHERE a.entry_id = e.id AND instr(?, a.normalized_alias) > 0)`)
		args = append(args, text, text)
	}
	if len(terms) > 0 {
		conditions = append(conditions,
			`e.normalized_term IN (`+placeholders(len(terms))+`)`,
			`EXISTS (SELECT 1 FROM notebook_aliases a WHERE a.entry_id = e.id AND a.normalized_alias IN (`+placeholders(len(terms))+`))`)
		for range 2 {
			for _, term := range terms {
				args = append(args, term)
			}
		}
	}

	statement := `SELECT ` + notebookEntryColumns + ` FROM notebook_entries e
WHERE e.scope_key IN (` + placeholders(len(scopes)) + `) AND e.status = 'active'
  AND (` + strings.Join(conditions, " OR ") + `)
ORDER BY e.usage_count DESC, e.updated_at DESC
LIMIT ?`
	args = append(args, notebookQueryLimit(query.Limit, notebookLookupLimit))
	entries, err := queryNotebookEntries(ctx, s.db, statement, args...)
	if err != nil {
		return nil, err
	}
	assistant.SortNotebookEntriesByScope(entries, query.ScopeKeys)
	return entries, nil
}

// ListNotebookEntries 按作用域翻笔记本，Text 非空时按关键词过滤。
func (s *SQLiteStore) ListNotebookEntries(ctx context.Context, query assistant.NotebookQuery) ([]assistant.NotebookEntry, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	scopes := notebookScopeArgs(query.ScopeKeys)
	if len(scopes) == 0 {
		return nil, nil
	}
	statement := `SELECT ` + notebookEntryColumns + ` FROM notebook_entries e
WHERE e.scope_key IN (` + placeholders(len(scopes)) + `)`
	args := append([]any{}, scopes...)
	if !query.IncludeDeleted {
		statement += ` AND e.status = 'active'`
	}
	if keyword := assistant.NormalizeNotebookTitle(query.Text); keyword != "" {
		statement += ` AND (instr(lower(e.term), ?) > 0 OR instr(lower(e.meaning), ?) > 0
		  OR EXISTS (SELECT 1 FROM notebook_aliases a WHERE a.entry_id = e.id AND instr(a.normalized_alias, ?) > 0))`
		args = append(args, keyword, keyword, keyword)
	}
	// 类型筛选只用于人工翻阅。自动命中从不带它——一条待办和一个梗同样可能是
	// 这句话需要的上下文，按类型预先筛掉等于替模型决定它该想起什么。
	if kinds := notebookKindArgs(query.Kinds); len(kinds) > 0 {
		statement += ` AND e.kind IN (` + placeholders(len(kinds)) + `)`
		args = append(args, kinds...)
	}
	statement += ` ORDER BY e.usage_count DESC, e.updated_at DESC LIMIT ?`
	args = append(args, notebookQueryLimit(query.Limit, notebookListLimit))
	entries, err := queryNotebookEntries(ctx, s.db, statement, args...)
	if err != nil {
		return nil, err
	}
	assistant.SortNotebookEntriesByScope(entries, query.ScopeKeys)
	return entries, nil
}

// NotebookEntryDetail 返回单条条目及最近的修订记录。
func (s *SQLiteStore) NotebookEntryDetail(ctx context.Context, scopeKey, term string) (assistant.NotebookEntry, bool, error) {
	if s == nil || s.db == nil {
		return assistant.NotebookEntry{}, false, nil
	}
	scope := strings.TrimSpace(scopeKey)
	normalized := assistant.NormalizeNotebookTitle(term)
	if scope == "" || normalized == "" {
		return assistant.NotebookEntry{}, false, nil
	}
	entry, found, err := notebookEntryByTerm(ctx, s.db, scope, normalized)
	if err != nil || !found {
		return assistant.NotebookEntry{}, false, err
	}
	revisions, err := notebookRevisions(ctx, s.db, entry.ID)
	if err != nil {
		return assistant.NotebookEntry{}, false, err
	}
	entry.Revisions = revisions
	return entry, true, nil
}

// TouchNotebookEntries 记一次命中：用得多的条目排前面，长期没人用的自然沉底。
func (s *SQLiteStore) TouchNotebookEntries(ctx context.Context, ids []string, at time.Time) error {
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
UPDATE notebook_entries
SET usage_count = usage_count + 1, last_used_at = ?
WHERE id IN (`+placeholders(len(args)-1)+`)`, args...)
	return err
}

// notebookQuerier 让读取逻辑既能跑在事务里也能跑在连接池上。
type notebookQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func notebookEntryByTerm(ctx context.Context, db notebookQuerier, scope, normalized string) (assistant.NotebookEntry, bool, error) {
	entries, err := queryNotebookEntries(ctx, db,
		`SELECT `+notebookEntryColumns+` FROM notebook_entries e WHERE e.scope_key = ? AND e.normalized_term = ? LIMIT 1`,
		scope, normalized)
	if err != nil || len(entries) == 0 {
		return assistant.NotebookEntry{}, false, err
	}
	return entries[0], true, nil
}

func queryNotebookEntries(ctx context.Context, db notebookQuerier, statement string, args ...any) ([]assistant.NotebookEntry, error) {
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("query notebook entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make([]assistant.NotebookEntry, 0, 8)
	for rows.Next() {
		var entry assistant.NotebookEntry
		var aliases string
		var lastUsed sql.NullInt64
		var createdAt, updatedAt int64
		var status, kind string
		if err := rows.Scan(&entry.ID, &entry.ScopeKey, &kind, &entry.Term, &entry.Meaning, &entry.Example, &entry.Note,
			&aliases, &entry.AuthorUserID, &entry.AuthorName, &entry.EditorUserID, &entry.EditorName,
			&entry.UsageCount, &lastUsed, &entry.Version, &status, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan notebook entry: %w", err)
		}
		entry.Status = assistant.NotebookStatus(status)
		entry.Kind = assistant.NormalizeNotebookKind(kind)
		entry.Aliases = decodeNotebookAliases(aliases)
		if lastUsed.Valid {
			entry.LastUsedAt = time.Unix(0, lastUsed.Int64).UTC()
		}
		entry.CreatedAt = time.Unix(0, createdAt).UTC()
		entry.UpdatedAt = time.Unix(0, updatedAt).UTC()
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notebook entries: %w", err)
	}
	return entries, nil
}

func notebookRevisions(ctx context.Context, db notebookQuerier, entryID string) ([]assistant.NotebookRevision, error) {
	rows, err := db.QueryContext(ctx, `
SELECT version, action, kind, meaning, example, aliases, note, editor_user_id, editor_name, recorded_at
FROM notebook_revisions WHERE entry_id = ? ORDER BY version DESC LIMIT ?`, entryID, notebookRevisionLimit)
	if err != nil {
		return nil, fmt.Errorf("query notebook revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	revisions := make([]assistant.NotebookRevision, 0, notebookRevisionLimit)
	for rows.Next() {
		var revision assistant.NotebookRevision
		var action, aliases, kind string
		var recordedAt int64
		if err := rows.Scan(&revision.Version, &action, &kind, &revision.Meaning, &revision.Example, &aliases,
			&revision.Note, &revision.EditorUserID, &revision.EditorName, &recordedAt); err != nil {
			return nil, fmt.Errorf("scan notebook revision: %w", err)
		}
		revision.Kind = assistant.NormalizeNotebookKind(kind)
		revision.Aliases = decodeNotebookAliases(aliases)
		revision.RecordedAt = time.Unix(0, recordedAt).UTC()
		// action 拼进 note，调用方不必再认一个字段：修订史是给人读的。
		revision.Note = notebookRevisionNote(action, revision.Note)
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notebook revisions: %w", err)
	}
	return revisions, nil
}

func notebookRevisionNote(action, note string) string {
	label := map[string]string{
		notebookActionCreated:  "新建",
		notebookActionUpdated:  "更新",
		notebookActionDeleted:  "作废",
		notebookActionRestored: "恢复",
	}[action]
	if label == "" {
		label = action
	}
	if strings.TrimSpace(note) == "" {
		return label
	}
	return label + "：" + note
}

func replaceNotebookAliases(ctx context.Context, tx *sql.Tx, entryID, normalizedTerm string, aliases []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM notebook_aliases WHERE entry_id = ?`, entryID); err != nil {
		return err
	}
	// 条目本身也进别名表，匹配时只查一张表就够。
	values := append([]string{normalizedTerm}, aliases...)
	for _, value := range values {
		normalized := assistant.NormalizeNotebookTitle(value)
		if normalized == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO notebook_aliases (entry_id, normalized_alias) VALUES (?, ?)`,
			entryID, normalized); err != nil {
			return err
		}
	}
	return nil
}

func recordNotebookRevision(ctx context.Context, tx *sql.Tx, entryID string, version int, action string, kind assistant.NotebookKind, meaning, example, aliases, note, editorUserID, editorName string, stamp int64) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO notebook_revisions (
  id, entry_id, version, action, kind, meaning, example, aliases, note, editor_user_id, editor_name, recorded_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), entryID, version, action, string(kind), meaning, example, aliases, note, editorUserID, editorName, stamp)
	return err
}

func decodeNotebookAliases(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var aliases []string
	if err := json.Unmarshal([]byte(raw), &aliases); err != nil {
		return nil
	}
	return aliases
}

func notebookScopeArgs(scopes []string) []any {
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

func notebookQueryLimit(requested, fallback int) int {
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

// NotebookScopeSummary 是一个作用域下的条目统计，供控制台列出「有哪些本笔记本」。
type NotebookScopeSummary struct {
	ScopeKey     string    `json:"scope_key"`
	ActiveCount  int       `json:"active_count"`
	DeletedCount int       `json:"deleted_count"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ListNotebookScopes 列出所有存在条目的作用域。控制台要先知道有哪些群立过笔记本，
// 才谈得上翻它——按作用域名字猜是猜不出来的。
func (s *SQLiteStore) ListNotebookScopes(ctx context.Context) ([]NotebookScopeSummary, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT scope_key,
       SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END),
       SUM(CASE WHEN status = 'deleted' THEN 1 ELSE 0 END),
       MAX(updated_at)
FROM notebook_entries
GROUP BY scope_key
ORDER BY MAX(updated_at) DESC`)
	if err != nil {
		return nil, fmt.Errorf("query notebook scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	summaries := make([]NotebookScopeSummary, 0, 8)
	for rows.Next() {
		var summary NotebookScopeSummary
		var updatedAt int64
		if err := rows.Scan(&summary.ScopeKey, &summary.ActiveCount, &summary.DeletedCount, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan notebook scope: %w", err)
		}
		summary.UpdatedAt = time.Unix(0, updatedAt).UTC()
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notebook scopes: %w", err)
	}
	return summaries, nil
}

// notebookKindArgs 把类型筛选规整成查询参数，未知类型直接丢掉。
func notebookKindArgs(kinds []assistant.NotebookKind) []any {
	out := make([]any, 0, len(kinds))
	seen := map[assistant.NotebookKind]bool{}
	for _, kind := range kinds {
		if !kind.Valid() || seen[kind] {
			continue
		}
		seen[kind] = true
		out = append(out, string(kind))
	}
	return out
}
