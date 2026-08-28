// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

const (
	// openAPIKeyPlaintextPrefix 让密钥在日志或配置里泄露时一眼能认出来源，
	// 也方便密钥扫描工具按前缀匹配。
	openAPIKeyPlaintextPrefix = "diana_"
	openAPIKeyRandomBytes     = 32
	// openAPIKeyPrefixRunes 是列表里展示的明文前缀长度，够辨认、不够重放。
	openAPIKeyPrefixRunes = 14
	openAPIKeyNameMaxLen  = 64
	// openAPIMaxTextRunes 拦下明显不是通知的超长正文；正常推送远小于它。
	openAPIMaxTextRunes = 8000
	openAPIPushTimeout  = 30 * time.Second
)

var (
	ErrOpenAPIKeyNameInvalid = errors.New("密钥名称需为 1-64 个字符，且不能包含控制字符")
	ErrOpenAPIKeyNotFound    = errors.New("密钥不存在")
)

// OpenAPIKeyStore 持久化对外开放接口的密钥集合。
type OpenAPIKeyStore interface {
	LoadWebUIAPIKeys(ctx context.Context) (storage.WebUIAPIKeySet, bool, error)
	SaveWebUIAPIKeys(ctx context.Context, set storage.WebUIAPIKeySet) error
}

// OpenAPIKeyInfo 是密钥在管理接口里的展示形态，永远不含 token 哈希。
type OpenAPIKeyInfo struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// OpenAPIKeyManager 管理对外开放接口的访问密钥。明文只在创建那一刻返回一次，
// 存储和内存里都只有 SHA-256 哈希。
type OpenAPIKeyManager struct {
	store OpenAPIKeyStore

	mu   sync.Mutex
	keys map[string]storage.WebUIAPIKey // token 哈希 -> 密钥元数据
}

// NewOpenAPIKeyManager 创建密钥管理器并从存储加载状态。
func NewOpenAPIKeyManager(store OpenAPIKeyStore) *OpenAPIKeyManager {
	m := &OpenAPIKeyManager{store: store, keys: map[string]storage.WebUIAPIKey{}}
	if store == nil {
		return m
	}
	if set, ok, err := store.LoadWebUIAPIKeys(context.Background()); err == nil && ok {
		for _, key := range set.Keys {
			key.TokenHash = strings.TrimSpace(key.TokenHash)
			if key.TokenHash != "" {
				m.keys[key.TokenHash] = key
			}
		}
	}
	return m
}

func validateOpenAPIKeyName(name string) error {
	length := len([]rune(name))
	if length < 1 || length > openAPIKeyNameMaxLen {
		return ErrOpenAPIKeyNameInvalid
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return ErrOpenAPIKeyNameInvalid
		}
	}
	return nil
}

// Create 生成一把新密钥；返回值里的 token 是唯一一次能拿到明文的机会。
func (m *OpenAPIKeyManager) Create(name string) (OpenAPIKeyInfo, string, error) {
	name = strings.TrimSpace(name)
	if err := validateOpenAPIKeyName(name); err != nil {
		return OpenAPIKeyInfo{}, "", err
	}
	raw := make([]byte, openAPIKeyRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return OpenAPIKeyInfo{}, "", err
	}
	token := openAPIKeyPlaintextPrefix + hex.EncodeToString(raw)
	tokenHash := hashToken(token)
	record := storage.WebUIAPIKey{
		ID:        webUISessionID(tokenHash),
		Name:      name,
		Prefix:    token[:openAPIKeyPrefixRunes],
		TokenHash: tokenHash,
		CreatedAt: time.Now(),
	}
	m.mu.Lock()
	m.keys[tokenHash] = record
	m.mu.Unlock()
	if err := m.persist(); err != nil {
		// 持久化失败时回滚内存态，避免一把重启就消失的“幽灵密钥”。
		m.mu.Lock()
		delete(m.keys, tokenHash)
		m.mu.Unlock()
		return OpenAPIKeyInfo{}, "", err
	}
	return openAPIKeyInfo(record), token, nil
}

// List 返回全部密钥，按创建时间倒序。
func (m *OpenAPIKeyManager) List() []OpenAPIKeyInfo {
	m.mu.Lock()
	items := make([]OpenAPIKeyInfo, 0, len(m.keys))
	for _, key := range m.keys {
		items = append(items, openAPIKeyInfo(key))
	}
	m.mu.Unlock()
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].ID < items[j].ID
	})
	return items
}

