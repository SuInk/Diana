// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/google/uuid"
)

// 自述的持久化。一张表，改写和删除都留行：
//
//   - 改写是滚动版本（旧行转 superseded，新行带 supersedes_id），不是原地覆盖。
//     自述跨群生效，主人事后要能看出「这句自我描述是哪天、被谁哄着改成这样的」，
//     原地 UPDATE 会把这条线索抹掉。
//   - 删除是软删除，理由同上。
//
// 时间统一存 UnixNano：同一秒内连改两次是常事，秒级精度会让排序失真。
const selfNoteSchema = `
CREATE TABLE IF NOT EXISTS self_notes (
  id TEXT PRIMARY KEY,
  profile_id TEXT NOT NULL DEFAULT '',
  topic TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  source_session TEXT NOT NULL DEFAULT '',
  source_group_id TEXT NOT NULL DEFAULT '',
  source_message_id TEXT NOT NULL DEFAULT '',
  source_user_id TEXT NOT NULL DEFAULT '',
  source_user_name TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
  supersedes_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('active', 'superseded', 'deleted')),
  editor_user_id TEXT NOT NULL DEFAULT '',
  editor_name TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_self_notes_active ON self_notes(profile_id, status, created_at);
`

const selfNoteColumns = `id, profile_id, topic, content, source_session, source_group_id,
	source_message_id, source_user_id, source_user_name, version, supersedes_id, status,
	editor_user_id, editor_name, created_at, updated_at`

func (s *SQLiteStore) migrateSelfNotes() error {
	if _, err := s.db.Exec(selfNoteSchema); err != nil {
		return fmt.Errorf("create self note schema: %w", err)
	}
	return nil
}

// WriteSelfNote 追加或改写一条自述。
//
// 容量检查和插入必须在同一个事务里：两个群同时聊着、模型各写一条时，先查后插会
// 双双通过检查，在册条数越过上限，而上限存在的理由就是这一层的 token 预算。
func (s *SQLiteStore) WriteSelfNote(ctx context.Context, request assistant.SelfNoteWriteRequest) (assistant.SelfNote, error) {
	defer s.observeStorage(ctx, "WriteSelfNote", "write")()
	request.ProfileID = strings.TrimSpace(request.ProfileID)
	request.Topic = assistant.NormalizeSelfNoteTopic(request.Topic)
	request.Content = assistant.NormalizeSelfNoteContent(request.Content)
	request.SupersedesID = strings.TrimSpace(request.SupersedesID)
	if request.Content == "" {
		return assistant.SelfNote{}, fmt.Errorf("self note content is empty")
	}
	if request.Now.IsZero() {
		request.Now = time.Now()
	}
	nowNS := request.Now.UnixNano()
	tx, err := s.beginWriteTx(ctx, "WriteSelfNote")
	if err != nil {
		return assistant.SelfNote{}, err
	}
	defer observeTransaction("WriteSelfNote")()
	defer func() { _ = tx.Rollback() }()

	version := 1
	if request.SupersedesID != "" {
		var previousVersion int
		err = tx.QueryRowContext(ctx, `
SELECT version FROM self_notes WHERE id = ? AND profile_id = ? AND status = 'active'
`, request.SupersedesID, request.ProfileID).Scan(&previousVersion)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return assistant.SelfNote{}, fmt.Errorf("self note %s is not active", request.SupersedesID)
		case err != nil:
			return assistant.SelfNote{}, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE self_notes SET status = 'superseded', updated_at = ? WHERE id = ? AND status = 'active'
`, nowNS, request.SupersedesID); err != nil {
			return assistant.SelfNote{}, err
		}
		version = previousVersion + 1
	} else {
		var active int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM self_notes WHERE profile_id = ? AND status = 'active'
`, request.ProfileID).Scan(&active); err != nil {
			return assistant.SelfNote{}, err
		}
		if active >= assistant.MaximumActiveSelfNotes {
			return assistant.SelfNote{}, assistant.ErrSelfNoteCapacity
		}
	}

	note := assistant.SelfNote{
		ID:              uuid.NewString(),
		ProfileID:       request.ProfileID,
		Topic:           request.Topic,
		Content:         request.Content,
		SourceSession:   strings.TrimSpace(request.SourceSession),
		SourceGroupID:   strings.TrimSpace(request.SourceGroupID),
		SourceMessageID: strings.TrimSpace(request.SourceMessageID),
		SourceUserID:    strings.TrimSpace(request.SourceUserID),
		SourceUserName:  strings.TrimSpace(request.SourceUserName),
		Version:         version,
		SupersedesID:    request.SupersedesID,
		Status:          assistant.SelfNoteStatusActive,
		CreatedAt:       request.Now,
		UpdatedAt:       request.Now,
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO self_notes (
  id, profile_id, topic, content, source_session, source_group_id, source_message_id,
  source_user_id, source_user_name, version, supersedes_id, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)
`, note.ID, note.ProfileID, note.Topic, note.Content, note.SourceSession, note.SourceGroupID,
		note.SourceMessageID, note.SourceUserID, note.SourceUserName, note.Version, note.SupersedesID,
		nowNS, nowNS); err != nil {
		return assistant.SelfNote{}, err
	}
	if err := tx.Commit(); err != nil {
		return assistant.SelfNote{}, err
	}
	return note, nil
}

// ListSelfNotes 按写入顺序列出条目。注入顺序就是写入顺序：先记下的先出现，模型
// 读起来是一条时间线，而不是每轮随机换序（换序也会打断前缀缓存）。
func (s *SQLiteStore) ListSelfNotes(ctx context.Context, profileID string, includeInactive bool, limit int) ([]assistant.SelfNote, error) {
	defer s.observeStorage(ctx, "ListSelfNotes", "read")()
	if limit <= 0 {
		limit = assistant.MaximumActiveSelfNotes
	}
	query := `SELECT ` + selfNoteColumns + ` FROM self_notes WHERE profile_id = ? AND status = 'active' ORDER BY created_at, id LIMIT ?`
	if includeInactive {
		query = `SELECT ` + selfNoteColumns + ` FROM self_notes WHERE profile_id = ? ORDER BY created_at, id LIMIT ?`
	}
	// 每轮组装提示词都要读，走读池；WriteSelfNote 提交后读池立刻可见。
	rows, err := s.eventReader().QueryContext(ctx, query, strings.TrimSpace(profileID), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	notes := make([]assistant.SelfNote, 0, limit)
	for rows.Next() {
		note, err := scanSelfNote(rows)
		if err != nil {
			return nil, err
		}
		notes = append(notes, note)
	}
	return notes, rows.Err()
}

// DeleteSelfNote 软删除一条。已经删过时返回 found=false，让调用方说「可能已经删过」
// 而不是报错——重复删除是模型常见的重试，不是故障。
func (s *SQLiteStore) DeleteSelfNote(ctx context.Context, profileID, id, editorUserID, editorName string, now time.Time) (assistant.SelfNote, bool, error) {
	defer s.observeStorage(ctx, "DeleteSelfNote", "write")()
	if now.IsZero() {
		now = time.Now()
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE self_notes SET status = 'deleted', editor_user_id = ?, editor_name = ?, updated_at = ?
WHERE id = ? AND profile_id = ? AND status = 'active'
`, strings.TrimSpace(editorUserID), strings.TrimSpace(editorName), now.UnixNano(),
		strings.TrimSpace(id), strings.TrimSpace(profileID))
	if err != nil {
		return assistant.SelfNote{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return assistant.SelfNote{}, false, err
	}
	if affected == 0 {
		return assistant.SelfNote{}, false, nil
	}
	note, err := s.selfNoteByID(ctx, profileID, id)
	if err != nil {
		return assistant.SelfNote{}, false, err
	}
	return note, true, nil
}

