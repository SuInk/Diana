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
	"github.com/SuInk/diana/model/llmauth"
	"github.com/SuInk/diana/model/updater"

	_ "modernc.org/sqlite"
)

const (
	defaultDatabasePath  = "data/diana.db"
	llmProfilesKey       = "llm_profiles"
	llmRegistryKey       = "llm_provider_registry"
	llmAuthKey           = "llm_oauth"
	botProfilesKey       = "bot_profiles"
	botPersonasKey       = "bot_personas"
	botWorldBookKey      = "bot_world_book"
	botGroupConfigKey    = "bot_group_configs"
	pluginStateKey       = "plugin_states"
	remindersKey         = "reminders"
	updatePolicyKey      = "system_update_policy"
	replySuppressionsKey = "bot_reply_suppressions"
	webuiAuthKey         = "webui_auth"
	webuiSessionsKey     = "webui_sessions"
	webuiAPIKeysKey      = "webui_api_keys"
	releaseCacheKey      = "system_release_cache"
	updateGitHubTokenKey = "system_update_github_token"
	inboundRecoveryKey   = "bot_inbound_recovery_checkpoint"
)

type SQLiteStore struct {
	db   *sql.DB
	path string
	// historyFTS 表示历史检索能否走 FTS5 倒排索引。个别构建里 FTS5 不可用，
	// 那时回退到 LIKE 检索：慢，但功能不中断。
	historyFTS bool
	// historyVectors 表示语义向量表是否可用。
	historyVectors bool
	userMemoryMu   sync.Mutex
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
		path = absPath
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

// LoadLLMProfiles 读取提供商配置集。
func (s *SQLiteStore) LoadLLMProfiles(ctx context.Context) (llm.ProfileSet, bool, error) {
	var set llm.ProfileSet
	ok, err := s.loadJSON(ctx, llmProfilesKey, &set)
	return set, ok, err
}

// SaveLLMProfiles 保存提供商配置集。
func (s *SQLiteStore) SaveLLMProfiles(ctx context.Context, set llm.ProfileSet) error {
	return s.saveJSON(ctx, llmProfilesKey, set)
}

// LoadLLMProviderRegistry reads the versioned provider/model document.
func (s *SQLiteStore) LoadLLMProviderRegistry(ctx context.Context) (llm.ProviderRegistryDocument, bool, error) {
	var document llm.ProviderRegistryDocument
	ok, err := s.loadJSON(ctx, llmRegistryKey, &document)
	return document, ok, err
}

// SaveLLMProviderRegistry persists the provider/model document.
func (s *SQLiteStore) SaveLLMProviderRegistry(ctx context.Context, document llm.ProviderRegistryDocument) error {
	return s.saveJSON(ctx, llmRegistryKey, document)
}

// LoadLLMAuth 读取 OAuth 提供商与令牌。
//
// 和 API Key 同库同待遇：这份文档里是明文凭据，任何对外接口都必须先脱敏再返回。
func (s *SQLiteStore) LoadLLMAuth(ctx context.Context) (llmauth.Document, error) {
	var document llmauth.Document
	if _, err := s.loadJSON(ctx, llmAuthKey, &document); err != nil {
		return llmauth.Document{}, err
	}
	return document, nil
}

// SaveLLMAuth 保存 OAuth 提供商与令牌。
func (s *SQLiteStore) SaveLLMAuth(ctx context.Context, document llmauth.Document) error {
	return s.saveJSON(ctx, llmAuthKey, document)
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

// LoadBotPersonas 读取人设库。
func (s *SQLiteStore) LoadBotPersonas(ctx context.Context) (assistant.PersonaSet, bool, error) {
	var set assistant.PersonaSet
	ok, err := s.loadJSON(ctx, botPersonasKey, &set)
	return set, ok, err
}

// SaveBotPersonas 保存人设库。
func (s *SQLiteStore) SaveBotPersonas(ctx context.Context, set assistant.PersonaSet) error {
	return s.saveJSON(ctx, botPersonasKey, set)
}

// LoadWorldBook 读取世界书（世界观设定库）。
func (s *SQLiteStore) LoadWorldBook(ctx context.Context) (assistant.WorldBook, bool, error) {
	var tree assistant.WorldBook
	ok, err := s.loadJSON(ctx, botWorldBookKey, &tree)
	return tree, ok, err
}

// SaveWorldBook 保存世界书。
func (s *SQLiteStore) SaveWorldBook(ctx context.Context, tree assistant.WorldBook) error {
	return s.saveJSON(ctx, botWorldBookKey, tree)
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

// WebUIAPIKey 是对外开放接口的一把访问密钥；只存 token 哈希，不落明文。
type WebUIAPIKey struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Prefix     string    `json:"prefix"`
	TokenHash  string    `json:"token_hash"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

// WebUIAPIKeySet 是全部有效 API 密钥集合。
type WebUIAPIKeySet struct {
	Keys []WebUIAPIKey `json:"keys"`
}

// LoadWebUIAPIKeys 读取对外开放接口密钥集合。
func (s *SQLiteStore) LoadWebUIAPIKeys(ctx context.Context) (WebUIAPIKeySet, bool, error) {
	var set WebUIAPIKeySet
	ok, err := s.loadJSON(ctx, webuiAPIKeysKey, &set)
	return set, ok, err
}

// SaveWebUIAPIKeys 保存对外开放接口密钥集合。
func (s *SQLiteStore) SaveWebUIAPIKeys(ctx context.Context, set WebUIAPIKeySet) error {
	return s.saveJSON(ctx, webuiAPIKeysKey, set)
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

// LoadUpdateGitHubToken loads the optional token used only for GitHub update metadata requests.
func (s *SQLiteStore) LoadUpdateGitHubToken(ctx context.Context) (string, bool, error) {
	var token string
	ok, err := s.loadJSON(ctx, updateGitHubTokenKey, &token)
	return token, ok, err
}

// SaveUpdateGitHubToken persists the optional GitHub update token.
func (s *SQLiteStore) SaveUpdateGitHubToken(ctx context.Context, token string) error {
	return s.saveJSON(ctx, updateGitHubTokenKey, strings.TrimSpace(token))
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
	if err := s.migrateRestoredFeatures(); err != nil {
		return err
	}
	if err := s.ensureStickerAssets(); err != nil {
		return err
	}
	s.historyFTS = ensureMessageHistoryFTS(s.db)
	s.historyVectors = ensureMessageHistoryVectors(s.db)
	return nil
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
			// bool 返回值表示“没有保存过”，调用方据此使用 config.yaml 里的播种配置或内置默认值。
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), dest); err != nil {
		return false, fmt.Errorf("decode %s: %w", key, err)
	}
	return true, nil
}