// Revoke 吊销指定密钥；吊销即删除，没有恢复。
func (m *OpenAPIKeyManager) Revoke(id string) (OpenAPIKeyInfo, error) {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	foundHash := ""
	var found storage.WebUIAPIKey
	for tokenHash, key := range m.keys {
		if key.ID == id {
			foundHash = tokenHash
			found = key
			break
		}
	}
	if foundHash != "" {
		delete(m.keys, foundHash)
	}
	m.mu.Unlock()
	if foundHash == "" {
		return OpenAPIKeyInfo{}, ErrOpenAPIKeyNotFound
	}
	if err := m.persist(); err != nil {
		return OpenAPIKeyInfo{}, err
	}
	return openAPIKeyInfo(found), nil
}

// Authenticate 校验请求携带的密钥明文，命中时更新最近使用时间。
func (m *OpenAPIKeyManager) Authenticate(token string) (OpenAPIKeyInfo, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return OpenAPIKeyInfo{}, false
	}
	tokenHash := hashToken(token)
	m.mu.Lock()
	key, ok := m.keys[tokenHash]
	if !ok {
		m.mu.Unlock()
		return OpenAPIKeyInfo{}, false
	}
	now := time.Now()
	// 最近使用时间的精度要求不高，攒 5 分钟写一次，别让每个请求都落一次盘。
	persist := key.LastUsedAt.IsZero() || now.Sub(key.LastUsedAt) >= 5*time.Minute
	if persist {
		key.LastUsedAt = now
		m.keys[tokenHash] = key
	}
	m.mu.Unlock()
	if persist {
		_ = m.persist()
	}
	return openAPIKeyInfo(key), true
}

// persist 把密钥集合写回存储。
func (m *OpenAPIKeyManager) persist() error {
	if m.store == nil {
		return nil
	}
	m.mu.Lock()
	set := storage.WebUIAPIKeySet{Keys: make([]storage.WebUIAPIKey, 0, len(m.keys))}
	for tokenHash, key := range m.keys {
		key.TokenHash = tokenHash
		set.Keys = append(set.Keys, key)
	}
	m.mu.Unlock()
	sort.Slice(set.Keys, func(i, j int) bool { return set.Keys[i].ID < set.Keys[j].ID })
	return m.store.SaveWebUIAPIKeys(context.Background(), set)
}

func openAPIKeyInfo(key storage.WebUIAPIKey) OpenAPIKeyInfo {
	info := OpenAPIKeyInfo{
		ID:        key.ID,
		Name:      key.Name,
		Prefix:    key.Prefix,
		CreatedAt: key.CreatedAt,
	}
	if !key.LastUsedAt.IsZero() {
		lastUsed := key.LastUsedAt
		info.LastUsedAt = &lastUsed
	}
	return info
}

// ExternalMessagePusher 是对外推送依赖的运行时能力。
type ExternalMessagePusher interface {
	PushExternalMessage(ctx context.Context, target assistant.ExternalMessageTarget, text string) error
	Status() assistant.RuntimeStatus
}

// OpenAPIPluginGate 提供「对外 API」插件的启用状态与生效设置；由
// *assistant.PluginManager 满足。开关和限流参数都归插件系统管，这个
// 处理器只在每个外部请求上现查现用。
type OpenAPIPluginGate interface {
	PluginWithSettings(id string, overrides map[string]bool) (assistant.Plugin, assistant.SettingValues, bool)
}

// openAPIRateLimiter 是按密钥的固定窗口限流器。窗口重开即清零，实现最简单，
// 对「防失控脚本」这个目的足够。上限由调用方按插件设置逐次传入，
// 改设置立即生效，不用重建限流器。
type openAPIRateLimiter struct {
	window time.Duration

	mu      sync.Mutex
	starts  map[string]time.Time
	counts  map[string]int
	lastGC  time.Time
	nowFunc func() time.Time
}

func newOpenAPIRateLimiter(window time.Duration) *openAPIRateLimiter {
	return &openAPIRateLimiter{
		window:  window,
		starts:  map[string]time.Time{},
		counts:  map[string]int{},
		nowFunc: time.Now,
	}
}

