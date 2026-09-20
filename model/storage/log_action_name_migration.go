// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// 日志动作名现在一律是不带前缀的 snake_case：image_generate、llm_models_list。
//
// 历史行有两种旧写法。一是带过的项目名前缀 assistant.、qqbot.、chatbot.、diana.，
// 前缀不区分任何东西；二是用点分组，而查询和界面都按完整名字精确匹配，从没按点拆过
// 层级。旧名字留在库里，读取方就得把每一代写法都列一遍，漏列一处那一项统计就悄悄
// 归零。这个迁移把历史行统一改成现在的名字，读取方只认一个名字。
const logActionNamesMigrationKey = "storage.app_logs.action_names_flattened"

// 工具改名之后，它的日志动作名也要跟着改，否则同一个工具在库里有两种写法，统计
// 时得把每一代写法都列一遍。改名本身写在这张表里，历史行由下面的迁移补齐。
//
// 和上面那次「去前缀、点换下划线」不同，这里是逐个名字的对照，没法用一条表达式
// 推出来，所以单独记一个完成标记：加新条目时把标记的版本号往后挪一位，历史行会
// 再扫一遍。
const renamedLogActionsMigrationKey = "storage.app_logs.action_names_renamed.v1"

// renamedLogActions 的键是旧动作名，值是现在的名字。键必须写成「去前缀、点换下
// 划线」之后的样子，因为这一步排在 flattenedLogActionExpr 之后。
var renamedLogActions = map[string]string{
	// 工具对外叫 github，Go 类型叫 dianaGitHubTool，日志动作名以前却是
	// repository_issue，同一个工具三种写法。
	"repository_issue": "github",
}

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
	if err := s.saveJSON(ctx, logActionNamesMigrationKey, true); err != nil {
		return err
	}
	return s.saveJSON(ctx, renamedLogActionsMigrationKey, true)
}

func (s *SQLiteStore) logActionNamesMigrated(ctx context.Context) (bool, error) {
	return s.logActionMigrationDone(ctx, logActionNamesMigrationKey)
}

func (s *SQLiteStore) logActionMigrationDone(ctx context.Context, key string) (bool, error) {
	var migrated bool
	ok, err := s.loadJSON(ctx, key, &migrated)
	return ok && migrated, err
}

// forEachLogRowIDWindow 按 rowid 区间往前推，每批一个短事务。动作名上没有索引，
// 全表条件没法走索引，所以按主键分段扫，开销不随进度变大。
func (s *SQLiteStore) forEachLogRowIDWindow(ctx context.Context, apply func(low, high int64) error) error {
	var maxRowID int64
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(rowid), 0) FROM app_logs`).Scan(&maxRowID); err != nil {
		return fmt.Errorf("read app_logs rowid range: %w", err)
	}
	// 迁移开始后写入的行已经是新名字，扫到开始时的最大 rowid 就够了。
	for low := int64(0); low < maxRowID; low += logActionNamesMigrationRowIDWindow {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := apply(low, low+logActionNamesMigrationRowIDWindow); err != nil {
			return err
		}
	}
	return nil
}

// MigrateLogActionNames 分批把历史日志的动作名改成现在的写法，返回是否已全部完成。
// ctx 取消时停在两批之间，已提交的批次保留；改名是幂等的，下次重新扫一遍即可。
//
// 两步有先后：先去前缀、点换下划线，再按 renamedLogActions 逐个改名——后者的键是
// 拍平之后的名字，顺序反过来会漏掉 diana.repository_issue 这种还带前缀的行。
func (s *SQLiteStore) MigrateLogActionNames(ctx context.Context) (bool, error) {
	if s == nil || s.db == nil {
		return true, nil
	}
	if err := s.flattenLogActionNames(ctx); err != nil {
		return false, err
	}
	if err := s.renameLogActionNames(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQLiteStore) flattenLogActionNames(ctx context.Context) error {
	if done, err := s.logActionMigrationDone(ctx, logActionNamesMigrationKey); err != nil || done {
		return err
	}
	if err := s.forEachLogRowIDWindow(ctx, func(low, high int64) error {
		if _, err := s.db.ExecContext(ctx, `UPDATE app_logs SET action = `+flattenedLogActionExpr+`
WHERE rowid > ? AND rowid <= ? AND instr(action, '.') > 0`, low, high); err != nil {
			return fmt.Errorf("flatten app_logs action names: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	return s.saveJSON(ctx, logActionNamesMigrationKey, true)
}

func (s *SQLiteStore) renameLogActionNames(ctx context.Context) error {
	if done, err := s.logActionMigrationDone(ctx, renamedLogActionsMigrationKey); err != nil || done {
		return err
	}
	if len(renamedLogActions) > 0 {
		// 名字按字典序固定下来，批次里的 SQL 才是稳定的一条语句。
		legacy := make([]string, 0, len(renamedLogActions))
		for name := range renamedLogActions {
			legacy = append(legacy, name)
		}
		sort.Strings(legacy)
		statement := `UPDATE app_logs SET action = CASE action`
		arguments := make([]any, 0, len(legacy)*2+2)
		for _, name := range legacy {
			statement += ` WHEN ? THEN ?`
			arguments = append(arguments, name, renamedLogActions[name])
		}
		statement += ` ELSE action END WHERE rowid > ? AND rowid <= ? AND action IN (?` +
			strings.Repeat(`, ?`, len(legacy)-1) + `)`
		if err := s.forEachLogRowIDWindow(ctx, func(low, high int64) error {
			batch := append(append([]any{}, arguments...), low, high)
			for _, name := range legacy {
				batch = append(batch, name)
			}
			if _, err := s.db.ExecContext(ctx, statement, batch...); err != nil {
				return fmt.Errorf("rename app_logs action names: %w", err)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return s.saveJSON(ctx, renamedLogActionsMigrationKey, true)
}