// PurgeSelfNotes 软删除该档案下全部在册条目。
func (s *SQLiteStore) PurgeSelfNotes(ctx context.Context, profileID, editorUserID, editorName string, now time.Time) (int, error) {
	defer s.observeStorage(ctx, "PurgeSelfNotes", "write")()
	if now.IsZero() {
		now = time.Now()
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE self_notes SET status = 'deleted', editor_user_id = ?, editor_name = ?, updated_at = ?
WHERE profile_id = ? AND status = 'active'
`, strings.TrimSpace(editorUserID), strings.TrimSpace(editorName), now.UnixNano(), strings.TrimSpace(profileID))
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (s *SQLiteStore) selfNoteByID(ctx context.Context, profileID, id string) (assistant.SelfNote, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selfNoteColumns+` FROM self_notes WHERE id = ? AND profile_id = ?`,
		strings.TrimSpace(id), strings.TrimSpace(profileID))
	return scanSelfNote(row)
}

type selfNoteScanner interface {
	Scan(dest ...any) error
}

func scanSelfNote(row selfNoteScanner) (assistant.SelfNote, error) {
	var note assistant.SelfNote
	var status string
	var createdNS, updatedNS int64
	if err := row.Scan(&note.ID, &note.ProfileID, &note.Topic, &note.Content, &note.SourceSession,
		&note.SourceGroupID, &note.SourceMessageID, &note.SourceUserID, &note.SourceUserName,
		&note.Version, &note.SupersedesID, &status, &note.EditorUserID, &note.EditorName,
		&createdNS, &updatedNS); err != nil {
		return assistant.SelfNote{}, err
	}
	note.Status = assistant.SelfNoteStatus(status)
	note.CreatedAt = time.Unix(0, createdNS)
	note.UpdatedAt = time.Unix(0, updatedNS)
	return note, nil
}
