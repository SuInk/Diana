// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"

	"github.com/google/uuid"
)

// 从 storage 重新导出日志类型，旧 WebUI 代码可继续用 storage.AppLogEntry，
// 聊天插件则只依赖 model/applog，不需要知道 SQLite 存储细节。
type AppLogKind = applog.Kind

const (
	LogKindOperation = applog.KindOperation
	LogKindError     = applog.KindError
	LogKindDebug     = applog.KindDebug
)

type AppLogLevel = applog.Level

const (
	LogLevelInfo  = applog.LevelInfo
	LogLevelError = applog.LevelError
)

type AppLogEntry = applog.Entry
type AppLogFilter = applog.Filter

const (
	defaultAppLogLimit = 100
	maxAppLogLimit     = 500
)

// AppendLog 将审计日志追加到 SQLite。
func (s *SQLiteStore) AppendLog(ctx context.Context, entry AppLogEntry) error {
	defer s.observeStorage(ctx, "AppendLog", "write")()
	if s == nil || s.db == nil {
		return nil
	}
	entry = normalizeLogEntry(entry)
	if root := s.debugTraceDir(); root != "" && storesDebugTraceInFile(entry) {
		return s.writeDebugTraceFile(root, entry)
	}
	var metadata string
	if len(entry.Metadata) > 0 {
		// Metadata 保持 map 形态方便各子系统扩展，落库时统一编码成 JSON，避免频繁改表结构。
		data, err := json.Marshal(entry.Metadata)
		if err != nil {
			return err
		}
		metadata = string(data)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO app_logs (id, kind, level, action, message, detail, actor, target, metadata, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, entry.ID, entry.Kind, entry.Level, entry.Action, entry.Message, entry.Detail, entry.Actor, entry.Target, metadata, entry.CreatedAt.Format(time.RFC3339Nano))
	return err
}

// ListLogs 按筛选条件读取最近日志。
func (s *SQLiteStore) ListLogs(ctx context.Context, filter AppLogFilter) ([]AppLogEntry, error) {
	defer s.observeStorage(ctx, "ListLogs", "read")()
	if s == nil || s.db == nil {
		return nil, nil
	}
	limit := normalizeLogLimit(filter.Limit)
	where := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if filter.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, string(filter.Kind))
	}
	if filter.Level != "" {
		where = append(where, "level = ?")
		args = append(args, string(filter.Level))
	}
	if len(filter.Kinds) > 0 {
		placeholders := make([]string, 0, len(filter.Kinds))
		for _, kind := range filter.Kinds {
			placeholders = append(placeholders, "?")
			args = append(args, string(kind))
		}
		where = append(where, "kind IN ("+strings.Join(placeholders, ", ")+")")
	}
	if len(filter.Actions) > 0 {
		placeholders := make([]string, 0, len(filter.Actions))
		for _, action := range filter.Actions {
			placeholders = append(placeholders, "?")
			args = append(args, action)
		}
		where = append(where, "action IN ("+strings.Join(placeholders, ", ")+")")
	}
	if len(filter.ExcludeActions) > 0 {
		placeholders := make([]string, 0, len(filter.ExcludeActions))
		for _, action := range filter.ExcludeActions {
			placeholders = append(placeholders, "?")
			args = append(args, action)
		}
		where = append(where, "action NOT IN ("+strings.Join(placeholders, ", ")+")")
	}
	if !filter.Since.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, filter.Since.UTC().Format(time.RFC3339Nano))
	}
	if !filter.Until.IsZero() {
		where = append(where, "created_at < ?")
		args = append(args, filter.Until.UTC().Format(time.RFC3339Nano))
	}
	if !filter.BeforeTime.IsZero() {
		// 游标和 ORDER BY 用同一套字典序：created_at 入库时统一成 UTC 的 RFC3339Nano，
		// 这里按同样的格式拼回去，同一时刻的多条再按 id 接着往下翻。
		before := filter.BeforeTime.UTC().Format(time.RFC3339Nano)
		where = append(where, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, before, before, filter.BeforeID)
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		like := "%" + escapeLogLike(query) + "%"
		conditions := []string{
			`action LIKE ? ESCAPE '\'`, `message LIKE ? ESCAPE '\'`, `detail LIKE ? ESCAPE '\'`,
			`actor LIKE ? ESCAPE '\'`, `target LIKE ? ESCAPE '\'`, `metadata LIKE ? ESCAPE '\'`,
		}
		for range conditions {
			args = append(args, like)
		}
		for _, actor := range filter.QueryActors {
			if actor = strings.TrimSpace(actor); actor != "" {
				// actor 有「qq:123」和早先直接写账号两种写法，两种都认。
				conditions = append(conditions, "actor = ?", `actor LIKE ? ESCAPE '\'`)
				args = append(args, actor, "%:"+escapeLogLike(actor))
			}
		}
		for _, groupID := range filter.QueryGroupIDs {
			if groupID = strings.TrimSpace(groupID); groupID != "" {
				conditions = append(conditions, "json_extract(NULLIF(metadata, ''), '$.group_id') = ?")
				args = append(args, groupID)
			}
		}
		where = append(where, "("+strings.Join(conditions, " OR ")+")")
	}
	metadataKeys := make([]string, 0, len(filter.Metadata))
	for key := range filter.Metadata {
		metadataKeys = append(metadataKeys, key)
	}
	sort.Strings(metadataKeys)
	for _, key := range metadataKeys {
		if !isPlainLogMetadataKey(key) {
			return nil, fmt.Errorf("unsupported log metadata key %q", key)
		}
		where = append(where, "json_extract(NULLIF(metadata, ''), '$."+key+"') = ?")
		args = append(args, filter.Metadata[key])
	}
	if filter.ProfileID != "" {
		// 机器人 ID 只在 metadata 里：各子系统的 target 含义不同，不能拿它当机器人 ID。
		where = append(where, "json_extract(NULLIF(metadata, ''), '$.profile_id') = ?")
		args = append(args, filter.ProfileID)
	}
	query := `
SELECT id, kind, level, action, message, detail, actor, target, metadata, created_at
FROM app_logs`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	// 最新日志优先展示，并配合 migrate 中的 created_at 索引降低 limit 查询成本。
	query += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.eventReader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	logs := make([]AppLogEntry, 0, limit)
	for rows.Next() {
		entry, err := scanLogEntry(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return logs, nil
}

// isPlainLogMetadataKey 只放行字母、数字和下划线：键要拼进 JSON 路径，不能带引号或点。
func isPlainLogMetadataKey(key string) bool {
	if key == "" {
		return false
	}
	for _, char := range key {
		if !(char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

// escapeLogLike 转义 LIKE 里的通配符，搜「100%」不会变成「100 开头的任意内容」。
func escapeLogLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(value)
}

// normalizeLogEntry 补齐日志条目的默认字段。
func normalizeLogEntry(entry AppLogEntry) AppLogEntry {
	// 写入前补齐默认值，调用方只需要提供 action/message 等关键信息。
	entry.ID = strings.TrimSpace(entry.ID)
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	if entry.Kind == "" {
		entry.Kind = LogKindOperation
	}
	if entry.Level == "" {
		if entry.Kind == LogKindError {
			entry.Level = LogLevelError
		} else {
			entry.Level = LogLevelInfo
		}
	}
	entry.Action = strings.TrimSpace(entry.Action)
	if entry.Action == "" {
		entry.Action = "system.log"
	}
	entry.Message = strings.TrimSpace(entry.Message)
	if entry.Message == "" {
		entry.Message = entry.Action
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	// created_at 以 RFC3339 文本入库，而查询是拿字符串做范围比较的（见
	// dashboardLogStats、listInboundEventTokenUsage），所以必须统一时区：带 +08:00
	// 偏移写进去的那一行，和按 UTC 拼出来的边界做字典序比较时日期和小时位根本对不上，
	// 于是那条日志在仪表盘和用量统计里凭空消失。调用方传本地时间是很自然的写法
	// （webui/system_update.go 就传 time.Now()），所以在这里兜住，而不是指望每个
	// 调用方都记得写 .UTC()。
	entry.CreatedAt = entry.CreatedAt.UTC()
	return entry
}

// normalizeLogLimit 规范化日志查询返回数量。
func normalizeLogLimit(limit int) int {
	// 限制最大返回量，避免日志中心一次性拉太多数据拖慢 WebUI。
	if limit <= 0 {
		return defaultAppLogLimit
	}
	if limit > maxAppLogLimit {
		return maxAppLogLimit
	}
	return limit
}

type logScanner interface {
	Scan(dest ...any) error
}

// scanLogEntry 从数据库扫描结果构造日志条目。
func scanLogEntry(scanner logScanner) (AppLogEntry, error) {
	var entry AppLogEntry
	var kind, level, createdAt string
	var detail, actor, target, metadata sql.NullString
	if err := scanner.Scan(&entry.ID, &kind, &level, &entry.Action, &entry.Message, &detail, &actor, &target, &metadata, &createdAt); err != nil {
		return AppLogEntry{}, err
	}
	entry.Kind = AppLogKind(kind)
	entry.Level = AppLogLevel(level)
	entry.Detail = detail.String
	entry.Actor = actor.String
	entry.Target = target.String
	if metadata.Valid && strings.TrimSpace(metadata.String) != "" {
		// metadata 是可选列，只有非空时才解码，兼容老数据和无扩展信息的日志。
		var parsed map[string]any
		if err := json.Unmarshal([]byte(metadata.String), &parsed); err != nil {
			return AppLogEntry{}, fmt.Errorf("decode log metadata: %w", err)
		}
		entry.Metadata = parsed
	}
	parsedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return AppLogEntry{}, fmt.Errorf("decode log created_at: %w", err)
	}
	entry.CreatedAt = parsedAt
	return entry, nil
}

// FindUserIDsByDisplayName 按昵称子串反查账号，供日志搜索把屏幕上的名字换回号码。
// 只查 user_profiles：群名片散在消息表里，按名字反查要整表扫描。
func (s *SQLiteStore) FindUserIDsByDisplayName(ctx context.Context, query string, limit int) ([]string, error) {
	defer s.observeStorage(ctx, "FindUserIDsByDisplayName", "read")()
	query = strings.TrimSpace(query)
	if s == nil || s.db == nil || query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT DISTINCT user_id FROM user_profiles
WHERE display_name LIKE ? ESCAPE '\' AND TRIM(COALESCE(user_id, '')) <> ''
LIMIT ?`, "%"+escapeLogLike(query)+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("find users by display name: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
