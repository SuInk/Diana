// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

const maximumPersistedVoiceBlobBytes = 100 << 20

func (s *SQLiteStore) persistInboundVoiceBlobs(ctx context.Context, event assistant.MessageEvent) (assistant.MessageEvent, error) {
	segments, err := s.persistVoiceSegmentBlobs(ctx, event.Segments)
	if err != nil {
		return event, err
	}
	event.Segments = segments
	if event.Quoted != nil {
		quoted := *event.Quoted
		quoted.Segments, err = s.persistVoiceSegmentBlobs(ctx, quoted.Segments)
		if err != nil {
			return event, err
		}
		event.Quoted = &quoted
	}
	return event, nil
}

func (s *SQLiteStore) persistVoiceSegmentBlobs(ctx context.Context, segments []assistant.MessageSegment) ([]assistant.MessageSegment, error) {
	for index := range segments {
		segment := &segments[index]
		if segment.Type != "record" {
			continue
		}
		sourceKey, source := "", ""
		for _, key := range []string{"file", "url", "path", "sourcePath"} {
			if value := strings.TrimSpace(segment.Data[key]); strings.HasPrefix(value, "base64://") || strings.HasPrefix(value, "data:audio/") {
				sourceKey, source = key, value
				break
			}
		}
		if source == "" {
			continue
		}
		payload := strings.TrimPrefix(source, "base64://")
		if comma := strings.Index(payload, ","); strings.HasPrefix(source, "data:") && comma >= 0 {
			payload = payload[comma+1:]
		}
		body, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
		if err != nil {
			body, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(payload))
		}
		if err != nil || len(body) == 0 {
			return segments, errors.New("persist inbound voice: invalid base64 audio")
		}
		if len(body) > maximumPersistedVoiceBlobBytes {
			return segments, fmt.Errorf("persist inbound voice: audio exceeds %d bytes", maximumPersistedVoiceBlobBytes)
		}
		hash := sha256.Sum256(body)
		digest := hex.EncodeToString(hash[:])
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO voice_blobs (audio_sha256, body, created_at) VALUES (?, ?, ?)`, digest, body, time.Now().Unix()); err != nil {
			return segments, fmt.Errorf("persist inbound voice blob: %w", err)
		}
		data := make(map[string]string, len(segment.Data)+2)
		for key, value := range segment.Data {
			if key != sourceKey {
				data[key] = value
			}
		}
		data["cached_blob_sha256"] = digest
		data["audio_sha256"] = digest
		segment.Data = data
	}
	return segments, nil
}

func (s *SQLiteStore) LoadVoiceBlob(ctx context.Context, audioSHA256 string) ([]byte, bool, error) {
	var body []byte
	err := s.db.QueryRowContext(ctx, `SELECT body FROM voice_blobs WHERE audio_sha256 = ?`, strings.TrimSpace(audioSHA256)).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load voice blob: %w", err)
	}
	return body, true, nil
}

func (s *SQLiteStore) DeleteVoiceBlob(ctx context.Context, audioSHA256 string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM voice_blobs WHERE audio_sha256 = ?`, strings.TrimSpace(audioSHA256)); err != nil {
		return fmt.Errorf("delete voice blob: %w", err)
	}
	return nil
}

func (s *SQLiteStore) LoadVoiceTranscript(ctx context.Context, cacheKey string) (assistant.VoiceTranscriptRecord, bool, error) {
	var record assistant.VoiceTranscriptRecord
	err := s.db.QueryRowContext(ctx, `SELECT cache_key, audio_sha256, backend, model, COALESCE(language,''), transcript, COALESCE(duration_ms,0), created_at FROM voice_transcripts WHERE cache_key = ?`, strings.TrimSpace(cacheKey)).Scan(
		&record.CacheKey, &record.AudioSHA256, &record.Backend, &record.Model, &record.Language, &record.Transcript, &record.DurationMS, &record.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.VoiceTranscriptRecord{}, false, nil
	}
	if err != nil {
		return assistant.VoiceTranscriptRecord{}, false, fmt.Errorf("load voice transcript: %w", err)
	}
	return record, true, nil
}

func (s *SQLiteStore) SaveVoiceTranscript(ctx context.Context, record assistant.VoiceTranscriptRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save voice transcript: %w", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `
INSERT INTO voice_transcripts (cache_key, audio_sha256, backend, model, language, transcript, duration_ms, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(cache_key) DO UPDATE SET transcript=excluded.transcript, duration_ms=excluded.duration_ms, created_at=excluded.created_at
`, record.CacheKey, record.AudioSHA256, record.Backend, record.Model, record.Language, record.Transcript, record.DurationMS, record.CreatedAt); err != nil {
		return fmt.Errorf("save voice transcript: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM voice_blobs WHERE audio_sha256 = ?`, strings.TrimSpace(record.AudioSHA256)); err != nil {
		return fmt.Errorf("delete transcribed voice blob: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("save voice transcript: %w", err)
	}
	return nil
}
