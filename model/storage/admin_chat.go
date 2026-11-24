package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// 管理对话（WebUI「管理对话」和桌面端的 Agent 会话）落库：重启服务、重新登录
// 之后历史还在，桌面端每次启动现登也接得上。会话归 WebUI 管理员所有——只有
// 一个管理员账号，所以不再按登录分。

// AdminChatSession 是一条会话的元数据；Messages 只在 GetAdminChatSession 里填。
type AdminChatSession struct {
	ID          string
	ProfileID   string
	Title       string
	LoadedTools string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// 列表用：消息条数和最后一条的正文。
	MessageCount int
	LastContent  string
}

// AdminChatMessage 是会话里的一条消息。Steps 是执行步骤的 JSON，由 webui 层编解码。
type AdminChatMessage struct {
	ID        string
	Role      string
	Content   string
	Error     bool
	Steps     string
	CreatedAt time.Time
}

// ErrAdminChatSessionNotFound 表示会话不存在（或已删除）。
var ErrAdminChatSessionNotFound = errors.New("admin chat session not found")

func (s *SQLiteStore) migrateAdminChat() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS admin_chat_sessions (
  id TEXT PRIMARY KEY,
  profile_id TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  loaded_tools TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS admin_chat_sessions_recent ON admin_chat_sessions(profile_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS admin_chat_messages (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  error INTEGER NOT NULL DEFAULT 0,
  steps TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS admin_chat_messages_session ON admin_chat_messages(session_id, seq);
`)
	return err
}

func (s *SQLiteStore) CreateAdminChatSession(ctx context.Context, id, profileID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_chat_sessions(id, profile_id, created_at, updated_at) VALUES (?, ?, ?, ?)`, id, profileID, at.UnixNano(), at.UnixNano())
	return err
}

const adminChatSessionColumns = `s.id, s.profile_id, s.title, s.loaded_tools, s.created_at, s.updated_at,
  (SELECT COUNT(*) FROM admin_chat_messages m WHERE m.session_id = s.id),
  COALESCE((SELECT m.content FROM admin_chat_messages m WHERE m.session_id = s.id ORDER BY m.seq DESC LIMIT 1), '')`

func scanAdminChatSession(row interface{ Scan(...any) error }) (AdminChatSession, error) {
	var out AdminChatSession
	var created, updated int64
	err := row.Scan(&out.ID, &out.ProfileID, &out.Title, &out.LoadedTools, &created, &updated, &out.MessageCount, &out.LastContent)
	out.CreatedAt, out.UpdatedAt = time.Unix(0, created), time.Unix(0, updated)
	return out, err
}

// ListAdminChatSessions 按最近更新倒序列出某个机器人的全部会话。
func (s *SQLiteStore) ListAdminChatSessions(ctx context.Context, profileID string) ([]AdminChatSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+adminChatSessionColumns+` FROM admin_chat_sessions s WHERE s.profile_id = ? ORDER BY s.updated_at DESC, s.id`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminChatSession
	for rows.Next() {
		session, err := scanAdminChatSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetAdminChatSession(ctx context.Context, id string) (AdminChatSession, error) {
	session, err := scanAdminChatSession(s.db.QueryRowContext(ctx, `SELECT `+adminChatSessionColumns+` FROM admin_chat_sessions s WHERE s.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return session, ErrAdminChatSessionNotFound
	}
	return session, err
}

// AdminChatMessages 按顺序返回会话里的全部消息。
func (s *SQLiteStore) AdminChatMessages(ctx context.Context, sessionID string) ([]AdminChatMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, role, content, error, steps, created_at FROM admin_chat_messages WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminChatMessage
	for rows.Next() {
		var m AdminChatMessage
		var failed int
		var created int64
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &failed, &m.Steps, &created); err != nil {
			return nil, err
		}
		m.Error, m.CreatedAt = failed != 0, time.Unix(0, created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// AppendAdminChatMessage 追加一条消息并刷新会话的更新时间；会话还没标题时用这条用户消息当标题。
func (s *SQLiteStore) AppendAdminChatMessage(ctx context.Context, sessionID string, m AdminChatMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	failed := 0
	if m.Error {
		failed = 1
	}
	result, err := tx.ExecContext(ctx, `UPDATE admin_chat_sessions SET updated_at = ?, title = CASE WHEN title = '' AND ? = 'user' THEN ? ELSE title END WHERE id = ?`, m.CreatedAt.UnixNano(), m.Role, strings.TrimSpace(m.Content), sessionID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrAdminChatSessionNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_chat_messages(id, session_id, seq, role, content, error, steps, created_at)
VALUES (?, ?, COALESCE((SELECT MAX(seq) FROM admin_chat_messages WHERE session_id = ?), 0) + 1, ?, ?, ?, ?, ?)`, m.ID, sessionID, sessionID, m.Role, m.Content, failed, m.Steps, m.CreatedAt.UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) SetAdminChatLoadedTools(ctx context.Context, sessionID, tools string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin_chat_sessions SET loaded_tools = ? WHERE id = ?`, tools, sessionID)
	return err
}

// ClearAdminChatSession 清空消息、标题和已加载的工具，会话本身留着。
func (s *SQLiteStore) ClearAdminChatSession(ctx context.Context, sessionID string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_chat_messages WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE admin_chat_sessions SET title = '', loaded_tools = '', updated_at = ? WHERE id = ?`, at.UnixNano(), sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteAdminChatSession 连会话带消息一起删掉。
func (s *SQLiteStore) DeleteAdminChatSession(ctx context.Context, sessionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_chat_messages WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_chat_sessions WHERE id = ?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}
