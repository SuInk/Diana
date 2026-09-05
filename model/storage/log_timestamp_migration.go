// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// app_logs 的 created_at 以 RFC3339 文本入库，而仪表盘统计和事件用量查询都是拿字符串
// 做范围比较、边界按 UTC 拼的。normalizeLogEntry 现在会把写入统一归一化到 UTC，但那之前
// 已经带着 +08:00 这类偏移落库的历史行不会自己变好：它们和 UTC 边界做字典序比较时日期和
// 小时位对不上，于是在仪表盘和用量统计里始终查不出来。
//
// 这个迁移把那些行重写成 UTC 形态。时刻本身不变，只换一种写法。
const logTimestampUTCMigrationKey = "storage.app_logs.created_at_utc_migrated"

// migrateLogTimestampsToUTC 重写历史日志里带时区偏移的 created_at。
//
// 用 app_state 里的标记记住做过了，而不是每次启动都扫一遍：日志可以配置为永久保留，
// 而「没有需要修的行」这个结论恰恰只能靠全表扫描得出，代价随表增长。
func (s *SQLiteStore) migrateLogTimestampsToUTC() error {
	if s == nil || s.db == nil {
		return nil
	}
	ctx := context.Background()
	var migrated bool
	if ok, err := s.loadJSON(ctx, logTimestampUTCMigrationKey, &migrated); err != nil {
		return err
	} else if ok && migrated {
		return nil
	}
	if err := s.rewriteNonUTCLogTimestamps(ctx); err != nil {
		return err
	}
	return s.saveJSON(ctx, logTimestampUTCMigrationKey, true)
}

// rewriteNonUTCLogTimestamps 找出所有不是 UTC 形态的 created_at 并就地改写。
func (s *SQLiteStore) rewriteNonUTCLogTimestamps(ctx context.Context) error {
	// 先把要改的行读齐再统一更新：SQLite 上一边遍历结果集一边写同一张表容易撞锁。
	type rewrite struct{ id, createdAt string }
	var pending []rewrite

	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at FROM app_logs WHERE created_at NOT LIKE '%Z'`)
	if err != nil {
		return fmt.Errorf("scan app_logs created_at: %w", err)
	}
	for rows.Next() {
		var id, createdAt string
		if err := rows.Scan(&id, &createdAt); err != nil {
			_ = rows.Close()
			return err
		}
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(createdAt))
		if err != nil {
			// 认不出来的格式原样留着。这个迁移只负责换时区写法，不该顺手去猜一个
			// 本来就存坏了的值——猜错会把一条尚可辨认的日志变成彻底错误的时间。
			continue
		}
		utc := parsed.UTC().Format(time.RFC3339Nano)
		if utc == createdAt {
			continue
		}
		pending = append(pending, rewrite{id: id, createdAt: utc})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `UPDATE app_logs SET created_at = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, item := range pending {
		if _, err := stmt.ExecContext(ctx, item.createdAt, item.id); err != nil {
			return fmt.Errorf("rewrite app_logs created_at: %w", err)
		}
	}
	return tx.Commit()
}
