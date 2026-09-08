package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/SuInk/diana/model/assistant"
)

func (s *SQLiteStore) SaveImageModelRecord(ctx context.Context, scope string, record assistant.ImageModelRecord) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO image_model_records(scope,message_id,payload,created_at) VALUES(?,?,?,?) ON CONFLICT(scope,message_id) DO UPDATE SET payload=excluded.payload,created_at=excluded.created_at`, scope, record.MessageID, string(payload), record.CreatedAt)
	return err
}

func (s *SQLiteStore) LoadImageModelRecord(ctx context.Context, scope, messageID string) (assistant.ImageModelRecord, bool, error) {
	var raw string
	var err error
	if messageID == "" {
		err = s.db.QueryRowContext(ctx, `SELECT payload FROM image_model_records WHERE scope=? ORDER BY created_at DESC LIMIT 1`, scope).Scan(&raw)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT payload FROM image_model_records WHERE scope=? AND message_id=?`, scope, messageID).Scan(&raw)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.ImageModelRecord{}, false, nil
	}
	if err != nil {
		return assistant.ImageModelRecord{}, false, err
	}
	var record assistant.ImageModelRecord
	err = json.Unmarshal([]byte(raw), &record)
	return record, err == nil, err
}
