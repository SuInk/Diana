package webui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
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
)

var ownerPairingCodePattern = regexp.MustCompile(`^(?:登录(?:控制台)?\s*)?(\d{6})$`)

// OwnerLoginRuntime 是主人 QQ 配对登录依赖的机器人运行时能力。
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

// OwnerLoginHandler 提供浏览器发起、主人 QQ 私聊确认的一次性配对登录。
type OwnerLoginHandler struct {
	auth    *AuthManager
	runtime OwnerLoginRuntime
	logs    AppLogWriter

	mu          sync.Mutex
	pairings    map[string]*ownerPairing
	codeIndex   map[string]string
	lastCreated map[string]time.Time
}

// NewOwnerLoginHandler 创建主人 QQ 配对登录处理器。
func NewOwnerLoginHandler(auth *AuthManager, runtime OwnerLoginRuntime) *OwnerLoginHandler {
	return &OwnerLoginHandler{
		auth:        auth,
		runtime:     runtime,
		pairings:    make(map[string]*ownerPairing),
		codeIndex:   make(map[string]string),
		lastCreated: make(map[string]time.Time),
	}
}

// SetLogStore 注入操作日志写入器。
func (h *OwnerLoginHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// Register 注册主人 QQ 配对登录路由。
func (h *OwnerLoginHandler) Register(router gin.IRouter) {
	router.GET("/api/auth/owner/status", h.status)
	router.POST("/api/auth/owner/pair", h.createPairing)
	router.POST("/api/auth/owner/pair/status", h.pollPairing)
}

func (h *OwnerLoginHandler) availability() error {
	if h.auth == nil || !h.auth.Required() {
		return errors.New("未开启密码保护，无需 QQ 配对登录")
	}
	cfg := h.runtime.Config()
	if !cfg.OwnerLoginEnabled {
		return errors.New("主人 QQ 登录未开启")
	}
	if strings.TrimSpace(cfg.OwnerID) == "" {
		return errors.New("未配置主人 QQ 号")
	}
	return nil
}

func (h *OwnerLoginHandler) status(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"available": h.availability() == nil})
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

	token, err := h.auth.IssueSession()
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