// allow 报告这次调用是否放行；拒绝时返回距窗口重开的等待时长。
func (l *openAPIRateLimiter) allow(key string, limit int) (bool, time.Duration) {
	now := l.nowFunc()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastGC) >= l.window {
		for id, start := range l.starts {
			if now.Sub(start) >= l.window {
				delete(l.starts, id)
				delete(l.counts, id)
			}
		}
		l.lastGC = now
	}
	start, ok := l.starts[key]
	if !ok || now.Sub(start) >= l.window {
		l.starts[key] = now
		l.counts[key] = 1
		return true, 0
	}
	if l.counts[key] >= limit {
		return false, l.window - now.Sub(start)
	}
	l.counts[key]++
	return true, 0
}

// OpenAPIHandler 暴露对外开放接口：管理端在 /api/openapi 下管密钥（走 WebUI
// 会话鉴权），外部系统在 /openapi/v1 下用 Bearer 密钥推送消息。
// 总开关是「对外 API」内置插件：停用后外部调用一律 403，密钥管理不受影响，
// 主人可以先备好密钥再开闸。
type OpenAPIHandler struct {
	manager *OpenAPIKeyManager
	pusher  ExternalMessagePusher
	plugins OpenAPIPluginGate
	logs    AppLogWriter
	limiter *openAPIRateLimiter
}

// NewOpenAPIHandler 创建对外开放接口处理器。
func NewOpenAPIHandler(manager *OpenAPIKeyManager, pusher ExternalMessagePusher, plugins OpenAPIPluginGate) *OpenAPIHandler {
	return &OpenAPIHandler{
		manager: manager,
		pusher:  pusher,
		plugins: plugins,
		limiter: newOpenAPIRateLimiter(time.Minute),
	}
}

// SetLogStore 注入操作日志写入器。
func (h *OpenAPIHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// Register 注册管理与开放路由。/openapi 前缀不在 /api 下，WebUI 会话中间件
// 不会碰它；这些路由完全由 Bearer 密钥鉴权。
func (h *OpenAPIHandler) Register(router gin.IRouter) {
	router.GET("/api/openapi/keys", h.listKeys)
	router.POST("/api/openapi/keys", h.createKey)
	router.DELETE("/api/openapi/keys/:id", h.revokeKey)

	router.GET("/openapi/v1/status", h.status)
	router.POST("/openapi/v1/messages", h.pushMessage)
}

func (h *OpenAPIHandler) listKeys(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"keys": h.manager.List()})
}

func (h *OpenAPIHandler) createKey(c *gin.Context) {
	var payload struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	info, token, err := h.manager.Create(payload.Name)
	if err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, ErrOpenAPIKeyNameInvalid) {
			status = http.StatusInternalServerError
		}
		logAndWriteError(c, h.logs, status, "openapi.key.create", err, "", nil)
		return
	}
	recordRequestOperation(c, h.logs, "openapi.key.create", "创建对外 API 密钥", info.ID, map[string]any{"name": info.Name})
	c.Header("Cache-Control", "no-store")
	// token 只在这次响应里出现，之后任何接口都拿不到明文。
	c.JSON(http.StatusOK, gin.H{"key": info, "token": token})
}

func (h *OpenAPIHandler) revokeKey(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		writeError(c, http.StatusBadRequest, errors.New("key id is required"))
		return
	}
	info, err := h.manager.Revoke(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrOpenAPIKeyNotFound) {
			status = http.StatusNotFound
		}
		logAndWriteError(c, h.logs, status, "openapi.key.revoke", err, id, nil)
		return
	}
	recordRequestOperation(c, h.logs, "openapi.key.revoke", "吊销对外 API 密钥", info.ID, map[string]any{"name": info.Name})
	c.JSON(http.StatusOK, gin.H{"revoked": true})
}

