// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
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
	ownerMessageSendTimeout    = 10 * time.Second
)

var ownerPairingCodePattern = regexp.MustCompile(`^(?:登录(?:控制台)?\s*)?(\d{6})$`)

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

// OwnerLoginHandler 提供主人私聊确认登录：网页显示一次性验证码，主人私聊发给
// 机器人即完成确认。服务端不会主动发出任何消息，因此没有匿名请求能触发的骚扰面。
type OwnerLoginHandler struct {
	auth     *AuthManager
	runtime  OwnerLoginRuntime
	logs     AppLogWriter
	throttle *authThrottle

	mu          sync.Mutex
	pairings    map[string]*ownerPairing
	codeIndex   map[string]string
	lastCreated map[string]time.Time
}

// NewOwnerLoginHandler 创建管理员快速登录处理器。
func NewOwnerLoginHandler(auth *AuthManager, runtime OwnerLoginRuntime) *OwnerLoginHandler {
	return &OwnerLoginHandler{
		auth:        auth,
		runtime:     runtime,
		throttle:    newAuthThrottle(),
		pairings:    make(map[string]*ownerPairing),
		codeIndex:   make(map[string]string),
		lastCreated: make(map[string]time.Time),
	}
}

// SetLogStore 注入操作日志写入器。
func (h *OwnerLoginHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// Register 注册管理员快速登录路由。
func (h *OwnerLoginHandler) Register(router gin.IRouter) {
	router.GET("/api/auth/owner/status", h.status)
	router.POST("/api/auth/owner/pair", h.createPairing)
	router.POST("/api/auth/owner/pair/status", h.pollPairing)
	router.POST("/api/auth/owner/pair/claim", h.claimPairing)
}

// availability 校验前提：开启了密码保护、配置了主人账号，且当前平台能把回执
// 投递给主人。
func (h *OwnerLoginHandler) availability() (assistant.BotConfig, error) {
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

func (h *OwnerLoginHandler) status(c *gin.Context) {
	_, err := h.availability()
	c.JSON(http.StatusOK, gin.H{"available": err == nil})
}

func (h *OwnerLoginHandler) createPairing(c *gin.Context) {
	if _, err := h.availability(); err != nil {
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
	if _, err := h.availability(); err != nil {
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

	if !h.issueOwnerSession(c, "auth.owner.pair.login", "主人私聊确认登录成功") {
		return
	}
	c.JSON(http.StatusOK, gin.H{"approved": true})
}

// claimPairing 用验证码直接兑换会话，兜住网页没能自动跳转的情况：轮询被网络
// 掐断、页面被手机浏览器回收、或者干脆换了个标签页打开。主人私聊发出去的那条
// 消息里就有验证码，从聊天记录抄回来填即可，不必重走一遍流程。
func (h *OwnerLoginHandler) claimPairing(c *gin.Context) {
	if _, err := h.availability(); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	var payload struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	// 验证码只有 6 位，这个端点必须限流：否则可以靠穷举抢走一个已确认、但还
	// 没被网页取走的配对。
	throttleKey := c.ClientIP()
	if wait := h.throttle.RetryAfter(throttleKey, time.Now()); wait > 0 {
		c.Header("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(c, http.StatusTooManyRequests, fmt.Errorf("尝试过于频繁，请 %s 后再试", formatRetryAfter(wait)))
		return
	}

	match := ownerPairingCodePattern.FindStringSubmatch(strings.TrimSpace(payload.Code))
	now := time.Now()
	var pairing *ownerPairing
	if len(match) == 2 {
		h.mu.Lock()
		h.cleanupLocked(now)
		if candidate := h.pairings[h.codeIndex[hashOwnerCode(match[1])]]; candidate != nil && candidate.approved {
			pairing = candidate
			h.deletePairingLocked(candidate)
		}
		h.mu.Unlock()
	}
	if pairing == nil {
		// 「没这个码」和「还没在私聊里确认」回同一句话，免得这个端点变成枚举
		// 验证码的探针。主人自己知道有没有发出去。
		time.Sleep(400 * time.Millisecond)
		h.throttle.Fail(throttleKey, time.Now())
		logAndWriteError(c, h.logs, http.StatusUnauthorized, "auth.owner.pair.claim",
			errors.New("验证码无效、已过期，或还没有在私聊里确认"), "", nil)
		return
	}
	h.throttle.Reset(throttleKey)
	if !h.issueOwnerSession(c, "auth.owner.pair.claim", "主人私聊确认登录成功（手动填写验证码）") {
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *OwnerLoginHandler) issueOwnerSession(c *gin.Context, action, message string) bool {
	metadata := authSessionMetadata(c)
	metadata.DeviceName = "主人私聊确认"
	token, err := h.auth.IssueSessionWithMetadata(metadata)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return false
	}
	authSetSessionCookie(c, token, int(authSessionTTL/time.Second))
	recordRequestOperation(c, h.logs, action, message, "", nil)
	return true
}

// ConsumePrivateMessage 消费主人私聊发来的登录验证码：验证码一到就确认，并回一条
// 带来源信息的回执。返回 true 表示这条消息属于登录流程、不应再交给对话逻辑。
func (h *OwnerLoginHandler) ConsumePrivateMessage(ctx context.Context, event assistant.MessageEvent, text string) bool {
	if event.Kind != assistant.EventKindPrivate {
		return false
	}
	cfg, err := h.availability()
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
	// 保留 codeIndex：网页没能自动跳转时，主人还要靠这个码手动兑换会话。
	requestIP := firstNonEmptyWebUI(pairing.requestIP, "未知")
	deviceName := firstNonEmptyWebUI(pairing.deviceName, "未知设备")
	h.mu.Unlock()

	// 回执不是确认环节，主人不用做任何动作。它存在只是为了别让这条消息被静默
	// 吞掉——万一验证码是被诱导转发的，主人当场就能发现并去踢掉会话。
	receipt := fmt.Sprintf("已确认登录\n来源 IP：%s\n设备：%s\n\n浏览器没有自动跳转的话，把这个验证码填进登录页即可。\n若非本人操作，请立刻在控制台踢掉该会话并修改密码。", requestIP, deviceName)
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
