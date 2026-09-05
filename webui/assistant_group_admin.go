// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

const (
	groupAdminChallengeTTL = 10 * time.Minute
	groupAdminSessionTTL   = 2 * time.Hour
)

type groupAdminVerifier struct {
	mu         sync.Mutex
	challenges map[string]groupAdminChallenge
	sessions   map[string]groupAdminSession
}

type groupAdminChallenge struct {
	groupID   string
	userID    string
	code      string
	expiresAt time.Time
	attempts  int
}

type groupAdminSession struct {
	groupID   string
	userID    string
	expiresAt time.Time
}

func newGroupAdminVerifier() *groupAdminVerifier {
	return &groupAdminVerifier{
		challenges: map[string]groupAdminChallenge{},
		sessions:   map[string]groupAdminSession{},
	}
}

func (v *groupAdminVerifier) CreateChallenge(groupID string, userID string) (string, time.Time, error) {
	code, err := randomDigits(6)
	if err != nil {
		return "", time.Time{}, err
	}
	challenge := groupAdminChallenge{
		groupID:   groupID,
		userID:    userID,
		code:      code,
		expiresAt: time.Now().Add(groupAdminChallengeTTL),
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cleanupLocked(time.Now())
	v.challenges[groupAdminKey(groupID, userID)] = challenge
	return code, challenge.expiresAt, nil
}

func (v *groupAdminVerifier) Verify(groupID string, userID string, code string) (string, time.Time, error) {
	now := time.Now()
	key := groupAdminKey(groupID, userID)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cleanupLocked(now)
	challenge, ok := v.challenges[key]
	if !ok {
		return "", time.Time{}, fmt.Errorf("验证码不存在或已过期")
	}
	if challenge.attempts >= 5 {
		delete(v.challenges, key)
		return "", time.Time{}, fmt.Errorf("验证码尝试次数过多，请重新发送")
	}
	challenge.attempts++
	v.challenges[key] = challenge
	if strings.TrimSpace(code) != challenge.code {
		return "", time.Time{}, fmt.Errorf("验证码不正确")
	}
	delete(v.challenges, key)
	token, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	session := groupAdminSession{
		groupID:   groupID,
		userID:    userID,
		expiresAt: now.Add(groupAdminSessionTTL),
	}
	v.sessions[token] = session
	return token, session.expiresAt, nil
}

func (v *groupAdminVerifier) Session(token string) (groupAdminSession, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return groupAdminSession{}, false
	}
	now := time.Now()
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cleanupLocked(now)
	session, ok := v.sessions[token]
	return session, ok
}

func (v *groupAdminVerifier) cleanupLocked(now time.Time) {
	for key, challenge := range v.challenges {
		if now.After(challenge.expiresAt) {
			delete(v.challenges, key)
		}
	}
	for token, session := range v.sessions {
		if now.After(session.expiresAt) {
			delete(v.sessions, token)
		}
	}
}

func (h *BotHandler) startGroupAdminChallenge(c *gin.Context) {
	var payload groupAdminChallengePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_admin.challenge", err, "", nil)
		return
	}
	groupID, userID, err := normalizeGroupAdminIdentity(payload.GroupID, payload.UserID)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_admin.challenge", err, groupID, nil)
		return
	}
	if err := h.requireGroupAdmin(c.Request.Context(), groupID, userID); err != nil {
		h.writeError(c, http.StatusForbidden, "assistant.group_admin.challenge", err, groupID, map[string]any{"group_id": groupID, "user_id": userID})
		return
	}
	code, expiresAt, err := h.groupAdmin.CreateChallenge(groupID, userID)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.group_admin.challenge", err, groupID, nil)
		return
	}
	message := fmt.Sprintf("Diana 群管理验证码：%s。10 分钟内有效，请勿转发。群：%s", code, groupID)
	if err := h.sendPrivateMessage(c.Request.Context(), userID, message); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_admin.challenge", err, groupID, map[string]any{"group_id": groupID, "user_id": userID})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.group_admin.challenge", "群管理员验证码已发送", groupID, map[string]any{"group_id": groupID, "user_id": userID})
	c.JSON(http.StatusOK, groupAdminChallengeResponse{
		GroupID:   groupID,
		UserID:    userID,
		ExpiresAt: expiresAt,
		Message:   "验证码已通过 私聊发送",
	})
}

