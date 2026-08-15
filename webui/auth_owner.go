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
	ownerChallengeSendTimeout  = 10 * time.Second
)

var (
	ownerPairingCodePattern   = regexp.MustCompile(`^(?:登录(?:控制台)?\s*)?(\d{6})$`)
	ownerChallengeCodePattern = regexp.MustCompile(`^\d{6}$`)
)

// OwnerLoginRuntime 是管理员验证码与私聊确认登录依赖的机器人运行时能力。
type OwnerLoginRuntime interface {
	Config() assistant.BotConfig
	CallOneBotAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error)
}

type ownerPairing struct {
	tokenHash string
	codeHash  string
	requestIP string
	expiresAt time.Time
	approved  bool
}

type ownerChallenge struct {
	tokenHash string
	codeHash  string
	requestIP string
	ownerKey  string
	expiresAt time.Time
	attempts  int
}

// OwnerLoginHandler 提供验证码下发和主人私聊确认两种一次性登录方式。
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

func (h *OwnerLoginHandler) availability() error {
	if h.auth == nil || h.runtime == nil || !h.auth.Required() {
		return errors.New("未开启密码保护，无需管理员快速登录")
	}
	cfg := h.runtime.Config()
	if !cfg.OwnerLoginEnabled {
		return errors.New("管理员快速登录未开启")
	}
	if strings.TrimSpace(cfg.OwnerID) == "" {
		return errors.New("未配置主人账号")
	}
	return nil
}

func (h *OwnerLoginHandler) status(c *gin.Context) {
	available := h.availability() == nil
	challengeAvailable := false
	if available {
		_, _, challengeErr := ownerChallengeDelivery(h.runtime.Config(), "")
		challengeAvailable = challengeErr == nil
	}
	c.JSON(http.StatusOK, gin.H{
		"available":               available,
		"code_delivery_available": challengeAvailable,
	})
}

// createChallenge sends a one-time code to the configured administrator. The
// browser receives only a high-entropy token that binds verification to this
// login attempt; the six-digit code is delivered through the chat platform.
func (h *OwnerLoginHandler) createChallenge(c *gin.Context) {
	if err := h.availability(); err != nil {
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
	cfg := h.runtime.Config()
	name := firstNonEmptyWebUI(cfg.Name, "Diana")
	message := fmt.Sprintf("%s 管理员登录验证码：%s，%d 分钟内有效。若非本人操作请忽略。", name, code, int(ownerChallengeTTL.Minutes()))
	action, params, err := ownerChallengeDelivery(cfg, message)
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
	h.challengeSending = true
	h.lastChallengeSentAt = now
	h.mu.Unlock()

	sendCtx, sendCancel := context.WithTimeout(c.Request.Context(), ownerChallengeSendTimeout)
	_, sendErr := h.runtime.CallOneBotAPI(sendCtx, action, params)
	sendCancel()
	h.mu.Lock()
	h.challengeSending = false
	if sendErr == nil {
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
		logAndWriteError(c, h.logs, http.StatusBadGateway, "auth.owner.challenge",
			fmt.Errorf("验证码发送失败（机器人是否在线？）：%w", sendErr), cfg.OwnerID, nil)
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
	if err := h.availability(); err != nil {
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
	currentOwnerKey := ownerChallengeKey(h.runtime.Config())

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
	if err := h.availability(); err != nil {
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
	requestIP := c.ClientIP()
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
		tokenHash: tokenHash,
		codeHash:  codeHash,
		requestIP: requestIP,
		expiresAt: now.Add(ownerPairingTTL),
	}
	h.codeIndex[codeHash] = tokenHash
	h.lastCreated[requestIP] = now
	h.mu.Unlock()

	recordRequestOperation(c, h.logs, "auth.owner.pair.create", "已创建主人 QQ 登录配对", "", nil)
	c.JSON(http.StatusOK, gin.H{
		"ok":                 true,
		"code":               code,
		"poll_token":         token,
		"expires_in_seconds": int(ownerPairingTTL.Seconds()),
	})
}

func (h *OwnerLoginHandler) pollPairing(c *gin.Context) {
	if err := h.availability(); err != nil {
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
	recordRequestOperation(c, h.logs, "auth.owner.pair.login", "主人 QQ 配对登录成功", "", nil)
	c.JSON(http.StatusOK, gin.H{"approved": true})
}

// ConsumePrivateMessage 仅消费主人私聊发送的、仍在有效期内的登录验证码。
func (h *OwnerLoginHandler) ConsumePrivateMessage(ctx context.Context, event assistant.MessageEvent, text string) bool {
	if event.Kind != assistant.EventKindPrivate || h.availability() != nil {
		return false
	}
	cfg := h.runtime.Config()
	if strings.TrimSpace(event.UserID) != strings.TrimSpace(cfg.OwnerID) {
		return false
	}
	match := ownerPairingCodePattern.FindStringSubmatch(strings.TrimSpace(text))
	if len(match) != 2 {
		return false
	}

	now := time.Now()
	codeHash := hashOwnerCode(match[1])
	h.mu.Lock()
	h.cleanupLocked(now)
	tokenHash := h.codeIndex[codeHash]
	pairing := h.pairings[tokenHash]
	if pairing == nil || pairing.approved {
		h.mu.Unlock()
		return false
	}
	pairing.approved = true
	delete(h.codeIndex, codeHash)
	pairing.codeHash = ""
	h.mu.Unlock()

	recordOperation(ctx, h.logs, "auth.owner.pair.approve", "主人已通过 QQ 私聊确认登录", event.UserID, nil)
	return true
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

func ownerChallengeDelivery(cfg assistant.BotConfig, message string) (string, map[string]any, error) {
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
	return "", nil, fmt.Errorf("当前平台 %q 不支持验证码下发", platform)
}

func ownerChallengeKey(cfg assistant.BotConfig) string {
	return assistant.NormalizePlatformID(cfg.Platform) + ":" + strings.TrimSpace(cfg.OwnerID)
}
