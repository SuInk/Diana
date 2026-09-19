package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (s *SQLiteStore) migrateContextHistory() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS session_contexts (
  session TEXT PRIMARY KEY,
  generation INTEGER NOT NULL,
  reset_at INTEGER NOT NULL
)`); err != nil {
		return err
	}
	has, err := s.hasColumn("message_events", "context_generation")
	if err != nil || has {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE message_events ADD COLUMN context_generation INTEGER NOT NULL DEFAULT 0`)
	return err
}

// ResetContextHistory starts a new prompt generation without deleting the chat
// archive. Generations also distinguish messages received in the same second;
// replaying an existing message preserves its original generation on conflict.
func (s *SQLiteStore) ResetContextHistory(ctx context.Context, session string, at time.Time) error {
	session = strings.TrimSpace(session)
	if session == "" {
		return fmt.Errorf("context reset requires a session")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO session_contexts(session, generation, reset_at) VALUES (?, 1, ?)
ON CONFLICT(session) DO UPDATE SET generation = generation + 1, reset_at = excluded.reset_at
`, session, at.Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE thread_states SET status = 'cancelled', updated_at = ?, version = version + 1 WHERE session = ? AND status = 'active'`, at.UnixNano(), session); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_prompt_sessions WHERE session = ?`, session); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memory_items SET status = 'forgotten', updated_at = ? WHERE source_session = ? AND kind = 'thread' AND status = 'active'`, at.Unix(), session); err != nil {
		return err
	}
	return tx.Commit()
}
