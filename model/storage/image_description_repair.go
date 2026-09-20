// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"

	"github.com/SuInk/diana/model/assistant"
)

// 视觉模型拿不到图时会回一句「未收到图片，无法生成描述」，旧版本把它当成描述按图片
// 内容哈希缓存了下来。缓存命中不会再调模型，所以这些行是永久错误答案：换成能看图的
// 模型也修不好，只能删掉让它重新识别。写入侧的防护见 assistant.VisionDescriptionRefused。
const visionRefusalPurgeKey = "storage.image_descriptions.vision_refusal_purged.v1"

// visionRefusalPurgeDeleteBatch 限制单条 DELETE 的参数个数，避免撞上 SQLite 的变量上限。
const visionRefusalPurgeDeleteBatch = 400

type refusedCacheTable struct {
	name      string
	keyColumn string
	textColum string
}

var refusedCacheTables = []refusedCacheTable{
	{name: "image_descriptions", keyColumn: "content_sha256", textColum: "description"},
	{name: "image_recognitions", keyColumn: "cache_key", textColum: "text"},
}

// PurgeRefusedImageDescriptions 删除被当成描述缓存下来的拒答，返回删除行数。
// 只跑一次：完成标记写进 app_state，之后新写入已经由写入侧挡住了。
//
// 判断条件要和写入侧完全一致，所以文本逐行交给 assistant 里的同一个函数，没有在 SQL
// 里另写一套 LIKE。这两张表都不大（生产上分别是 7.6k 和 3 行），一次扫完即可；只取
// 主键和文本两列，命中的主键才留在内存里。
func (s *SQLiteStore) PurgeRefusedImageDescriptions(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	var purged bool
	if found, err := s.loadJSON(ctx, visionRefusalPurgeKey, &purged); err != nil {
		return 0, err
	} else if found && purged {
		return 0, nil
	}
	total := 0
	for _, table := range refusedCacheTables {
		count, err := s.purgeRefusedCacheTable(ctx, table)
		total += count
		if err != nil {
			return total, err
		}
	}
	if err := s.saveJSON(ctx, visionRefusalPurgeKey, true); err != nil {
		return total, err
	}
	return total, nil
}

func (s *SQLiteStore) purgeRefusedCacheTable(ctx context.Context, table refusedCacheTable) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+table.keyColumn+`, `+table.textColum+` FROM `+table.name)
	if err != nil {
		return 0, fmt.Errorf("scan %s: %w", table.name, err)
	}
	var keys []any
	for rows.Next() {
		var key, text string
		if err := rows.Scan(&key, &text); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan %s: %w", table.name, err)
		}
		if assistant.VisionDescriptionRefused(text) {
			keys = append(keys, key)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("scan %s: %w", table.name, err)
	}
	rows.Close()

	deleted := 0
	for start := 0; start < len(keys); start += visionRefusalPurgeDeleteBatch {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		end := min(start+visionRefusalPurgeDeleteBatch, len(keys))
		batch := keys[start:end]
		placeholders := "?"
		for i := 1; i < len(batch); i++ {
			placeholders += ", ?"
		}
		result, err := s.db.ExecContext(ctx,
			`DELETE FROM `+table.name+` WHERE `+table.keyColumn+` IN (`+placeholders+`)`, batch...)
		if err != nil {
			return deleted, fmt.Errorf("delete refused %s rows: %w", table.name, err)
		}
		if affected, err := result.RowsAffected(); err == nil {
			deleted += int(affected)
		}
	}
	return deleted, nil
}
