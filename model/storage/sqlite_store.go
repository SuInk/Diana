package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/updater"

	_ "modernc.org/sqlite"
)

const (
	defaultDatabasePath = "data/diana.db"
	legacyDatabasePath  = "data/diana-qq-bot.db"
	llmConfigKey        = "llm_config"
	llmProfilesKey      = "llm_profiles"
	qqbotConfigKey      = "qqbot_config"
	qqbotProfilesKey    = "qqbot_profiles"
	qqbotGroupConfigKey = "qqbot_group_configs"
	pluginStateKey      = "plugin_states"
	remindersKey        = "reminders"
	updateSettingsKey   = "system_update_settings"
	webuiAuthKey        = "webui_auth"
	webuiSessionsKey    = "webui_sessions"
)

type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 打开 SQLite 数据库并执行迁移。
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = defaultDatabasePath
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if _, legacyErr := os.Stat(legacyDatabasePath); legacyErr == nil {
				path = legacyDatabasePath
			}
		}
	}
	// 数据库目录可能不存在，先创建目录再打开 SQLite 文件。
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &SQLiteStore{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close 关闭 SQLite 数据库连接。
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// LoadLLMConfig 读取旧版单配置 LLM 数据。
func (s *SQLiteStore) LoadLLMConfig(ctx context.Context) (llm.ProviderConfig, bool, error) {
	var cfg llm.ProviderConfig
	ok, err := s.loadJSON(ctx, llmConfigKey, &cfg)
	return cfg, ok, err
}

// SaveLLMConfig 保存旧版单配置 LLM 数据。
func (s *SQLiteStore) SaveLLMConfig(ctx context.Context, cfg llm.ProviderConfig) error {
	return s.saveJSON(ctx, llmConfigKey, cfg)
}

// LoadLLMProfiles 读取 LLM 配置集。
func (s *SQLiteStore) LoadLLMProfiles(ctx context.Context) (llm.ProfileSet, bool, error) {
	var set llm.ProfileSet
	ok, err := s.loadJSON(ctx, llmProfilesKey, &set)
	return set, ok, err
}

// SaveLLMProfiles 保存 LLM 配置集。
func (s *SQLiteStore) SaveLLMProfiles(ctx context.Context, set llm.ProfileSet) error {
	return s.saveJSON(ctx, llmProfilesKey, set)
}

// LoadQQBotConfig 读取 QQ 机器人配置。
func (s *SQLiteStore) LoadQQBotConfig(ctx context.Context) (assistant.BotConfig, bool, error) {
	var cfg assistant.BotConfig
	ok, err := s.loadJSON(ctx, qqbotConfigKey, &cfg)
	return cfg, ok, err
}

// SaveQQBotConfig 保存 QQ 机器人配置。
func (s *SQLiteStore) SaveQQBotConfig(ctx context.Context, cfg assistant.BotConfig) error {
	return s.saveJSON(ctx, qqbotConfigKey, cfg)
}

// LoadQQBotProfiles 读取 QQ 机器人配置集。
func (s *SQLiteStore) LoadQQBotProfiles(ctx context.Context) (assistant.ProfileSet, bool, error) {
	var set assistant.ProfileSet
	ok, err := s.loadJSON(ctx, qqbotProfilesKey, &set)
	return set, ok, err
}

// SaveQQBotProfiles 保存 QQ 机器人配置集。
func (s *SQLiteStore) SaveQQBotProfiles(ctx context.Context, set assistant.ProfileSet) error {
	return s.saveJSON(ctx, qqbotProfilesKey, set)
}

// LoadQQBotGroupConfigs 读取 QQ 群级机器人配置。
func (s *SQLiteStore) LoadQQBotGroupConfigs(ctx context.Context) (assistant.GroupConfigSet, bool, error) {
	var set assistant.GroupConfigSet
	ok, err := s.loadJSON(ctx, qqbotGroupConfigKey, &set)
	return set, ok, err
}

// SaveQQBotGroupConfigs 保存 QQ 群级机器人配置。
func (s *SQLiteStore) SaveQQBotGroupConfigs(ctx context.Context, set assistant.GroupConfigSet) error {
	return s.saveJSON(ctx, qqbotGroupConfigKey, set)
}

// WebUIAuth 是 WebUI 管理密码的持久化记录。
type WebUIAuth struct {
	PasswordHash string    `json:"password_hash"`
	Salt         string    `json:"salt"`
	Iterations   int       `json:"iterations"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// WebUISession 是一次已登录会话；只存 token 哈希，不落明文。
type WebUISession struct {
	TokenHash string    `json:"token_hash"`
	ExpiresAt time.Time `json:"expires_at"`
}

// WebUISessionSet 是全部有效会话集合。
type WebUISessionSet struct {
	Sessions []WebUISession `json:"sessions"`
}

// LoadWebUIAuth 读取 WebUI 管理密码记录。
func (s *SQLiteStore) LoadWebUIAuth(ctx context.Context) (WebUIAuth, bool, error) {
	var auth WebUIAuth
	ok, err := s.loadJSON(ctx, webuiAuthKey, &auth)
	return auth, ok, err
}

// SaveWebUIAuth 保存 WebUI 管理密码记录。
func (s *SQLiteStore) SaveWebUIAuth(ctx context.Context, auth WebUIAuth) error {
	return s.saveJSON(ctx, webuiAuthKey, auth)
}

// LoadWebUISessions 读取 WebUI 登录会话集合。
func (s *SQLiteStore) LoadWebUISessions(ctx context.Context) (WebUISessionSet, bool, error) {
	var set WebUISessionSet
	ok, err := s.loadJSON(ctx, webuiSessionsKey, &set)
	return set, ok, err
}

// SaveWebUISessions 保存 WebUI 登录会话集合。
func (s *SQLiteStore) SaveWebUISessions(ctx context.Context, set WebUISessionSet) error {
	return s.saveJSON(ctx, webuiSessionsKey, set)
}

// LoadUpdateSettings 读取系统自动更新设置。
func (s *SQLiteStore) LoadUpdateSettings(ctx context.Context) (updater.Settings, bool, error) {
	var settings updater.Settings
	ok, err := s.loadJSON(ctx, updateSettingsKey, &settings)
	return settings, ok, err
}

// SaveUpdateSettings 保存系统自动更新设置。
func (s *SQLiteStore) SaveUpdateSettings(ctx context.Context, settings updater.Settings) error {
	return s.saveJSON(ctx, updateSettingsKey, settings)
}

// LoadPluginStates 读取插件状态。
func (s *SQLiteStore) LoadPluginStates(ctx context.Context) (map[string]assistant.PluginState, bool, error) {
	var states map[string]assistant.PluginState
	ok, err := s.loadJSON(ctx, pluginStateKey, &states)
	return states, ok, err
}

// SavePluginStates 保存插件状态。
func (s *SQLiteStore) SavePluginStates(ctx context.Context, states map[string]assistant.PluginState) error {
	return s.saveJSON(ctx, pluginStateKey, states)
}

// LoadReminders 读取提醒列表。
func (s *SQLiteStore) LoadReminders(ctx context.Context) ([]assistant.Reminder, bool, error) {
	var reminders []assistant.Reminder
	ok, err := s.loadJSON(ctx, remindersKey, &reminders)
	return reminders, ok, err
}

// SaveReminders 保存提醒列表。
func (s *SQLiteStore) SaveReminders(ctx context.Context, reminders []assistant.Reminder) error {
	return s.saveJSON(ctx, remindersKey, reminders)
}

// migrate 创建或升级 SQLite 表结构。
func (s *SQLiteStore) migrate() error {
	// app_state 用 key-value JSON 保存配置类状态；app_logs 单独建表方便按时间倒序查询。
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS app_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS app_logs (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  level TEXT NOT NULL,
  action TEXT NOT NULL,
  message TEXT NOT NULL,
  detail TEXT,
  actor TEXT,
  target TEXT,
  metadata TEXT,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_app_logs_kind_created_at ON app_logs(kind, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_app_logs_created_at ON app_logs(created_at DESC);
`)
	return err
}

// saveJSON 将指定 key 的结构体编码为 JSON 保存。
func (s *SQLiteStore) saveJSON(ctx context.Context, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	// 同一个 key 反复覆盖，updated_at 用于后续排查配置最后一次写入时间。
	_, err = s.db.ExecContext(ctx, `
INSERT INTO app_state (key, value, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP
`, key, string(data))
	return err
}

// loadJSON 读取指定 key 的 JSON 并解码。
func (s *SQLiteStore) loadJSON(ctx context.Context, key string, dest any) (bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key = ?`, key).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// bool 返回值表示“没有保存过”，调用方据此使用默认配置或环境变量。
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), dest); err != nil {
		return false, fmt.Errorf("decode %s: %w", key, err)
	}
	return true, nil
}