func (h *BotHandler) verifyGroupAdminChallenge(c *gin.Context) {
	var payload groupAdminVerifyPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_admin.verify", err, "", nil)
		return
	}
	groupID, userID, err := normalizeGroupAdminIdentity(payload.GroupID, payload.UserID)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_admin.verify", err, groupID, nil)
		return
	}
	token, expiresAt, err := h.groupAdmin.Verify(groupID, userID, payload.Code)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_admin.verify", err, groupID, map[string]any{"group_id": groupID, "user_id": userID})
		return
	}
	config := h.groupConfigForResponse(groupID)
	recordRequestOperation(c, h.logs, "assistant.group_admin.verify", "群管理员验证通过", groupID, map[string]any{"group_id": groupID, "user_id": userID})
	c.JSON(http.StatusOK, groupAdminConfigResponse{
		GroupID:   groupID,
		UserID:    userID,
		Token:     token,
		ExpiresAt: expiresAt,
		Config:    config,
		Plugins:   assistant.RedactStates(h.runtime.Plugins().ListVisibleForProfile(h.runtime.Config().ID)),
	})
}

func (h *BotHandler) getGroupAdminConfig(c *gin.Context) {
	session, ok := h.groupAdminSessionFromRequest(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "assistant.group_admin.config", fmt.Errorf("群管理登录已过期，请重新验证"), "", nil)
		return
	}
	c.JSON(http.StatusOK, groupAdminConfigResponse{
		GroupID:   session.groupID,
		UserID:    session.userID,
		ExpiresAt: session.expiresAt,
		Config:    h.groupConfigForResponse(session.groupID),
		Plugins:   assistant.RedactStates(h.runtime.Plugins().ListVisibleForProfile(h.runtime.Config().ID)),
	})
}

func (h *BotHandler) saveGroupAdminConfig(c *gin.Context) {
	session, ok := h.groupAdminSessionFromRequest(c)
	if !ok {
		h.writeError(c, http.StatusUnauthorized, "assistant.group_admin.config.save", fmt.Errorf("群管理登录已过期，请重新验证"), "", nil)
		return
	}
	var payload groupAdminSessionPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_admin.config.save", err, session.groupID, nil)
		return
	}
	cfg, err := h.sanitizeGroupConfigPayload(payload.Config, session.groupID)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_admin.config.save", err, session.groupID, map[string]any{"group_id": session.groupID})
		return
	}
	saved, err := h.groupConfigs.SaveGroupConfig(cfg, h.runtime.Config())
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_admin.config.save", err, session.groupID, map[string]any{"group_id": session.groupID})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.group_admin.config.save", "群级机器人配置已保存", session.groupID, map[string]any{"group_id": session.groupID, "user_id": session.userID})
	c.JSON(http.StatusOK, groupAdminConfigResponse{
		GroupID:   session.groupID,
		UserID:    session.userID,
		ExpiresAt: session.expiresAt,
		Config:    h.groupConfigForAPI(saved.WithDefaults(session.groupID, h.runtime.Config())),
		Plugins:   assistant.RedactStates(h.runtime.Plugins().ListVisibleForProfile(h.runtime.Config().ID)),
	})
}

func (h *BotHandler) groupAdminSessionFromRequest(c *gin.Context) (groupAdminSession, bool) {
	token := c.GetHeader("X-Diana-Group-Token")
	if token == "" {
		token = c.Query("token")
	}
	return h.groupAdmin.Session(token)
}

func (h *BotHandler) groupConfigForResponse(groupID string) assistant.GroupConfig {
	// 群管理员自助的会话里还没有机器人身份，先保持改造前的行为。
	if cfg, ok := h.groupConfigs.ConfigForGroupAnyProfile(groupID); ok {
		return h.groupConfigForAPI(cfg.WithDefaults(groupID, h.runtime.Config()))
	}
	return h.groupConfigForAPI(assistant.DefaultGroupConfig(groupID, h.runtime.Config()))
}

