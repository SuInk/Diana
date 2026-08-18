// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

const (
	ownerPairingTTL            = 5 * time.Minute
	ownerPairingCreateDelay    = 3 * time.Second
	ownerPairingMaxActive      = 32
	ownerPairingMaxActivePerIP = 3
	ownerChallengeTTL          = 5 * time.Minute
	ownerChallengeCooldown     = 60 * time.Second
	ownerChallengeMaxAttempts  = 5
	ownerMessageSendTimeout    = 10 * time.Second
)

var (
	ownerPairingCodePattern   = regexp.MustCompile(`^(?:登录(?:控制台)?\s*)?(\d{6})$`)
	ownerChallengeCodePattern = regexp.MustCompile(`^\d{6}$`)
)

// OwnerLoginRuntime 是管理员快速登录依赖的机器人运行时能力。
type OwnerLoginRuntime interface {
	Config() assistant.BotConfig
	CallOneBotAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error)
}

type ownerPairing struct {
	tokenHash  string
	codeHash   string
	requestIP  string
	deviceName string
	expiresAt  time.Time
	approved   bool
}

type ownerChallenge struct {
	tokenHash string
	codeHash  string
	requestIP string
	ownerKey  string
	expiresAt time.Time
	attempts  int
}

// OwnerLoginHandler 提供主人私聊确认与服务端下发验证码两种一次性登录方式，
// 两者各自独立开关：私聊确认默认开启，验证码下发默认关闭。
type OwnerLoginHandler struct {
	auth    *AuthManager
	runtime OwnerLoginRuntime
	logs    AppLogWriter

	mu          sync.Mutex
	pairings    map[string]*ownerPairing
	codeIndex   map[string]string
	lastCreated map[string]time.Time

	challenges          map[string]*ownerChallenge
	lastChallengeSentAt time.Time
	challengeSending    bool
}

// NewOwnerLoginHandler 创建管理员快速登录处理器。
func NewOwnerLoginHandler(auth *AuthManager, runtime OwnerLoginRuntime) *OwnerLoginHandler {
	return &OwnerLoginHandler{
		auth:        auth,
		runtime:     runtime,
		pairings:    make(map[string]*ownerPairing),
		codeIndex:   make(map[string]string),
		lastCreated: make(map[string]time.Time),
		challenges:  make(map[string]*ownerChallenge),
	}
}

