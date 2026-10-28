// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/google/uuid"
)

var _ assistant.IdentityLinkStore = (*SQLiteStore)(nil)

// 跨平台账号关联：一行一个账号，同一个 person_id 的几行是同一个人。账号由
// (profile_id, platform, user_id) 唯一确定，所以一个账号最多属于一个人。
const identityLinkSchema = `
CREATE TABLE IF NOT EXISTS identity_links (
  profile_id TEXT NOT NULL,
  platform TEXT NOT NULL,
  user_id TEXT NOT NULL,
  person_id TEXT NOT NULL,
  username TEXT NOT NULL DEFAULT '',
  display_name TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  PRIMARY KEY (profile_id, platform, user_id)
);
CREATE INDEX IF NOT EXISTS idx_identity_links_person ON identity_links(person_id);
`

func (s *SQLiteStore) migrateIdentityLinks() error {
	if _, err := s.db.Exec(identityLinkSchema); err != nil {
		return fmt.Errorf("create identity link schema: %w", err)
	}
	return nil
}

const identityLinkColumns = `person_id, profile_id, platform, user_id, username, display_name, source, created_at`

func normalizeIdentityLinkAccount(account assistant.IdentityAccount) assistant.IdentityAccount {
	account.ProfileID = strings.TrimSpace(account.ProfileID)
	account.Platform = assistant.NormalizePlatformID(account.Platform)
	account.UserID = strings.TrimSpace(account.UserID)
	account.Username = strings.TrimPrefix(strings.TrimSpace(account.Username), "@")
	account.DisplayName = strings.TrimSpace(account.DisplayName)
	account.Source = strings.TrimSpace(account.Source)
	return account
}

func validIdentityLinkAccount(account assistant.IdentityAccount) error {
	if account.ProfileID == "" || account.Platform == "" || account.UserID == "" {
		return fmt.Errorf("identity link account needs profile, platform and user id")
	}
	return nil
}

type identityLinkQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func scanIdentityLinks(ctx context.Context, db identityLinkQueryer, where string, args ...any) ([]assistant.IdentityAccount, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+identityLinkColumns+` FROM identity_links `+where+` ORDER BY person_id, created_at, profile_id, user_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []assistant.IdentityAccount
	for rows.Next() {
		var item assistant.IdentityAccount
		var createdAt int64
		if err := rows.Scan(&item.PersonID, &item.ProfileID, &item.Platform, &item.UserID, &item.Username, &item.DisplayName, &item.Source, &createdAt); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(0, createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListIdentityLinks 返回全部关联。
func (s *SQLiteStore) ListIdentityLinks(ctx context.Context) ([]assistant.IdentityAccount, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	return scanIdentityLinks(ctx, s.eventReader(), "")
}

// LinkedIdentityAccounts 返回和这个账号同属一个人的全部账号（含自己）。
func (s *SQLiteStore) LinkedIdentityAccounts(ctx context.Context, account assistant.IdentityAccount) ([]assistant.IdentityAccount, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	account = normalizeIdentityLinkAccount(account)
	return scanIdentityLinks(ctx, s.eventReader(), `WHERE person_id = (SELECT person_id FROM identity_links WHERE profile_id = ? AND platform = ? AND user_id = ?)`,
		account.ProfileID, account.Platform, account.UserID)
}

func identityLinkPerson(ctx context.Context, tx *sql.Tx, account assistant.IdentityAccount) (string, error) {
	var person string
	err := tx.QueryRowContext(ctx, `SELECT person_id FROM identity_links WHERE profile_id = ? AND platform = ? AND user_id = ?`,
		account.ProfileID, account.Platform, account.UserID).Scan(&person)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return person, err
}

// LinkIdentityAccounts 把 b 关联到 a 所在的人。两边都没有归属时新建一个人；一边有
// 就并进那一边；两边分属不同的人时拒绝，不自动合并两整组。
func (s *SQLiteStore) LinkIdentityAccounts(ctx context.Context, a, b assistant.IdentityAccount) ([]assistant.IdentityAccount, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("storage is not configured")
	}
	a, b = normalizeIdentityLinkAccount(a), normalizeIdentityLinkAccount(b)
	for _, account := range []assistant.IdentityAccount{a, b} {
		if err := validIdentityLinkAccount(account); err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	personA, err := identityLinkPerson(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	personB, err := identityLinkPerson(ctx, tx, b)
	if err != nil {
		return nil, err
	}
	person := personA
	switch {
	case personA != "" && personB != "" && personA != personB:
		return nil, assistant.ErrIdentityLinkConflict
	case personA == "" && personB == "":
		person = uuid.NewString()
	case personA == "":
		person = personB
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM identity_links WHERE person_id = ?`, person).Scan(&existing); err != nil {
		return nil, err
	}
	added := 0
	if personA == "" {
		added++
	}
	if personB == "" {
		added++
	}
	if existing+added > assistant.IdentityLinkGroupLimit {
		return nil, assistant.ErrIdentityLinkGroupFull
	}
	now := time.Now().UnixNano()
	for _, account := range []assistant.IdentityAccount{a, b} {
		// 已有归属的账号只刷新展示用的用户名和昵称，不改归属和来源。
		if _, err := tx.ExecContext(ctx, `
INSERT INTO identity_links (person_id, profile_id, platform, user_id, username, display_name, source, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(profile_id, platform, user_id) DO UPDATE SET
  username = CASE WHEN excluded.username != '' THEN excluded.username ELSE identity_links.username END,
  display_name = CASE WHEN excluded.display_name != '' THEN excluded.display_name ELSE identity_links.display_name END
`, person, account.ProfileID, account.Platform, account.UserID, account.Username, account.DisplayName, account.Source, now); err != nil {
			return nil, err
		}
	}
	group, err := scanIdentityLinks(ctx, tx, `WHERE person_id = ?`, person)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return group, nil
}

// UnlinkIdentityAccount 移除一个账号的关联。剩下的人名下只有一个账号时整组删掉：
// 一个账号自成一组没有意义，留着只会让「已关联」的展示误导人。
func (s *SQLiteStore) UnlinkIdentityAccount(ctx context.Context, account assistant.IdentityAccount) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("storage is not configured")
	}
	account = normalizeIdentityLinkAccount(account)
	if err := validIdentityLinkAccount(account); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	person, err := identityLinkPerson(ctx, tx, account)
	if err != nil || person == "" {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM identity_links WHERE profile_id = ? AND platform = ? AND user_id = ?`,
		account.ProfileID, account.Platform, account.UserID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM identity_links WHERE person_id = ? AND (SELECT COUNT(*) FROM identity_links WHERE person_id = ?) < 2
`, person, person); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
