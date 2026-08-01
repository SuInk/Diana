package webui

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

const (
	authCookieName     = "diana_session"
	defaultAdminUser   = "admin@diana.local"
	authSessionTTL     = 30 * 24 * time.Hour
	authPBKDF2Iters    = 210_000
	authMinPasswordLen = 8
)

var (
	ErrWrongPassword    = errors.New("账号或密码不正确")
	ErrPasswordTooShort = errors.New("密码至少 8 位")
)

// AuthStore 持久化 WebUI 密码与会话。
type AuthStore interface {
	LoadWebUIAuth(ctx context.Context) (storage.WebUIAuth, bool, error)
	SaveWebUIAuth(ctx context.Context, auth storage.WebUIAuth) error
	LoadWebUISessions(ctx context.Context) (storage.WebUISessionSet, bool, error)
	SaveWebUISessions(ctx context.Context, set storage.WebUISessionSet) error
}

// pbkdf2SHA256 是 RFC 2898 PBKDF2-HMAC-SHA256 的最小实现，避免引入额外依赖。
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, blocks*hashLen)
	buf := make([]byte, 4)
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf, uint32(block))
		prf.Write(buf)
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iterations; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// AuthManager 管理 WebUI 管理员凭据与登录会话。
type AuthManager struct {
	store AuthStore

	mu       sync.Mutex
	auth     *storage.WebUIAuth
	sessions map[string]time.Time // token 哈希 -> 过期时间
}

// NewAuthManager 创建鉴权管理器并从存储加载状态。
func NewAuthManager(store AuthStore) *AuthManager {
	m := &AuthManager{store: store, sessions: map[string]time.Time{}}
	if store == nil {
		return m
	}
	ctx := context.Background()
	if auth, ok, err := store.LoadWebUIAuth(ctx); err == nil && ok && auth.PasswordHash != "" {
		if strings.TrimSpace(auth.Username) == "" {
			auth.Username = defaultAdminUser
		}
		m.auth = &auth
	}
	if set, ok, err := store.LoadWebUISessions(ctx); err == nil && ok {
		for _, session := range set.Sessions {
			if time.Now().Before(session.ExpiresAt) {
				m.sessions[session.TokenHash] = session.ExpiresAt
			}
		}
	}
	return m
}

// Bootstrap 首次初始化管理员；空密码会生成安全随机密码并返回明文。
func (m *AuthManager) Bootstrap(password string) (string, error) {
	password = strings.TrimSpace(password)
	m.mu.Lock()
	configured := m.auth != nil
	m.mu.Unlock()
	if configured {
		return "", nil
	}
	generated := ""
	if password == "" {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		password = base64.RawURLEncoding.EncodeToString(raw)
		generated = password
	}
	if err := m.setPassword(password); err != nil {
		return "", err
	}
	return generated, nil
}

// Required 返回当前是否启用了密码鉴权。
func (m *AuthManager) Required() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.auth != nil
}

// verify 校验明文密码；恒定时间比较。
func (m *AuthManager) verify(username, password string) bool {
	m.mu.Lock()
	auth := m.auth
	m.mu.Unlock()
	if auth == nil {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(username)), []byte(auth.Username)) != 1 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(auth.Salt)
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(auth.PasswordHash)
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, auth.Iterations, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// setPassword 生成盐并保存新密码，同时清空全部旧会话。
func (m *AuthManager) setPassword(password string) error {
	if len([]rune(password)) < authMinPasswordLen {
		return ErrPasswordTooShort
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash := pbkdf2SHA256([]byte(password), salt, authPBKDF2Iters, sha256.Size)
	record := storage.WebUIAuth{
		Username:     defaultAdminUser,
		PasswordHash: base64.StdEncoding.EncodeToString(hash),
		Salt:         base64.StdEncoding.EncodeToString(salt),
		Iterations:   authPBKDF2Iters,
		UpdatedAt:    time.Now(),
	}
	if m.store != nil {
		if err := m.store.SaveWebUIAuth(context.Background(), record); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.auth = &record
	// 改密后旧会话全部失效，所有端重新登录。
	m.sessions = map[string]time.Time{}
	m.mu.Unlock()
	m.persistSessions()
	return nil
}

// SetPassword 设置或修改密码；已有密码时必须先校验当前密码。
func (m *AuthManager) SetPassword(current, next string) error {
	if m.Required() && !m.verify(defaultAdminUser, current) {
		return ErrWrongPassword
	}
	return m.setPassword(next)
}

// Login 校验密码并签发会话 token。
func (m *AuthManager) Login(username, password string) (string, error) {
	if !m.verify(username, password) {
		return "", ErrWrongPassword
	}
	return m.IssueSession()
}

// IssueSession 直接签发一个新会话；供密码之外的受信登录方式（如主人验证码）使用。
func (m *AuthManager) IssueSession() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	m.mu.Lock()
	m.sessions[hashToken(token)] = time.Now().Add(authSessionTTL)
	m.mu.Unlock()
	m.persistSessions()
	return token, nil
}

// Authenticate 校验会话 token 是否有效。
func (m *AuthManager) Authenticate(token string) bool {
	if token == "" {
		return false
	}
	key := hashToken(token)
	m.mu.Lock()
	defer m.mu.Unlock()
	expiry, ok := m.sessions[key]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(m.sessions, key)
		return false
	}
	return true
}