func (h *BotHandler) groupConfigForAPI(cfg assistant.GroupConfig) assistant.GroupConfig {
	if plugins := h.runtime.Plugins(); plugins != nil {
		cfg.PluginSettingOverrides = plugins.SanitizeGroupSettingOverrides(cfg.PluginSettingOverrides)
	} else {
		cfg.PluginSettingOverrides = nil
	}
	return cfg
}

func (h *BotHandler) requireGroupAdmin(ctx context.Context, groupID string, userID string) error {
	group, err := strconv.ParseInt(groupID, 10, 64)
	if err != nil {
		return fmt.Errorf("群号格式不正确")
	}
	user, err := strconv.ParseInt(userID, 10, 64)
	if err != nil {
		return fmt.Errorf("账号格式不正确")
	}
	data, err := h.runtime.CallOneBotAPI(ctx, "get_group_member_info", map[string]any{
		"group_id": group,
		"user_id":  user,
		"no_cache": true,
	})
	if err != nil {
		return fmt.Errorf("无法校验群管理员身份：%w", err)
	}
	if !assistant.GroupRoleCanConfigure(assistant.NormalizeGroupRole(fmt.Sprint(data["role"]))) {
		return fmt.Errorf("只有群主或管理员可以配置本群")
	}
	return nil
}

func (h *BotHandler) sendPrivateMessage(ctx context.Context, userID string, text string) error {
	parsed, err := strconv.ParseInt(userID, 10, 64)
	if err != nil {
		return fmt.Errorf("账号格式不正确")
	}
	_, err = h.runtime.CallOneBotAPI(ctx, "send_private_msg", map[string]any{
		"user_id": parsed,
		"message": []map[string]any{
			{"type": "text", "data": map[string]string{"text": text}},
		},
	})
	return err
}

func normalizeGroupAdminIdentity(groupID string, userID string) (string, string, error) {
	groupID = strings.TrimSpace(groupID)
	userID = strings.TrimSpace(userID)
	if groupID == "" {
		return groupID, userID, fmt.Errorf("群号不能为空")
	}
	if userID == "" {
		return groupID, userID, fmt.Errorf("账号不能为空")
	}
	if _, err := strconv.ParseInt(groupID, 10, 64); err != nil {
		return groupID, userID, fmt.Errorf("群号格式不正确")
	}
	if _, err := strconv.ParseInt(userID, 10, 64); err != nil {
		return groupID, userID, fmt.Errorf("账号格式不正确")
	}
	return groupID, userID, nil
}

func (h *BotHandler) sanitizeGroupConfigPayload(cfg assistant.GroupConfig, groupID string) (assistant.GroupConfig, error) {
	// Older clients do not know this field. Omission preserves existing values;
	// an explicit empty object from a current client resets all group overrides.
	if cfg.PluginSettingOverrides == nil {
		if existing, ok := h.groupConfigs.ConfigForGroupAnyProfile(groupID); ok {
			cfg.PluginSettingOverrides = existing.PluginSettingOverrides
		}
	}
	cfg.GroupID = strings.TrimSpace(groupID)
	cfg.GroupTriggers = trimStringSlice(cfg.GroupTriggers)
	cfg.WelcomeMessage = strings.TrimSpace(cfg.WelcomeMessage)
	if cfg.PluginOverrides == nil {
		cfg.PluginOverrides = map[string]bool{}
	}
	for id := range cfg.PluginOverrides {
		if strings.TrimSpace(id) == "" {
			delete(cfg.PluginOverrides, id)
		}
	}
	plugins := h.runtime.Plugins()
	normalized, err := plugins.ValidateGroupSettingOverrides(cfg.PluginSettingOverrides)
	if err != nil {
		return assistant.GroupConfig{}, err
	}
	cfg.PluginSettingOverrides = normalized
	return cfg, nil
}

func trimStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func groupAdminKey(groupID string, userID string) string {
	return strings.TrimSpace(groupID) + ":" + strings.TrimSpace(userID)
}

func randomDigits(length int) (string, error) {
	var builder strings.Builder
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		builder.WriteString(n.String())
	}
	return builder.String(), nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
