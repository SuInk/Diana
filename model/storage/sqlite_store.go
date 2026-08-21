// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/updater"

	_ "modernc.org/sqlite"
)

const (
	defaultDatabasePath  = "data/diana.db"
	legacyDatabasePath   = "data/diana-qq-bot.db"
	llmConfigKey         = "llm_config"
	llmProfilesKey       = "llm_profiles"
	llmRegistryKey       = "llm_provider_registry"
	botConfigKey         = "bot_config"
	botProfilesKey       = "bot_profiles"
	botGroupConfigKey    = "bot_group_configs"
	pluginStateKey       = "plugin_states"
	remindersKey         = "reminders"
	updatePolicyKey      = "system_update_policy"
	replySuppressionsKey = "bot_reply_suppressions"
	webuiAuthKey         = "webui_auth"
	webuiSessionsKey     = "webui_sessions"
	releaseCacheKey      = "system_release_cache"
	inboundRecoveryKey   = "bot_inbound_recovery_checkpoint"
)

type SQLiteStore struct {
	db           *sql.DB
	path         string
	userMemoryMu sync.Mutex
}

// NewSQLiteStore 打开 SQLite 数据库并执行迁移。
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = defaultDatabasePath
	}
	resolvedPath := ""
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve sqlite path: %w", err)
		}
		path, err = migrateLegacyDatabasePath(absPath)
		if err != nil {
			return nil, err
		}
		resolvedPath = path
	}
	// 数据库目录可能不存在，先创建目录再打开 SQLite 文件。
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;
`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure sqlite: %w", err)
	}
	store := &SQLiteStore{db: db, path: resolvedPath}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func migrateLegacyDatabasePath(requestedPath string) (string, error) {
	base := filepath.Base(requestedPath)
	canonicalName := filepath.Base(defaultDatabasePath)
	legacyName := filepath.Base(legacyDatabasePath)
	if base != canonicalName && base != legacyName {
		return requestedPath, nil
	}

	directory := filepath.Dir(requestedPath)
	canonicalPath := filepath.Join(directory, canonicalName)
	legacyPath := filepath.Join(directory, legacyName)
	legacyExists, err := regularPathExists(legacyPath)
	if err != nil {
		return "", fmt.Errorf("inspect legacy SQLite database: %w", err)
	}
	canonicalExists, err := regularPathExists(canonicalPath)
	if err != nil {
		return "", fmt.Errorf("inspect canonical SQLite database: %w", err)
	}
	if !legacyExists {
		return canonicalPath, nil
	}
	if canonicalExists {
		legacyHasData, err := sqliteFamilyHasData(legacyPath)
		if err != nil {
			return "", err
		}
		canonicalHasData, err := sqliteFamilyHasData(canonicalPath)
		if err != nil {
			return "", err
		}
		switch {
		case !canonicalHasData:
			if err := removeEmptySQLiteFamily(canonicalPath); err != nil {
				return "", err
			}
		case !legacyHasData:
			if err := removeEmptySQLiteFamily(legacyPath); err != nil {
				return "", err
			}
			return canonicalPath, nil
		default:
			return "", fmt.Errorf("both legacy SQLite database %q and canonical database %q contain data; archive the obsolete copy before starting Diana", legacyPath, canonicalPath)
		}
	}
	if err := renameSQLiteFamily(legacyPath, canonicalPath); err != nil {
		return "", fmt.Errorf("rename legacy SQLite database to %s: %w", canonicalName, err)
	}
	return canonicalPath, nil
}

func sqliteFamilyHasData(databasePath string) (bool, error) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(databasePath + suffix)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("%s is not a regular file", databasePath+suffix)
		}
		if info.Size() > 0 {
			return true, nil
		}
	}
	return false, nil
}

func removeEmptySQLiteFamily(databasePath string) error {
	hasData, err := sqliteFamilyHasData(databasePath)
	if err != nil {
		return err
	}
	if hasData {
		return fmt.Errorf("refusing to remove non-empty SQLite database family %s", databasePath)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(databasePath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func renameSQLiteFamily(sourcePath, targetPath string) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	type renamedFile struct {
		source string
		target string
	}
	renamed := make([]renamedFile, 0, 3)
	rollback := func() {
		for index := len(renamed) - 1; index >= 0; index-- {
			_ = os.Rename(renamed[index].target, renamed[index].source)
		}
	}
	// Move the main database last so an interrupted migration never exposes a
	// canonical main file without its existing WAL sidecars.
	for _, suffix := range []string{"-wal", "-shm", ""} {
		source := sourcePath + suffix
		exists, err := regularPathExists(source)
		if err != nil {
			rollback()
			return err
		}
		if !exists {
			continue
		}
		target := targetPath + suffix
		targetExists, err := regularPathExists(target)
		if err != nil {
			rollback()
			return err
		}
		if targetExists {
			rollback()
			return fmt.Errorf("target file already exists: %s", target)
		}
		if err := os.Rename(source, target); err != nil {
			rollback()
			return err
		}
		renamed = append(renamed, renamedFile{source: source, target: target})
	}
	return nil
}

func regularPathExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", path)
	}
	return true, nil
}

// Path returns the absolute path of the SQLite database opened by this store.
// The Release updater uses it after shutdown to create a consistent backup.
func (s *SQLiteStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
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

// LoadLLMProviderRegistry reads the versioned provider/model document.
func (s *SQLiteStore) LoadLLMProviderRegistry(ctx context.Context) (llm.ProviderRegistryDocument, bool, error) {
	var document llm.ProviderRegistryDocument
	ok, err := s.loadJSON(ctx, llmRegistryKey, &document)
	return document, ok, err
}

// SaveLLMProviderRegistry persists the provider/model document alongside the
// legacy profile set during the migration window.
func (s *SQLiteStore) SaveLLMProviderRegistry(ctx context.Context, document llm.ProviderRegistryDocument) error {
	return s.saveJSON(ctx, llmRegistryKey, document)
}

// LoadBotProfileConfig 读取 OneBot v11 机器人配置。
func (s *SQLiteStore) LoadBotProfileConfig(ctx context.Context) (assistant.BotConfig, bool, error) {
	var cfg assistant.BotConfig
	ok, err := s.loadJSON(ctx, botConfigKey, &cfg)
	return cfg, ok, err
}

// SaveBotProfileConfig 保存 OneBot v11 机器人配置。
func (s *SQLiteStore) SaveBotProfileConfig(ctx context.Context, cfg assistant.BotConfig) error {
	return s.saveJSON(ctx, botConfigKey, cfg)
}

// LoadBotProfiles 读取 OneBot v11 机器人配置集。
func (s *SQLiteStore) LoadBotProfiles(ctx context.Context) (assistant.ProfileSet, bool, error) {
	var set assistant.ProfileSet
	ok, err := s.loadJSON(ctx, botProfilesKey, &set)
	return set, ok, err
}

// SaveBotProfiles 保存 OneBot v11 机器人配置集。
func (s *SQLiteStore) SaveBotProfiles(ctx context.Context, set assistant.ProfileSet) error {
	return s.saveJSON(ctx, botProfilesKey, set)
}

// LoadBotGroupConfigs 读取 群级机器人配置。
func (s *SQLiteStore) LoadBotGroupConfigs(ctx context.Context) (assistant.GroupConfigSet, bool, error) {
	var set assistant.GroupConfigSet
	ok, err := s.loadJSON(ctx, botGroupConfigKey, &set)
	return set, ok, err
}

// SaveBotGroupConfigs 保存 群级机器人配置。
func (s *SQLiteStore) SaveBotGroupConfigs(ctx context.Context, set assistant.GroupConfigSet) error {
	return s.saveJSON(ctx, botGroupConfigKey, set)
}

// WebUIAuth 是 WebUI 管理密码的持久化记录。
type WebUIAuth struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	Salt         string    `json:"salt"`
	Iterations   int       `json:"iterations"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// WebUISession 是一次已登录会话；只存 token 哈希，不落明文。
