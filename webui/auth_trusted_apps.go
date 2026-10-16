// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package webui

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

// TrustedLoginApp is a server-managed relying party. SecretSHA256 is the SHA-256
// digest of a random, at least 32-byte credential, never a browser credential.
type TrustedLoginApp struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SecretSHA256 string `json:"secret_sha256"`
	ProfileID    string `json:"profile_id"`
}

var trustedAppID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)
var trustedCommand = regexp.MustCompile(`^登录应用 ([a-z][a-z0-9_-]{0,47}) (\d{6})$`)

// LoadTrustedLoginApps leaves integration disabled when path is empty.
func LoadTrustedLoginApps(path string) ([]TrustedLoginApp, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	var apps []TrustedLoginApp
	if err = d.Decode(&apps); err != nil {
		return nil, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("trusted login config must contain one JSON array")
	}
	if err = validateTrustedApps(apps); err != nil {
		return nil, err
	}
	return apps, nil
}
func validateTrustedApps(apps []TrustedLoginApp) error {
	if len(apps) > 32 {
		return errors.New("too many trusted login apps")
	}
	seen := map[string]bool{}
	for _, a := range apps {
		digest, err := hex.DecodeString(a.SecretSHA256)
		if !trustedAppID.MatchString(a.ID) || seen[a.ID] || strings.TrimSpace(a.Name) == "" || len([]rune(a.Name)) > 64 || strings.ContainsAny(a.Name, "\r\n\t") || strings.TrimSpace(a.ProfileID) == "" || err != nil || len(digest) != 32 {
			return errors.New("invalid or duplicate trusted login app")
		}
		seen[a.ID] = true
	}
	return nil
}

