// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
)

// 日志动作名现在一律是不带前缀的 snake_case：image_generate、llm_models_list。
//
// 历史行有两种旧写法。一是带过的项目名前缀 assistant.、qqbot.、chatbot.、diana.，
// 前缀不区分任何东西；二是用点分组，而查询和界面都按完整名字精确匹配，从没按点拆过
// 层级。旧名字留在库里，读取方就得把每一代写法都列一遍，漏列一处那一项统计就悄悄
// 归零。这个迁移把历史行统一改成现在的名字，读取方只认一个名字。
const logActionNamesMigrationKey = "storage.app_logs.action_names_flattened"

// 实测 35 万行的库一次性改完要 38 秒，而更新器等新版本健康检查只给 45 秒：放在启动
// 路径上会被当成启动失败回滚。所以真正改名交给存储维护协程分批做，每批一个短事务，
// 运行中的写入可以穿插进来。「名字里有点」没法走索引，所以按 rowid 区间往前推，
// 每批只扫一段主键，开销不随进度变大。区间大小按写连接的 busy_timeout（5 秒）定。
const logActionNamesMigrationRowIDWindow = 2000

// flattenedLogActionExpr 是把旧名字改成新名字的 SQL 表达式：先去掉项目名前缀，再把点
// 换成下划线。
const flattenedLogActionExpr = `replace(CASE
	WHEN action >= 'assistant.' AND action < 'assistant/' THEN substr(action, 11)
	WHEN action >= 'chatbot.' AND action < 'chatbot/' THEN substr(action, 9)
	WHEN action >= 'diana.' AND action < 'diana/' THEN substr(action, 7)
	WHEN action >= 'qqbot.' AND action < 'qqbot/' THEN substr(action, 7)
	ELSE action
END, '.', '_')`

// markLogActionNamesMigratedIfEmpty 让新库直接记为已迁移。有数据的库要不要改名只能
// 全表扫描才知道，那一步留给维护协程，不放在启动路径上。
func (s *SQLiteStore) markLogActionNamesMigratedIfEmpty() error {
	if s == nil || s.db == nil {
		return nil
	}
	ctx := context.Background()
	if done, err := s.logActionNamesMigrated(ctx); err != nil || done {
		return err
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM app_logs LIMIT 1)`).Scan(&rows); err != nil {
		return fmt.Errorf("check app_logs rows: %w", err)
	}
	if rows > 0 {
		return nil
	}
	return s.saveJSON(ctx, logActionNamesMigrationKey, true)
}

func (s *SQLiteStore) logActionNamesMigrated(ctx context.Context) (bool, error) {
	var migrated bool
	ok, err := s.loadJSON(ctx, logActionNamesMigrationKey, &migrated)
	return ok && migrated, err
}

// MigrateLogActionNames 分批把历史日志的动作名改成现在的写法，返回是否已全部完成。
// ctx 取消时停在两批之间，已提交的批次保留；改名是幂等的，下次重新扫一遍即可。
func (s *SQLiteStore) MigrateLogActionNames(ctx context.Context) (bool, error) {
	if s == nil || s.db == nil {
		return true, nil
	}
	if done, err := s.logActionNamesMigrated(ctx); err != nil || done {
		return done, err
	}
	var maxRowID int64
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(rowid), 0) FROM app_logs`).Scan(&maxRowID); err != nil {
		return false, fmt.Errorf("read app_logs rowid range: %w", err)
	}
	// 迁移开始后写入的行已经是新名字，扫到开始时的最大 rowid 就够了。
	for low := int64(0); low < maxRowID; low += logActionNamesMigrationRowIDWindow {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE app_logs SET action = `+flattenedLogActionExpr+`
WHERE rowid > ? AND rowid <= ? AND instr(action, '.') > 0`,
			low, low+logActionNamesMigrationRowIDWindow); err != nil {
			return false, fmt.Errorf("flatten app_logs action names: %w", err)
		}
	}
	if err := s.saveJSON(ctx, logActionNamesMigrationKey, true); err != nil {
		return false, err
	}
	return true, nil
}
