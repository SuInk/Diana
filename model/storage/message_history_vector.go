// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

// 语义检索的向量存储。向量在 Go 里暴力算余弦:按会话前缀和时间窗先在 SQL 里
// 收窄候选,再逐条打分取 top-K。这个量级(万级消息)不需要 HNSW 或向量库——
// 暴力扫几十毫秒内就能完成,还省掉索引常驻内存和图退化的维护成本。
//
// 向量存成 float32 小端 BLOB,入库前归一化,余弦相似度退化成点积。
// model 一起存:换 embedding 模型后维度和空间都不同,检索时只比对同一模型
// 产出的向量,旧向量自然失效,不会出现跨模型乱比。

const messageHistoryVectorTable = "message_event_vectors"

func ensureMessageHistoryVectors(db *sql.DB) bool {
	if db == nil {
		return false
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS ` + messageHistoryVectorTable + ` (
  id TEXT PRIMARY KEY,
  model TEXT NOT NULL,
  vector BLOB NOT NULL
)`)
	return err == nil
}

func encodeMessageVector(vector []float32) []byte {
	normalized := normalizeMessageVector(vector)
	buffer := make([]byte, 4*len(normalized))
	for index, value := range normalized {
		binary.LittleEndian.PutUint32(buffer[index*4:], math.Float32bits(value))
	}
	return buffer
}

func normalizeMessageVector(vector []float32) []float32 {
	var sum float64
	for _, value := range vector {
		sum += float64(value) * float64(value)
	}
	if sum <= 0 {
		return vector
	}
	scale := 1 / math.Sqrt(sum)
	normalized := make([]float32, len(vector))
	for index, value := range vector {
		normalized[index] = float32(float64(value) * scale)
	}
	return normalized
}

// dotMessageVectorBytes 直接在 float32 小端 BLOB 上算点积，不先解码成切片：跨会话检索
// 一次要比上万条向量，逐条分配会制造上百 MB 的垃圾。维度对不上返回 -1。
func dotMessageVectorBytes(query []float32, blob []byte) float64 {
	if len(blob) != 4*len(query) {
		return -1
	}
	var sum float64
	for index, value := range query {
		sum += float64(value) * float64(math.Float32frombits(binary.LittleEndian.Uint32(blob[index*4:])))
	}
	return sum
}

// SaveMessageEventVector 记录一条消息的语义向量。消息必须已经落进
// message_events(按 session+message_id 定位),没有 message_id 的事件跳过。
func (s *SQLiteStore) SaveMessageEventVector(ctx context.Context, session string, messageID string, model string, vector []float32) error {
	defer s.observeStorage(ctx, "SaveMessageEventVector", "write")()
	if s == nil || s.db == nil || !s.historyVectors {
		return nil
	}
	session = strings.TrimSpace(session)
	messageID = strings.TrimSpace(messageID)
	model = strings.TrimSpace(model)
	if session == "" || messageID == "" || model == "" || len(vector) == 0 {
		return nil
	}
	// The revision predicate and vector write are one SQLite statement. If an
	// older embedding finishes after a known edit, it cannot recreate the stale
	// vector; if it wins first, the edit transaction's delete runs afterward.
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+messageHistoryVectorTable+` (id, model, vector)
SELECT id, ?, ?
FROM message_events
WHERE session = ? AND message_id = ?
  AND COALESCE(json_extract(payload, '$.edit_refresh_only'), 0) = 0
ORDER BY created_at DESC
LIMIT 1
ON CONFLICT(id) DO UPDATE SET model=excluded.model, vector=excluded.vector`,
		model, encodeMessageVector(vector), session, messageID)
	return err
}

// DeleteMessageEventVector removes every vector attached to the current
// session/message identity. Edits enqueue this on the same worker as vector
// writes, so an older embedding that was already in flight cannot recreate a
// stale vector after the edit has invalidated it.
func (s *SQLiteStore) DeleteMessageEventVector(ctx context.Context, session, messageID string) error {
	defer s.observeStorage(ctx, "DeleteMessageEventVector", "write")()
	if s == nil || s.db == nil || !s.historyVectors {
		return nil
	}
	session = strings.TrimSpace(session)
	messageID = strings.TrimSpace(messageID)
	if session == "" || messageID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM `+messageHistoryVectorTable+`
WHERE id IN (SELECT id FROM message_events WHERE session = ? AND message_id = ?)`, session, messageID)
	return err
}

// SearchMessageEventsByVector 按余弦相似度检索历史消息。
//
// 分两步，payload 只读排在前面的那几条。以前一条 SQL 把时间窗内每行的 payload
// 连同 json_extract 一起读出来再打分：跨会话检索一次要扫上万行、一百多 MB，线上
// 慢日志里 1.5–4 秒、不少直接超时。现在第一步只取 id 和向量打分（向量直接在字节上
// 算点积，不逐条分配），第二步按分数从高到低分批读 payload，在这里再滤掉已知编辑
// 标记的行，凑够 limit 就停。11-26 在 miku 上实测跨会话第一步 0.05 秒（原查询 0.64）。
func (s *SQLiteStore) SearchMessageEventsByVector(ctx context.Context, query assistant.MessageHistoryVectorQuery) ([]assistant.MessageEvent, error) {
	defer s.observeStorage(ctx, "SearchMessageEventsByVector", "read")()
	if s == nil || s.db == nil || !s.historyVectors {
		return nil, nil
	}
	query.Session = strings.TrimSpace(query.Session)
	query.SessionPrefix = strings.TrimSpace(query.SessionPrefix)
	query.Model = strings.TrimSpace(query.Model)
	if query.Model == "" || len(query.Vector) == 0 {
		return nil, nil
	}
	if (!query.CrossSession && query.Session == "") || (query.CrossSession && query.SessionPrefix == "") {
		return nil, nil
	}
	limit := normalizeMessageHistoryLimit(query.Limit)
	hits, err := s.scoreMessageEventVectors(ctx, query)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(hits, func(left, right int) bool { return hits[left].score > hits[right].score })
	return s.loadVectorHitEvents(ctx, hits, limit)
}