// Logout 使指定会话失效。
func (m *AuthManager) Logout(token string) {
	m.mu.Lock()
	delete(m.sessions, hashToken(token))
	m.mu.Unlock()
	m.persistSessions()
}

// persistSessions 把有效会话写回存储；失败只影响重启后需要重新登录。
func (m *AuthManager) persistSessions() {
	if m.store == nil {
		return
	}
	m.mu.Lock()
	set := storage.WebUISessionSet{Sessions: make([]storage.WebUISession, 0, len(m.sessions))}
	now := time.Now()
	for tokenHash, expiresAt := range m.sessions {
		if now.Before(expiresAt) {
			set.Sessions = append(set.Sessions, storage.WebUISession{TokenHash: tokenHash, ExpiresAt: expiresAt})
		}
	}
	m.mu.Unlock()
	_ = m.store.SaveWebUISessions(context.Background(), set)
}

// hashToken 计算会话 token 的存储哈希。
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// authExemptPath 判断请求路径是否豁免鉴权。
func authExemptPath(path string) bool {
	switch {
	case path == "/api/auth/status",
		path == "/api/auth/login",
		path == "/api/auth/owner/status",
		path == "/api/auth/owner/pair",
		path == "/api/auth/owner/pair/status":
		return true
	case path == "/api/health":
		// 健康检查供监控探活。
		return true
	case strings.HasPrefix(path, "/onebot/"):
		// NapCat 反向 WebSocket 由 OneBot access token 单独鉴权。
		return true
	case strings.HasPrefix(path, "/api/assistant/group-admin"), strings.HasPrefix(path, "/api/qqbot/group-admin"):
		// 群管理页有自己的一次性群验证码 token 流程。
		return true
	default:
		return false
	}
}

// Middleware 返回鉴权中间件；凭据缺失时失败关闭。
func (m *AuthManager) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if !strings.HasPrefix(path, "/api/") || authExemptPath(path) {
			// 静态资源放行，登录界面由前端渲染。
			c.Next()
			return
		}
		if !m.Required() {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "管理员凭据未初始化"})
			return
		}
		token, _ := c.Cookie(authCookieName)
		if !m.Authenticate(token) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "需要登录", "auth_required": true})
			return
		}
		c.Next()
	}
}

// AuthHandler 暴露登录、登出、状态和改密接口。
type AuthHandler struct {
	manager *AuthManager
	logs    AppLogWriter
}

// NewAuthHandler 创建鉴权接口处理器。
func NewAuthHandler(manager *AuthManager) *AuthHandler {
	return &AuthHandler{manager: manager}
}

// SetLogStore 注入操作日志写入器。
func (h *AuthHandler) SetLogStore(store AppLogWriter) {
	h.logs = store
}

// Register 注册鉴权相关路由。
func (h *AuthHandler) Register(router gin.IRouter) {
	router.GET("/api/auth/status", h.status)
	router.POST("/api/auth/login", h.login)
	router.POST("/api/auth/logout", h.logout)
	router.POST("/api/auth/password", h.setPassword)
}

// status 返回鉴权开关与当前登录态。
func (h *AuthHandler) status(c *gin.Context) {
	token, _ := c.Cookie(authCookieName)
	c.JSON(http.StatusOK, gin.H{
		"auth_required": h.manager.Required(),
		"authenticated": h.manager.Authenticate(token),
	})
}

// login 校验密码并写入会话 cookie。
func (h *AuthHandler) login(c *gin.Context) {
	var payload struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	token, err := h.manager.Login(payload.Username, payload.Password)
	if err != nil {
		// 失败固定延迟，抬高在线爆破成本。
		time.Sleep(400 * time.Millisecond)
		logAndWriteError(c, h.logs, http.StatusUnauthorized, "auth.login", err, "", nil)
		return
	}
	h.setSessionCookie(c, token, int(authSessionTTL/time.Second))
	recordRequestOperation(c, h.logs, "auth.login", "WebUI 登录成功", "", nil)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// logout 使当前会话失效并清除 cookie。
func (h *AuthHandler) logout(c *gin.Context) {
	if token, err := c.Cookie(authCookieName); err == nil {
		h.manager.Logout(token)
	}
	h.setSessionCookie(c, "", -1)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// setPassword 设置或修改管理密码。
func (h *AuthHandler) setPassword(c *gin.Context) {
	var payload struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := h.manager.SetPassword(payload.CurrentPassword, payload.NewPassword); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrWrongPassword) {
			status = http.StatusUnauthorized
			time.Sleep(400 * time.Millisecond)
		}
		logAndWriteError(c, h.logs, status, "auth.password", err, "", nil)
		return
	}
	// 改密清空了所有会话，立刻给当前端签发新会话，避免自己被登出。
	token, err := h.manager.Login(defaultAdminUser, payload.NewPassword)
	if err == nil {
		h.setSessionCookie(c, token, int(authSessionTTL/time.Second))
	}
	recordRequestOperation(c, h.logs, "auth.password", "WebUI 管理密码已更新", "", nil)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// setSessionCookie 写会话 cookie；不设 Secure 以兼容内网 HTTP 部署，公网请套 HTTPS 反代。
func (h *AuthHandler) setSessionCookie(c *gin.Context, token string, maxAge int) {
	authSetSessionCookie(c, token, maxAge)
}

// authSetSessionCookie 是会话 cookie 的统一写入口，密码登录与主人验证码登录共用。
func authSetSessionCookie(c *gin.Context, token string, maxAge int) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(authCookieName, token, maxAge, "/", "", false, true)
}
