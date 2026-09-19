package storage

import (
	"context"
	"encoding/json"

	"github.com/SuInk/diana/model/assistant"
)

func (s *SQLiteStore) migrateGroupPromptSessions() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS group_prompt_sessions (
  scope TEXT PRIMARY KEY,
  session TEXT NOT NULL,
  generation INTEGER NOT NULL,
  payload TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS group_prompt_sessions_session ON group_prompt_sessions(session);`)
	return err
}

func (s *SQLiteStore) LoadGroupPromptSession(ctx context.Context, scope, session string) (assistant.GroupPromptSession, error) {
	var state assistant.GroupPromptSession
	var payload string
	// One snapshot reads both generation and payload; a reset cannot fall
	// between two independent reads and make old content look current.
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(c.generation, 0), COALESCE(p.payload, '')
FROM (SELECT ? AS session) AS current
LEFT JOIN session_contexts c ON c.session = current.session
LEFT JOIN group_prompt_sessions p ON p.scope = ? AND p.session = current.session AND p.generation = COALESCE(c.generation, 0)`, session, scope).Scan(&state.Generation, &payload)
	if err != nil {
		return state, err
	}
	generation := state.Generation
	if payload != "" {
		if err = json.Unmarshal([]byte(payload), &state); err != nil {
			return assistant.GroupPromptSession{}, err
		}
	}
	state.Generation = generation
	return state, nil
}

func (s *SQLiteStore) SaveGroupPromptSession(ctx context.Context, scope, session string, state assistant.GroupPromptSession) (bool, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO group_prompt_sessions(scope, session, generation, payload)
SELECT ?, ?, ?, ? WHERE COALESCE((SELECT generation FROM session_contexts WHERE session = ?), 0) = ?
ON CONFLICT(scope) DO UPDATE SET generation = excluded.generation, payload = excluded.payload
WHERE group_prompt_sessions.session = excluded.session`, scope, session, state.Generation, string(payload), session, state.Generation)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

var _ assistant.GroupPromptSessionStore = (*SQLiteStore)(nil)
