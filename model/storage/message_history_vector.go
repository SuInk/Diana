// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
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

func decodeMessageVector(buffer []byte) []float32 {
	if len(buffer) < 4 || len(buffer)%4 != 0 {
		return nil
	}
	vector := make([]float32, len(buffer)/4)
	for index := range vector {
		vector[index] = math.Float32frombits(binary.LittleEndian.Uint32(buffer[index*4:]))
	}
	return vector
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

func dotMessageVectors(left, right []float32) float64 {
	if len(left) != len(right) {
		return -1
	}
	var sum float64
	for index := range left {
		sum += float64(left[index]) * float64(right[index])
	}
	return sum
}

// SaveMessageEventVector 记录一条消息的语义向量。消息必须已经落进
// message_events(按 session+message_id 定位),没有 message_id 的事件跳过。
func (s *SQLiteStore) SaveMessageEventVector(ctx context.Context, session string, messageID string, model string, vector []float32) error {
	if s == nil || s.db == nil || !s.historyVectors {
		return nil
	}
	session = strings.TrimSpace(session)
	messageID = strings.TrimSpace(messageID)
	model = strings.TrimSpace(model)
	if session == "" || messageID == "" || model == "" || len(vector) == 0 {
		return nil
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM message_events WHERE session = ? AND message_id = ? ORDER BY created_at DESC LIMIT 1`,
		session, messageID).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO `+messageHistoryVectorTable+` (id, model, vector) VALUES (?, ?, ?)
ON CONFLICT(id) DO UPDATE SET model=excluded.model, vector=excluded.vector`,
		id, model, encodeMessageVector(vector))
	return err
}

// SearchMessageEventsByVector 按余弦相似度检索历史消息。
func (s *SQLiteStore) SearchMessageEventsByVector(ctx context.Context, query assistant.MessageHistoryVectorQuery) ([]assistant.MessageEvent, error) {
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
	where := `v.model = ? AND e.kind != ? AND e.event_time BETWEEN ? AND ?`
	args := []any{query.Model, string(assistant.EventKindNotice), query.FromTime, query.ThroughTime}
	if query.CrossSession {
		where += ` AND e.session LIKE ? ESCAPE '\'`
		args = append(args, escapeMessageHistoryLike(query.SessionPrefix)+"%")
	} else {
		where += ` AND e.session = ?`
		args = append(args, query.Session)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT e.payload, v.vector
FROM `+messageHistoryVectorTable+` AS v
JOIN message_events AS e ON e.id = v.id
WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	queryVector := normalizeMessageVector(query.Vector)
	type scored struct {
		payload string
		score   float64
	}
	hits := make([]scored, 0, 64)
	for rows.Next() {
		var payload string
		var blob []byte
		if err := rows.Scan(&payload, &blob); err != nil {
			return nil, err
		}
		score := dotMessageVectors(queryVector, decodeMessageVector(blob))
		if score <= 0 {
			continue
		}
		hits = append(hits, scored{payload: payload, score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(hits, func(left, right int) bool { return hits[left].score > hits[right].score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	events := make([]assistant.MessageEvent, 0, len(hits))
	for _, hit := range hits {
		var event assistant.MessageEvent
		if err := json.Unmarshal([]byte(hit.payload), &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	return events, nil
}
