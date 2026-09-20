// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// identityAliasSaltKey 存脱敏别名的全局盐。
//
// 盐决定了「真实账号 → im_user_xxx」这个映射。它一变，所有历史行里的别名跟着变，
// 供应商的前缀缓存整段作废，所以必须跨进程重启保持不变——这正是它要落库的原因。
const identityAliasSaltKey = "identity_alias_salt_v1"

func (s *SQLiteStore) LoadIdentityAliasSalt(ctx context.Context) (string, error) {
	if s == nil || s.db == nil {
		return "", nil
	}
	defer s.observeStorage(ctx, "LoadIdentityAliasSalt", "read")()
	var salt string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key = ?`, identityAliasSaltKey).Scan(&salt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(salt), nil
}

func (s *SQLiteStore) SaveIdentityAliasSalt(ctx context.Context, salt string) error {
	if s == nil || s.db == nil {
		return nil
	}
	salt = strings.TrimSpace(salt)
	if salt == "" {
		return nil
	}
	defer s.observeStorage(ctx, "SaveIdentityAliasSalt", "write")()
	// 只在还没有值时写入：并发启动时先到的那个说了算，后到的读回同一个值，
	// 避免两个进程互相覆盖导致别名反复变化。
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO app_state(key, value) VALUES(?, ?)`, identityAliasSaltKey, salt)
	return err
}
