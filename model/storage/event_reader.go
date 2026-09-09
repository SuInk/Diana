package storage

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// Record browsing must not occupy the connection used for durable ingest.
// Each reader connection receives its own read-only pragmas via the driver DSN.
func (s *SQLiteStore) openEventReader() error {
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_app_logs_action_target_time ON app_logs(action, target, created_at);
CREATE INDEX IF NOT EXISTS idx_inbound_events_profile_order ON inbound_events(profile_id, event_time DESC, created_at DESC, id DESC);`); err != nil {
		return err
	}
	if s.path == "" {
		return nil
	} // In-memory and custom DSNs retain their existing semantics.
	uriPath := filepath.ToSlash(s.path)
	if filepath.VolumeName(s.path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(1000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return err
	}
	s.readDB = db
	return nil
}

func (s *SQLiteStore) eventReader() *sql.DB {
	if s.readDB != nil {
		return s.readDB
	}
	return s.db
}
