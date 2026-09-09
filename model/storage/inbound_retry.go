package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SuInk/diana/model/assistant"
)

type inboundRetryRecord struct {
	Session  string                 `json:"session"`
	Event    assistant.MessageEvent `json:"event"`
	Priority int                    `json:"priority"`
}

func (s *SQLiteStore) retryDirectory() string { return s.path + ".inbound-retry" }

func (s *SQLiteStore) SaveInboundRetry(ctx context.Context, session string, event assistant.MessageEvent, priority int) error {
	if s.path == "" {
		return errors.New("retry journal requires a file database")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := stableInboundEventID(session, event)
	if err != nil {
		return err
	}
	body, err := json.Marshal(inboundRetryRecord{Session: session, Event: event, Priority: priority})
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(id))
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	if err := os.MkdirAll(s.retryDirectory(), 0700); err != nil {
		return err
	}
	destination := filepath.Join(s.retryDirectory(), hex.EncodeToString(digest[:])+".json")
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(s.retryDirectory(), ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), destination); err != nil {
		return err
	}
	if dir, err := os.Open(s.retryDirectory()); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (s *SQLiteStore) ReplayInboundRetries(ctx context.Context, limit int) (int, error) {
	if s.path == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(s.retryDirectory())
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if limit <= 0 || limit > 32 {
		limit = 32
	}
	count, attempted := 0, 0
	var invalid []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if attempted >= limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return count, err
		}
		path := filepath.Join(s.retryDirectory(), entry.Name())
		body, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return count, err
		}
		var record inboundRetryRecord
		decodeErr := json.Unmarshal(body, &record)
		if decodeErr == nil {
			_, decodeErr = stableInboundEventID(record.Session, record.Event)
		}
		if decodeErr != nil || strings.TrimSpace(record.Session) == "" {
			invalid = append(invalid, fmt.Errorf("invalid retry file %s", entry.Name()))
			_ = os.Rename(path, path+".invalid")
			continue
		}
		attempted++
		record.Event.RetryRecovered = true
		if _, _, err := s.EnqueueInboundEvent(ctx, record.Session, record.Event, record.Priority); err != nil {
			return count, err
		}
		s.retryMu.Lock()
		err = os.Remove(path)
		s.retryMu.Unlock()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return count, err
		}
		count++
	}
	return count, errors.Join(invalid...)
}