type messageVectorHit struct {
	id    string
	score float64
}

// scoreMessageEventVectors 是第一步：按模型、会话、时间窗收窄候选，只读 id 和向量。
func (s *SQLiteStore) scoreMessageEventVectors(ctx context.Context, query assistant.MessageHistoryVectorQuery) ([]messageVectorHit, error) {
	where := `v.model = ?
AND e.kind != ?
AND e.event_time BETWEEN ? AND ?`
	args := []any{query.Model, string(assistant.EventKindNotice), query.FromTime, query.ThroughTime}
	// 单会话走 session+时间索引，让规划器自己选；跨会话没有能用的会话索引，从向量表
	// 出发（CROSS JOIN 固定连接顺序），每个向量按主键找一次消息，不扫整张消息表。
	join := "JOIN"
	if query.CrossSession {
		join = "CROSS JOIN"
		// 前缀用范围比较而不是 LIKE：LIKE 默认不区分大小写，用不上索引，也会把大小写
		// 不同的会话错当成同一前缀。
		where += ` AND e.session >= ?`
		args = append(args, query.SessionPrefix)
		if upper, ok := messageSessionPrefixUpperBound(query.SessionPrefix); ok {
			where += ` AND e.session < ?`
			args = append(args, upper)
		}
	} else {
		where += ` AND e.session = ?`
		args = append(args, query.Session)
	}
	if exclude := strings.TrimSpace(query.ExcludeSession); exclude != "" {
		where += ` AND e.session != ?`
		args = append(args, exclude)
	}
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT v.id, v.vector
FROM `+messageHistoryVectorTable+` AS v
`+join+` message_events AS e ON e.id = v.id
WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	queryVector := normalizeMessageVector(query.Vector)
	hits := make([]messageVectorHit, 0, 64)
	var blob []byte
	for rows.Next() {
		var id string
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		score := dotMessageVectorBytes(queryVector, blob)
		if score <= 0 || score < query.MinSimilarity {
			continue
		}
		hits = append(hits, messageVectorHit{id: id, score: score})
	}
	return hits, rows.Err()
}

// messageVectorPayloadBatch 是第二步每批读几条 payload。已知编辑标记的行很少（11-26
// 线上 50 万条里 1300 条），一批通常就够。
const messageVectorPayloadBatch = 32

// loadVectorHitEvents 是第二步：按分数顺序分批读 payload，滤掉已知编辑标记的行。
// A known edit marks its row before the best-effort physical vector delete.
// Filtering the row here closes the commit/crash and delete-failure windows:
// an old vector can remain on disk temporarily, but it cannot be recalled.
func (s *SQLiteStore) loadVectorHitEvents(ctx context.Context, hits []messageVectorHit, limit int) ([]assistant.MessageEvent, error) {
	events := make([]assistant.MessageEvent, 0, min(limit, len(hits)))
	for start := 0; start < len(hits) && len(events) < limit; start += messageVectorPayloadBatch {
		batch := hits[start:min(start+messageVectorPayloadBatch, len(hits))]
		args := make([]any, len(batch))
		for index, hit := range batch {
			args[index] = hit.id
		}
		rows, err := s.eventReader().QueryContext(ctx, `
SELECT id, payload
FROM message_events
WHERE id IN (?`+strings.Repeat(",?", len(batch)-1)+`)
AND COALESCE(json_extract(payload, '$.edit_refresh_only'), 0) = 0`, args...)
		if err != nil {
			return nil, err
		}
		payloads := make(map[string]string, len(batch))
		for rows.Next() {
			var id, payload string
			if err := rows.Scan(&id, &payload); err != nil {
				_ = rows.Close()
				return nil, err
			}
			payloads[id] = payload
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		for _, hit := range batch {
			payload, ok := payloads[hit.id]
			if !ok {
				continue
			}
			var event assistant.MessageEvent
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				continue
			}
			events = append(events, event)
			if len(events) == limit {
				break
			}
		}
	}
	return events, nil
}

// messageSessionPrefixUpperBound 返回按字节比较时恰好大于所有以 prefix 开头的字符串的
// 最小上界：最后一个不是 0xFF 的字节加一并截断。全是 0xFF 时没有上界。
func messageSessionPrefixUpperBound(prefix string) (string, bool) {
	bound := []byte(prefix)
	for index := len(bound) - 1; index >= 0; index-- {
		if bound[index] < 0xFF {
			bound[index]++
			return string(bound[:index+1]), true
		}
	}
	return "", false
}
