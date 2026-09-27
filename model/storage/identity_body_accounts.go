// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

var _ assistant.IdentityBodyAccountStore = (*SQLiteStore)(nil)

// 隐私代理核实过「是本群成员」的正文账号。成员结论要跨重启：某一轮把正文里的号换成
// 了别名，这段话就进了历史，重启后查不到结论，历史里的号会变回真号。只存成员，不存
// 「不在群」，所以不需要过期。
const identityBodyAccountSchema = `
CREATE TABLE IF NOT EXISTS identity_body_accounts (
  platform TEXT NOT NULL,
  profile_id TEXT NOT NULL,
  group_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  verified_at INTEGER NOT NULL,
  PRIMARY KEY (platform, profile_id, group_id, user_id)
);
`

func (s *SQLiteStore) migrateIdentityBodyAccounts() error {
	if _, err := s.db.Exec(identityBodyAccountSchema); err != nil {
		return fmt.Errorf("create identity body account schema: %w", err)
	}
	return nil
}

// LoadIdentityBodyAccounts 读一个群里核实过的全部成员账号。组装提示词时读，走读池。
func (s *SQLiteStore) LoadIdentityBodyAccounts(ctx context.Context, platform, profileID, groupID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	defer s.observeStorage(ctx, "LoadIdentityBodyAccounts", "read")()
	rows, err := s.eventReader().QueryContext(ctx,
		`SELECT user_id FROM identity_body_accounts WHERE platform = ? AND profile_id = ? AND group_id = ?`,
		strings.TrimSpace(platform), strings.TrimSpace(profileID), strings.TrimSpace(groupID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		out = append(out, userID)
	}
	return out, rows.Err()
}

// SaveIdentityBodyAccount 记下一个核实过的成员。重复写保留最早那次的时间。
func (s *SQLiteStore) SaveIdentityBodyAccount(ctx context.Context, platform, profileID, groupID, userID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	groupID = strings.TrimSpace(groupID)
	if userID == "" || groupID == "" {
		return nil
	}
	defer s.observeStorage(ctx, "SaveIdentityBodyAccount", "write")()
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO identity_body_accounts(platform, profile_id, group_id, user_id, verified_at) VALUES(?, ?, ?, ?, ?)`,
		strings.TrimSpace(platform), strings.TrimSpace(profileID), groupID, userID, time.Now().Unix())
	return err
}