// SetLogStore 注入操作日志写入器。
func (h *OwnerLoginHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// Register 注册管理员快速登录路由。
func (h *OwnerLoginHandler) Register(router gin.IRouter) {
	router.GET("/api/auth/owner/status", h.status)
	router.POST("/api/auth/owner/challenge", h.createChallenge)
	router.POST("/api/auth/owner/verify", h.verifyChallenge)
	router.POST("/api/auth/owner/pair", h.createPairing)
	router.POST("/api/auth/owner/pair/status", h.pollPairing)
}

// baseAvailability 校验两种方式共同的前提：开启了密码保护、配置了主人账号、
// 且当前平台能把消息投递给主人。
func (h *OwnerLoginHandler) baseAvailability() (assistant.BotConfig, error) {
	if h.auth == nil || h.runtime == nil || !h.auth.Required() {
		return assistant.BotConfig{}, errors.New("未开启密码保护，无需管理员快速登录")
	}
	cfg := h.runtime.Config()
	if !cfg.OwnerLoginEnabled {
		return cfg, errors.New("管理员快速登录未开启")
	}
	if strings.TrimSpace(cfg.OwnerID) == "" {
		return cfg, errors.New("未配置主人账号")
	}
	if _, _, err := ownerMessageDelivery(cfg, ""); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (h *OwnerLoginHandler) pairAvailability() (assistant.BotConfig, error) {
	cfg, err := h.baseAvailability()
	if err != nil {
		return cfg, err
	}
	if !cfg.OwnerLoginPairAllowed() {
		return cfg, errors.New("主人私聊确认登录未开启")
	}
	return cfg, nil
}

func (h *OwnerLoginHandler) codeAvailability() (assistant.BotConfig, error) {
	cfg, err := h.baseAvailability()
	if err != nil {
		return cfg, err
	}
	if !cfg.OwnerLoginCodeAllowed() {
		return cfg, errors.New("验证码下发登录未开启")
	}
	return cfg, nil
}

func (h *OwnerLoginHandler) status(c *gin.Context) {
	_, pairErr := h.pairAvailability()
	_, codeErr := h.codeAvailability()
	c.JSON(http.StatusOK, gin.H{
		"available":      pairErr == nil || codeErr == nil,
		"pair_available": pairErr == nil,
		"code_available": codeErr == nil,
	})
}

// createChallenge sends a one-time code to the configured administrator. The
// browser receives only a high-entropy token that binds verification to this
// login attempt; the six-digit code is delivered through the chat platform.
//
// 这个端点无需任何凭证即可触发服务端主动发消息，因此默认关闭：开启后任何匿名
// 请求都能让主人收到私聊，并且能靠占满冷却窗口把主人本人挡在门外。
func (h *OwnerLoginHandler) createChallenge(c *gin.Context) {
	cfg, err := h.codeAvailability()
	if err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	code, err := randomOwnerCode()
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	token, err := randomOwnerPairingToken()
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	name := firstNonEmptyWebUI(cfg.Name, "Diana")
	message := fmt.Sprintf("%s 管理员登录验证码：%s，%d 分钟内有效。若非本人操作请忽略。", name, code, int(ownerChallengeTTL.Minutes()))
	action, params, err := ownerMessageDelivery(cfg, message)
	if err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}

	now := time.Now()
	requestIP := c.ClientIP()
	h.mu.Lock()
	h.cleanupLocked(now)
	if h.challengeSending {
		h.mu.Unlock()
		writeError(c, http.StatusTooManyRequests, errors.New("验证码正在发送，请稍后再试"))
		return
	}
	if since := now.Sub(h.lastChallengeSentAt); since < ownerChallengeCooldown {
		wait := int((ownerChallengeCooldown - since).Seconds()) + 1
		h.mu.Unlock()
		c.Header("Retry-After", strconv.Itoa(wait))
		writeError(c, http.StatusTooManyRequests, fmt.Errorf("发送过于频繁，请 %d 秒后再试", wait))
		return
	}
	previousSentAt := h.lastChallengeSentAt
	h.challengeSending = true
	h.lastChallengeSentAt = now
	h.mu.Unlock()

	sendCtx, sendCancel := context.WithTimeout(c.Request.Context(), ownerMessageSendTimeout)
	_, sendErr := h.runtime.CallOneBotAPI(sendCtx, action, params)
	sendCancel()
	h.mu.Lock()
	h.challengeSending = false
	if sendErr != nil {
		// 没发出去就不该占用冷却窗口，否则机器人离线期间的失败请求会一路把
		// 主人自己的重试挡在 429 上。
		h.lastChallengeSentAt = previousSentAt
	} else {
		for tokenHash, challenge := range h.challenges {
			if challenge.requestIP == requestIP {
				delete(h.challenges, tokenHash)
			}
		}
		tokenHash := hashOwnerPairingToken(token)
		h.challenges[tokenHash] = &ownerChallenge{
			tokenHash: tokenHash,
			codeHash:  hashOwnerCode(code),
			requestIP: requestIP,
			ownerKey:  ownerChallengeKey(cfg),
			expiresAt: now.Add(ownerChallengeTTL),
		}
	}
	h.mu.Unlock()
	if sendErr != nil {
		logAndWriteMaskedError(c, h.logs, http.StatusBadGateway, "auth.owner.challenge",
			sendErr, "验证码发送失败，请确认机器人在线后重试", cfg.OwnerID, nil)
		return
	}

	recordRequestOperation(c, h.logs, "auth.owner.challenge", "管理员登录验证码已发送", cfg.OwnerID, nil)
	c.JSON(http.StatusOK, gin.H{
		"ok":                 true,
		"challenge_token":    token,
		"expires_in_seconds": int(ownerChallengeTTL.Seconds()),
		"cooldown_seconds":   int(ownerChallengeCooldown.Seconds()),
	})
}

func (h *OwnerLoginHandler) verifyChallenge(c *gin.Context) {
	cfg, err := h.codeAvailability()
	if err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	var payload struct {
		ChallengeToken string `json:"challenge_token"`
		Code           string `json:"code"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	code := strings.TrimSpace(payload.Code)
	tokenHash := hashOwnerPairingToken(strings.TrimSpace(payload.ChallengeToken))
	now := time.Now()
	currentOwnerKey := ownerChallengeKey(cfg)

	h.mu.Lock()
	h.cleanupLocked(now)
	challenge := h.challenges[tokenHash]
	valid := challenge != nil &&
		challenge.attempts < ownerChallengeMaxAttempts &&
		challenge.ownerKey == currentOwnerKey &&
		ownerChallengeCodePattern.MatchString(code)
	match := valid && subtle.ConstantTimeCompare([]byte(hashOwnerCode(code)), []byte(challenge.codeHash)) == 1
	if challenge != nil {
		if match {
			delete(h.challenges, tokenHash)
		} else {
			challenge.attempts++
			if challenge.attempts >= ownerChallengeMaxAttempts {
				delete(h.challenges, tokenHash)
			}
		}
	}
	h.mu.Unlock()

	if !match {
		time.Sleep(400 * time.Millisecond)
		logAndWriteError(c, h.logs, http.StatusUnauthorized, "auth.owner.verify", errors.New("验证码错误或已失效"), "", nil)
		return
	}
	metadata := authSessionMetadata(c)
	metadata.DeviceName = "管理员验证码"
	token, err := h.auth.IssueSessionWithMetadata(metadata)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	authSetSessionCookie(c, token, int(authSessionTTL/time.Second))
	recordRequestOperation(c, h.logs, "auth.owner.verify", "管理员验证码登录成功", "", nil)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *OwnerLoginHandler) createPairing(c *gin.Context) {
	if _, err := h.pairAvailability(); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}

	code, err := randomOwnerCode()
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	token, err := randomOwnerPairingToken()
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}

	now := time.Now()
	metadata := authSessionMetadata(c)
	requestIP := metadata.IPAddress
	tokenHash := hashOwnerPairingToken(token)
	codeHash := hashOwnerCode(code)

	h.mu.Lock()
	h.cleanupLocked(now)
	if since := now.Sub(h.lastCreated[requestIP]); since < ownerPairingCreateDelay {
		h.mu.Unlock()
		wait := int((ownerPairingCreateDelay - since).Seconds()) + 1
		writeError(c, http.StatusTooManyRequests, fmt.Errorf("请求过于频繁，请 %d 秒后再试", wait))
		return
	}
	if len(h.pairings) >= ownerPairingMaxActive || h.activeForIPLocked(requestIP) >= ownerPairingMaxActivePerIP {
		h.mu.Unlock()
		writeError(c, http.StatusTooManyRequests, errors.New("待确认登录过多，请稍后再试"))
		return
	}
	for h.codeIndex[codeHash] != "" {
		code, err = randomOwnerCode()
		if err != nil {
			h.mu.Unlock()
			writeError(c, http.StatusInternalServerError, err)
			return
		}
		codeHash = hashOwnerCode(code)
	}
	h.pairings[tokenHash] = &ownerPairing{
		tokenHash:  tokenHash,
		codeHash:   codeHash,
		requestIP:  requestIP,
		deviceName: metadata.DeviceName,
		expiresAt:  now.Add(ownerPairingTTL),
	}
	h.codeIndex[codeHash] = tokenHash
	h.lastCreated[requestIP] = now
	h.mu.Unlock()

	recordRequestOperation(c, h.logs, "auth.owner.pair.create", "已创建主人私聊登录配对", "", nil)
	c.JSON(http.StatusOK, gin.H{
		"ok":                 true,
		"code":               code,
		"poll_token":         token,
		"expires_in_seconds": int(ownerPairingTTL.Seconds()),
	})
}

func (h *OwnerLoginHandler) pollPairing(c *gin.Context) {
	if _, err := h.pairAvailability(); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	var payload struct {
		PollToken string `json:"poll_token"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	tokenHash := hashOwnerPairingToken(strings.TrimSpace(payload.PollToken))
	now := time.Now()

	h.mu.Lock()
	h.cleanupLocked(now)
	pairing := h.pairings[tokenHash]
	if pairing == nil {
		h.mu.Unlock()
		c.JSON(http.StatusOK, gin.H{"approved": false, "expired": true})
		return
	}
	if !pairing.approved {
		remaining := max(0, int(time.Until(pairing.expiresAt).Seconds()))
		h.mu.Unlock()
		c.JSON(http.StatusOK, gin.H{
			"approved":           false,
			"expires_in_seconds": remaining,
		})
		return
	}
	h.deletePairingLocked(pairing)
	h.mu.Unlock()

	metadata := authSessionMetadata(c)
	metadata.DeviceName = "主人私聊确认"
	token, err := h.auth.IssueSessionWithMetadata(metadata)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	authSetSessionCookie(c, token, int(authSessionTTL/time.Second))
	recordRequestOperation(c, h.logs, "auth.owner.pair.login", "主人私聊确认登录成功", "", nil)
	c.JSON(http.StatusOK, gin.H{"approved": true})
}

// ConsumePrivateMessage 消费主人私聊发来的登录验证码：验证码一到就放行，并回一条
// 带来源信息的回执。返回 true 表示这条消息属于登录流程、不应再交给对话逻辑。
func (h *OwnerLoginHandler) ConsumePrivateMessage(ctx context.Context, event assistant.MessageEvent, text string) bool {
	if event.Kind != assistant.EventKindPrivate {
		return false
	}
	cfg, err := h.pairAvailability()
	if err != nil {
		return false
	}
	if strings.TrimSpace(event.UserID) != strings.TrimSpace(cfg.OwnerID) {
		return false
	}
	match := ownerPairingCodePattern.FindStringSubmatch(strings.TrimSpace(text))
	if len(match) != 2 {
		return false
	}

	now := time.Now()
	h.mu.Lock()
	h.cleanupLocked(now)
	pairing := h.pairings[h.codeIndex[hashOwnerCode(match[1])]]
	if pairing == nil || pairing.approved {
		h.mu.Unlock()
		// 不是登录码就交回对话逻辑，免得把主人正常聊天里的数字吞掉。
		return false
	}
	pairing.approved = true
	delete(h.codeIndex, pairing.codeHash)
	pairing.codeHash = ""
	requestIP := firstNonEmptyWebUI(pairing.requestIP, "未知")
	deviceName := firstNonEmptyWebUI(pairing.deviceName, "未知设备")
	h.mu.Unlock()

	// 回执不是确认环节，主人不用做任何动作。它存在只是为了别让这条消息被静默
	// 吞掉——万一验证码是被诱导转发的，主人当场就能发现并去踢掉会话。
	receipt := fmt.Sprintf("已登录控制台\n来源 IP：%s\n设备：%s\n\n若非本人操作，请立刻在控制台踢掉该会话并修改密码。", requestIP, deviceName)
	if err := h.notifyOwner(ctx, cfg, receipt); err != nil {
		recordOperation(ctx, h.logs, "auth.owner.pair.notify", "登录回执发送失败："+err.Error(), event.UserID, nil)
	}
	recordOperation(ctx, h.logs, "auth.owner.pair.approve", "主人已通过私聊确认控制台登录", event.UserID, nil)
	return true
}

// notifyOwner 只在主人自己先私聊过来之后才会被调用，因此不存在匿名请求触发
// 消息推送的路径。
func (h *OwnerLoginHandler) notifyOwner(ctx context.Context, cfg assistant.BotConfig, message string) error {
	action, params, err := ownerMessageDelivery(cfg, message)
	if err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, ownerMessageSendTimeout)
	defer cancel()
	_, err = h.runtime.CallOneBotAPI(sendCtx, action, params)
	return err
}

func (h *OwnerLoginHandler) cleanupLocked(now time.Time) {
	for _, pairing := range h.pairings {
		if !now.Before(pairing.expiresAt) {
			h.deletePairingLocked(pairing)
		}
	}
	for ip, createdAt := range h.lastCreated {
		if now.Sub(createdAt) >= ownerPairingTTL {
			delete(h.lastCreated, ip)
		}
	}
	for tokenHash, challenge := range h.challenges {
		if !now.Before(challenge.expiresAt) || challenge.attempts >= ownerChallengeMaxAttempts {
			delete(h.challenges, tokenHash)
		}
	}
}

func (h *OwnerLoginHandler) deletePairingLocked(pairing *ownerPairing) {
	delete(h.pairings, pairing.tokenHash)
	if pairing.codeHash != "" {
		delete(h.codeIndex, pairing.codeHash)
	}
}

func (h *OwnerLoginHandler) activeForIPLocked(requestIP string) int {
	count := 0
	for _, pairing := range h.pairings {
		if pairing.requestIP == requestIP {
			count++
		}
	}
	return count
}

func randomOwnerCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func randomOwnerPairingToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func hashOwnerCode(code string) string {
	sum := sha256.Sum256([]byte("owner-login-code:" + code))
	return hex.EncodeToString(sum[:])
}

func hashOwnerPairingToken(token string) string {
	sum := sha256.Sum256([]byte("owner-login-token:" + token))
	return hex.EncodeToString(sum[:])
}

// ownerMessageDelivery 把一条发给主人的私聊消息翻译成当前平台的 API 调用。
// 传空消息可用于探测当前配置是否具备投递能力。
func ownerMessageDelivery(cfg assistant.BotConfig, message string) (string, map[string]any, error) {
	ownerID := strings.TrimSpace(cfg.OwnerID)
	parsedOwnerID, err := strconv.ParseInt(ownerID, 10, 64)
	if err != nil {
		return "", nil, errors.New("管理员账号格式不正确")
	}
	platform := assistant.NormalizePlatformID(cfg.Platform)
	if platform == assistant.PlatformTelegram {
		return "sendMessage", map[string]any{
			"chat_id": parsedOwnerID,
			"text":    message,
		}, nil
	}
	if assistant.IsOneBotPlatform(platform) {
		return "send_private_msg", map[string]any{
			"user_id": parsedOwnerID,
			"message": []map[string]any{
				{"type": "text", "data": map[string]string{"text": message}},
			},
		}, nil
	}
	return "", nil, fmt.Errorf("当前平台 %q 不支持向主人投递消息", platform)
}

func ownerChallengeKey(cfg assistant.BotConfig) string {
	return assistant.NormalizePlatformID(cfg.Platform) + ":" + strings.TrimSpace(cfg.OwnerID)
}