type trustedPair struct {
	appID, codeHash string
	expires         time.Time
	identity        *trustedIdentity
}
type trustedIdentity struct {
	ProfileID   string    `json:"profile_id"`
	Platform    string    `json:"platform"`
	UserID      string    `json:"user_id"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

// TrustedLoginHandler keeps external pairings separate from console sessions.
// Only an authenticated backend holding the opaque polling token can consume one.
type TrustedLoginHandler struct {
	owner      *OwnerLoginHandler
	apps       map[string]TrustedLoginApp
	mu         sync.Mutex
	pairs      map[string]*trustedPair
	lastCreate map[string]time.Time
	lastPoll   map[string]time.Time
	throttle   *authThrottle
}

func NewTrustedLoginHandler(owner *OwnerLoginHandler, apps []TrustedLoginApp) (*TrustedLoginHandler, error) {
	if err := validateTrustedApps(apps); err != nil {
		return nil, err
	}
	h := &TrustedLoginHandler{owner: owner, apps: map[string]TrustedLoginApp{}, pairs: map[string]*trustedPair{}, lastCreate: map[string]time.Time{}, lastPoll: map[string]time.Time{}, throttle: newAuthThrottle()}
	for _, a := range apps {
		h.apps[a.ID] = a
	}
	return h, nil
}
func (h *TrustedLoginHandler) Register(r gin.IRouter) {
	r.POST("/api/auth/apps/pair", h.authenticate, h.create)
	r.POST("/api/auth/apps/pair/status", h.authenticate, h.poll)
}
func (h *TrustedLoginHandler) authenticate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	now := time.Now()
	key := c.ClientIP()
	if h.throttle.RetryAfter(key, now) > 0 {
		c.AbortWithStatusJSON(429, gin.H{"error": "请求过于频繁"})
		return
	}
	id, secret, ok := c.Request.BasicAuth()
	a, exists := h.apps[id]
	expected, _ := hex.DecodeString(a.SecretSHA256)
	actual := sha256.Sum256([]byte(secret))
	if !ok || !exists || len(secret) < 32 || subtle.ConstantTimeCompare(expected, actual[:]) != 1 {
		h.throttle.Fail(key, now)
		c.AbortWithStatusJSON(401, gin.H{"error": "应用凭证无效"})
		return
	}
	if h.owner == nil || h.owner.auth == nil || !h.owner.auth.Required() || h.owner.runtime == nil {
		c.AbortWithStatusJSON(503, gin.H{"error": "私聊登录不可用"})
		return
	}
	cfg := h.owner.runtime.ProfileConfig(a.ProfileID)
	if cfg.ID != a.ProfileID || ownerLoginEligible(cfg) != nil {
		c.AbortWithStatusJSON(503, gin.H{"error": "私聊登录不可用"})
		return
	}
	c.Set("trusted_login_app", a)
}
func (h *TrustedLoginHandler) cleanup(now time.Time) {
	for k, p := range h.pairs {
		if !now.Before(p.expires) {
			delete(h.pairs, k)
			delete(h.lastPoll, k)
		}
	}
}
func (h *TrustedLoginHandler) create(c *gin.Context) {
	a := c.MustGet("trusted_login_app").(TrustedLoginApp)
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	h.cleanup(now)
	count := 0
	for _, p := range h.pairs {
		if p.appID == a.ID {
			count++
		}
	}
	if now.Sub(h.lastCreate[a.ID]) < 3*time.Second || count >= 8 || len(h.pairs) >= 128 {
		c.Header("Retry-After", "3")
		c.JSON(429, gin.H{"error": "待确认登录过多或请求过快"})
		return
	}
	code, err := randomOwnerCode()
	if err != nil {
		c.Status(500)
		return
	}
	for {
		collision := false
		for _, p := range h.pairs {
			if p.appID == a.ID && p.codeHash == hashOwnerCode(code) {
				collision = true
				break
			}
		}
		if !collision {
			break
		}
		code, err = randomOwnerCode()
		if err != nil {
			c.Status(500)
			return
		}
	}
	token, err := randomOwnerPairingToken()
	if err != nil {
		c.Status(500)
		return
	}
	h.pairs[hashOwnerPairingToken(token)] = &trustedPair{appID: a.ID, codeHash: hashOwnerCode(code), expires: now.Add(ownerPairingTTL)}
	h.lastCreate[a.ID] = now
	recordRequestOperation(c, h.owner.logs, "auth_app_pair_create", "已创建受信应用登录配对："+a.ID, "", nil)
	c.JSON(200, gin.H{"code": code, "command": fmt.Sprintf("登录应用 %s %s", a.ID, code), "app_name": a.Name, "poll_token": token, "expires_in_seconds": 300})
}
func (h *TrustedLoginHandler) poll(c *gin.Context) {
	a := c.MustGet("trusted_login_app").(TrustedLoginApp)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	var body struct {
		PollToken string `json:"poll_token"`
	}
	if c.ShouldBindJSON(&body) != nil || len(body.PollToken) != 64 {
		c.JSON(400, gin.H{"error": "请求无效"})
		return
	}
	key := hashOwnerPairingToken(body.PollToken)
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	h.cleanup(now)
	p := h.pairs[key]
	if p == nil || p.appID != a.ID {
		c.JSON(200, gin.H{"approved": false, "expired": true})
		return
	}
	if now.Sub(h.lastPoll[key]) < time.Second {
		c.Header("Retry-After", "1")
		c.JSON(429, gin.H{"error": "轮询过快"})
		return
	}
	h.lastPoll[key] = now
	if p.identity == nil {
		c.JSON(200, gin.H{"approved": false, "expires_in_seconds": int(time.Until(p.expires).Seconds())})
		return
	}
	// Consume atomically; this route deliberately never issues a console cookie.
	delete(h.pairs, key)
	delete(h.lastPoll, key)
	recordRequestOperation(c, h.owner.logs, "auth_app_pair_claim", "已领取受信应用登录结果："+a.ID, p.identity.UserID, nil)
	c.JSON(200, gin.H{"approved": true, "app_id": a.ID, "identity": p.identity})
}
func (h *TrustedLoginHandler) ConsumePrivateMessage(ctx context.Context, event assistant.MessageEvent, text string) bool {
	m := trustedCommand.FindStringSubmatch(strings.TrimSpace(text))
	if len(m) != 3 || event.Kind != assistant.EventKindPrivate {
		return false
	}
	a, ok := h.apps[m[1]]
	if !ok || event.ProfileID != a.ProfileID || h.owner == nil || h.owner.runtime == nil || h.owner.auth == nil || !h.owner.auth.Required() {
		return false
	}
	cfg := h.owner.runtime.ProfileConfig(event.ProfileID)
	if cfg.ID != a.ProfileID || ownerLoginEligible(cfg) != nil || !cfg.IsOwnerEvent(event) || string(cfg.Platform) != string(event.Platform) || event.UserID == "" {
		return false
	}
	h.mu.Lock()
	now := time.Now()
	h.cleanup(now)
	var found *trustedPair
	for _, p := range h.pairs {
		if p.appID == a.ID && p.codeHash == hashOwnerCode(m[2]) && p.identity == nil {
			found = p
			break
		}
	}
	if found == nil {
		h.mu.Unlock()
		return false
	}
	found.identity = &trustedIdentity{ProfileID: cfg.ID, Platform: string(cfg.Platform), UserID: event.UserID, ConfirmedAt: now.UTC()}
	h.mu.Unlock()
	cfg.OwnerID = cfg.OwnerIDForEvent(event)
	receipt := fmt.Sprintf("已确认登录 %s（%s）\n请返回发起登录的页面。此确认仅用于该应用，不会登录 Diana 控制台。\n若非本人操作，请联系该应用管理员撤销会话。", a.Name, a.ID)
	if err := h.owner.notifyOwner(ctx, cfg, receipt); err != nil {
		recordOperation(ctx, h.owner.logs, "auth_app_pair_notify", "受信应用登录回执发送失败", event.UserID, nil)
	}
	recordOperation(ctx, h.owner.logs, "auth_app_pair_approve", "已确认受信应用登录："+a.ID, event.UserID, nil)
	return true
}