// authenticateRequest 依次做插件开关、Bearer 密钥、按插件设置限流三道检查；
// 失败时已写响应。
func (h *OpenAPIHandler) authenticateRequest(c *gin.Context) (OpenAPIKeyInfo, bool) {
	settings, enabled := h.pluginSettings()
	if !enabled {
		writeError(c, http.StatusForbidden, errors.New("对外 API 插件未启用，请在控制台「插件」页开启"))
		return OpenAPIKeyInfo{}, false
	}
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(strings.TrimSpace(scheme), "Bearer") {
		c.Header("WWW-Authenticate", "Bearer")
		writeError(c, http.StatusUnauthorized, errors.New("missing bearer token"))
		return OpenAPIKeyInfo{}, false
	}
	info, ok := h.manager.Authenticate(token)
	if !ok {
		c.Header("WWW-Authenticate", "Bearer")
		writeError(c, http.StatusUnauthorized, errors.New("invalid api key"))
		return OpenAPIKeyInfo{}, false
	}
	if allowed, wait := h.limiter.allow(info.ID, assistant.OpenAPIRateLimitPerMinute(settings)); !allowed {
		c.Header("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(c, http.StatusTooManyRequests, errors.New("rate limit exceeded"))
		return OpenAPIKeyInfo{}, false
	}
	return info, true
}

// pluginSettings 返回「对外 API」插件的生效设置与启用状态。没接插件管理器
// 的直接构造（如测试）按未启用处理——闸门宁可失败关闭。
func (h *OpenAPIHandler) pluginSettings() (assistant.SettingValues, bool) {
	if h.plugins == nil {
		return nil, false
	}
	_, settings, ok := h.plugins.PluginWithSettings(assistant.OpenAPIPluginID, nil)
	return settings, ok
}

// status 供外部系统探活并发现可投递的通道；只暴露路由所需的最小信息。
func (h *OpenAPIHandler) status(c *gin.Context) {
	if _, ok := h.authenticateRequest(c); !ok {
		return
	}
	runtimeStatus := h.pusher.Status()
	channels := make([]gin.H, 0, len(runtimeStatus.Channels))
	for _, channel := range runtimeStatus.Channels {
		channels = append(channels, gin.H{
			"profile_id": channel.ProfileID,
			"platform":   channel.Platform,
			"name":       channel.Name,
			"connected":  channel.Connected,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"running":  runtimeStatus.Running,
		"channels": channels,
	})
}

func (h *OpenAPIHandler) pushMessage(c *gin.Context) {
	key, ok := h.authenticateRequest(c)
	if !ok {
		return
	}
	var payload struct {
		Platform  string `json:"platform"`
		ProfileID string `json:"profile_id"`
		GroupID   string `json:"group_id"`
		UserID    string `json:"user_id"`
		Text      string `json:"text"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		writeError(c, http.StatusBadRequest, errors.New("text is required"))
		return
	}
	if len([]rune(text)) > openAPIMaxTextRunes {
		writeError(c, http.StatusBadRequest, fmt.Errorf("text exceeds %d characters", openAPIMaxTextRunes))
		return
	}
	target := assistant.ExternalMessageTarget{
		Platform:  payload.Platform,
		ProfileID: payload.ProfileID,
		GroupID:   payload.GroupID,
		UserID:    payload.UserID,
	}
	if strings.TrimSpace(target.GroupID) == "" && strings.TrimSpace(target.UserID) == "" {
		writeError(c, http.StatusBadRequest, errors.New("either group_id or user_id is required"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), openAPIPushTimeout)
	defer cancel()
	targetLabel := openAPITargetLabel(target)
	if err := h.pusher.PushExternalMessage(ctx, target, text); err != nil {
		// 正文本身不进日志：推送内容归调用方，日志只记发没发出去。
		recordAppLog(c.Request.Context(), h.logs, storage.AppLogEntry{
			Kind:     storage.LogKindError,
			Level:    storage.LogLevelError,
			Action:   "openapi.message",
			Message:  err.Error(),
			Actor:    "openapi:" + key.Name,
			Target:   targetLabel,
			Metadata: map[string]any{"text_runes": len([]rune(text))},
		})
		writeError(c, http.StatusBadGateway, err)
		return
	}
	recordAppLog(c.Request.Context(), h.logs, storage.AppLogEntry{
		Kind:     storage.LogKindOperation,
		Level:    storage.LogLevelInfo,
		Action:   "openapi.message",
		Message:  "对外 API 推送已投递",
		Actor:    "openapi:" + key.Name,
		Target:   targetLabel,
		Metadata: map[string]any{"text_runes": len([]rune(text))},
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func openAPITargetLabel(target assistant.ExternalMessageTarget) string {
	if groupID := strings.TrimSpace(target.GroupID); groupID != "" {
		return "group:" + groupID
	}
	return "private:" + strings.TrimSpace(target.UserID)
}