type WebUISession struct {
	ID         string    `json:"id,omitempty"`
	TokenHash  string    `json:"token_hash"`
	DeviceName string    `json:"device_name,omitempty"`
	UserAgent  string    `json:"user_agent,omitempty"`
	IPAddress  string    `json:"ip_address,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
	LastSeenAt time.Time `json:"last_seen_at,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
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

// LoadUpdatePolicy loads the persistent OTA automation policy.
func (s *SQLiteStore) LoadUpdatePolicy(ctx context.Context) (updater.UpdatePolicy, bool, error) {
	var policy updater.UpdatePolicy
	ok, err := s.loadJSON(ctx, updatePolicyKey, &policy)
	return policy, ok, err
}

// SaveUpdatePolicy persists the OTA automation policy across restarts.
func (s *SQLiteStore) SaveUpdatePolicy(ctx context.Context, policy updater.UpdatePolicy) error {
	return s.saveJSON(ctx, updatePolicyKey, policy)
}

// LoadReleaseCache loads the persisted GitHub Release metadata cache.
func (s *SQLiteStore) LoadReleaseCache(ctx context.Context) ([]byte, bool, error) {
	var payload json.RawMessage
	ok, err := s.loadJSON(ctx, releaseCacheKey, &payload)
	return append([]byte(nil), payload...), ok, err
}

// SaveReleaseCache persists GitHub Release metadata and rate-limit reset state.
func (s *SQLiteStore) SaveReleaseCache(ctx context.Context, payload []byte) error {
	if !json.Valid(payload) {
		return errors.New("invalid system release cache JSON")
	}
	return s.saveJSON(ctx, releaseCacheKey, json.RawMessage(payload))
}

// LoadInboundRecoveryCheckpoint returns the latest instant when the channel
// was known to be online. The coordinator uses it to size reconnect backfill.
func (s *SQLiteStore) LoadInboundRecoveryCheckpoint(ctx context.Context) (time.Time, bool, error) {
	var checkpoint time.Time
	ok, err := s.loadJSON(ctx, inboundRecoveryKey, &checkpoint)
	return checkpoint, ok, err
}

// SaveInboundRecoveryCheckpoint persists the online boundary across restarts.
func (s *SQLiteStore) SaveInboundRecoveryCheckpoint(ctx context.Context, connectedAt time.Time) error {
	return s.saveJSON(ctx, inboundRecoveryKey, connectedAt.UTC())
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
CREATE INDEX IF NOT EXISTS idx_app_logs_trace_target ON app_logs(kind, action, target, created_at ASC);
`)
	if err != nil {
		return err
	}
	return s.migrateRestoredFeatures()
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
// legacyStateKeys 是历史上带平台名的 app_state 键。新键读不到时回退到旧键，
// 否则升级之后机器人配置、群配置和恢复位点会当成「从未保存过」。写入一律用新键。
var legacyStateKeys = map[string]string{
	botConfigKey:         "qqbot_config",
	botProfilesKey:       "qqbot_profiles",
	botGroupConfigKey:    "qqbot_group_configs",
	replySuppressionsKey: "qqbot_reply_suppressions",
	inboundRecoveryKey:   "qqbot_inbound_recovery_checkpoint",
}

func (s *SQLiteStore) loadJSON(ctx context.Context, key string, dest any) (bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		if legacy, ok := legacyStateKeys[key]; ok {
			err = s.db.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key = ?`, legacy).Scan(&raw)
		}
	}
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
