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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

const (
	authCookieName          = "diana_session"
	legacyAdminUser         = "admin@diana.local"
	adminUsernamePrefix     = "diana#"
	authRandomUsernameBytes = 8
	authSessionTTL          = 30 * 24 * time.Hour
	authPBKDF2Iters         = 210_000
	authMinPasswordLen      = 8
)

var (
	ErrWrongPassword    = errors.New("账号或密码不正确")
	ErrPasswordTooShort = errors.New("密码至少 8 位")
	ErrUsernameInvalid  = errors.New("账号必须以 diana# 开头，后面至少包含 8 位字母或数字")
)

// AuthBootstrapResult 描述首次启动创建的管理员凭据。
type AuthBootstrapResult struct {
	Created           bool
	Username          string
	GeneratedPassword string
}

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
	sessions map[string]storage.WebUISession // token 哈希 -> 会话元数据
}

type AuthSessionMetadata struct {
	DeviceName string
	UserAgent  string
	IPAddress  string
}

type AuthSessionInfo struct {
	ID         string    `json:"id"`
	DeviceName string    `json:"device_name"`
	UserAgent  string    `json:"user_agent,omitempty"`
	IPAddress  string    `json:"ip_address,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
}

// NewAuthManager 创建鉴权管理器并从存储加载状态。
func NewAuthManager(store AuthStore) *AuthManager {
	m := &AuthManager{store: store, sessions: map[string]storage.WebUISession{}}
	if store == nil {
		return m
	}
	ctx := context.Background()
	if auth, ok, err := store.LoadWebUIAuth(ctx); err == nil && ok && auth.PasswordHash != "" {
		if strings.TrimSpace(auth.Username) == "" {
			auth.Username = legacyAdminUser
		}
		m.auth = &auth
	}
	if set, ok, err := store.LoadWebUISessions(ctx); err == nil && ok {
		now := time.Now()
		for _, session := range set.Sessions {
			session.TokenHash = strings.TrimSpace(session.TokenHash)
			if session.TokenHash != "" && now.Before(session.ExpiresAt) {
				normalizeWebUISession(&session, now)
				m.sessions[session.TokenHash] = session
			}
		}
	}
	return m
}

// Bootstrap 首次初始化管理员；空账号和密码会分别生成安全随机值。
func (m *AuthManager) Bootstrap(username, password string) (AuthBootstrapResult, error) {
	password = strings.TrimSpace(password)
	m.mu.Lock()
	auth := m.auth
	m.mu.Unlock()
	if auth != nil {
		return AuthBootstrapResult{Username: auth.Username}, nil
	}
	username = strings.TrimSpace(username)
	if username == "" {
		var err error
		username, err = randomAdminUsername()
		if err != nil {
			return AuthBootstrapResult{}, err
		}
	} else if err := validateAdminUsername(username); err != nil {
		return AuthBootstrapResult{}, err
	}
	generatedPassword := ""
	if password == "" {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return AuthBootstrapResult{}, err
		}
		password = base64.RawURLEncoding.EncodeToString(raw)
		generatedPassword = password
	}
	if err := m.setCredentials(username, password); err != nil {
		return AuthBootstrapResult{}, err
	}
	return AuthBootstrapResult{Created: true, Username: username, GeneratedPassword: generatedPassword}, nil
}

func randomAdminUsername() (string, error) {
	raw := make([]byte, authRandomUsernameBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return adminUsernamePrefix + hex.EncodeToString(raw), nil
}

func validateAdminUsername(username string) error {
	if !strings.HasPrefix(username, adminUsernamePrefix) {
		return ErrUsernameInvalid
	}
	suffix := strings.TrimPrefix(username, adminUsernamePrefix)
	if len(suffix) < 8 {
		return ErrUsernameInvalid
	}
	for _, char := range suffix {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') {
			return ErrUsernameInvalid
		}
	}
	return nil
}

// Required 返回当前是否启用了密码鉴权。
func (m *AuthManager) Required() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.auth != nil
}

// Username 返回当前管理员账号。
func (m *AuthManager) Username() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.auth == nil {
		return ""
	}
	return m.auth.Username
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

// setCredentials 生成盐并保存新凭据，同时清空全部旧会话。
func (m *AuthManager) setCredentials(username, password string) error {
	if len([]rune(password)) < authMinPasswordLen {
		return ErrPasswordTooShort
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash := pbkdf2SHA256([]byte(password), salt, authPBKDF2Iters, sha256.Size)
	record := storage.WebUIAuth{
		Username:     username,
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
	m.sessions = map[string]storage.WebUISession{}
	m.mu.Unlock()
	m.persistSessions()
	return nil
}

// SetCredentials 设置或修改账号密码；已有凭据时必须先校验当前密码。
func (m *AuthManager) SetCredentials(current, nextUsername, nextPassword string) (string, error) {
	username := strings.TrimSpace(nextUsername)
	if m.Required() {
		currentUsername := m.Username()
		if !m.verify(currentUsername, current) {
			return "", ErrWrongPassword
		}
		if username == "" {
			username = currentUsername
		} else if err := validateAdminUsername(username); err != nil {
			return "", err
		}
	} else if username == "" {
		var err error
		username, err = randomAdminUsername()
		if err != nil {
			return "", err
		}
	} else if err := validateAdminUsername(username); err != nil {
		return "", err
	}
	if err := m.setCredentials(username, nextPassword); err != nil {
		return "", err
	}
	return username, nil
}

// SetPassword 保留旧调用方式，并在首次设置时生成随机账号。
func (m *AuthManager) SetPassword(current, next string) error {
	_, err := m.SetCredentials(current, "", next)
	return err
}

// Login 校验密码并签发会话 token。
func (m *AuthManager) Login(username, password string) (string, error) {
	return m.LoginWithMetadata(username, password, AuthSessionMetadata{DeviceName: "Web 登录"})
}

// LoginWithMetadata 校验凭据并记录当前浏览器会话来源。
func (m *AuthManager) LoginWithMetadata(username, password string, metadata AuthSessionMetadata) (string, error) {
	if !m.verify(username, password) {
		return "", ErrWrongPassword
	}
	return m.IssueSessionWithMetadata(metadata)
}

// IssueSession 直接签发一个新会话；供密码之外的受信登录方式（如主人验证码）使用。
func (m *AuthManager) IssueSession() (string, error) {
	return m.IssueSessionWithMetadata(AuthSessionMetadata{DeviceName: "主人验证码"})
}

// IssueSessionWithMetadata 直接签发带来源信息的新会话。
func (m *AuthManager) IssueSessionWithMetadata(metadata AuthSessionMetadata) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	tokenHash := hashToken(token)
	now := time.Now()
	session := storage.WebUISession{
		ID:         webUISessionID(tokenHash),
		TokenHash:  tokenHash,
		DeviceName: strings.TrimSpace(metadata.DeviceName),
		UserAgent:  strings.TrimSpace(metadata.UserAgent),
		IPAddress:  strings.TrimSpace(metadata.IPAddress),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(authSessionTTL),
	}
	normalizeWebUISession(&session, now)
	m.mu.Lock()
	m.sessions[tokenHash] = session
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
	session, ok := m.sessions[key]
	if !ok {
		m.mu.Unlock()
		return false
	}
	now := time.Now()
	if now.After(session.ExpiresAt) {
		delete(m.sessions, key)
		m.mu.Unlock()
		m.persistSessions()
		return false
	}
	persist := session.LastSeenAt.IsZero() || now.Sub(session.LastSeenAt) >= 5*time.Minute
	if persist {
		session.LastSeenAt = now
		m.sessions[key] = session
	}
	m.mu.Unlock()
	if persist {
		m.persistSessions()
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

// Sessions 返回有效会话，当前会话排在最前，其余按最后使用时间倒序。
func (m *AuthManager) Sessions(currentToken string) []AuthSessionInfo {
	currentHash := hashToken(currentToken)
	now := time.Now()
	m.mu.Lock()
	items := make([]AuthSessionInfo, 0, len(m.sessions))
	changed := false
	for tokenHash, session := range m.sessions {
		if !now.Before(session.ExpiresAt) {
			delete(m.sessions, tokenHash)
			changed = true
			continue
		}
		normalizeWebUISession(&session, now)
		items = append(items, AuthSessionInfo{
			ID:         session.ID,
			DeviceName: session.DeviceName,
			UserAgent:  session.UserAgent,
			IPAddress:  session.IPAddress,
			CreatedAt:  session.CreatedAt,
			LastSeenAt: session.LastSeenAt,
			ExpiresAt:  session.ExpiresAt,
			Current:    tokenHash == currentHash,
		})
	}
	m.mu.Unlock()
	if changed {
		m.persistSessions()
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Current != items[j].Current {
			return items[i].Current
		}
		return items[i].LastSeenAt.After(items[j].LastSeenAt)
	})
	return items
}

// RevokeSession 撤销指定会话 ID，并报告它是否是当前会话。
func (m *AuthManager) RevokeSession(id, currentToken string) (bool, bool) {
	id = strings.TrimSpace(id)
	currentHash := hashToken(currentToken)
	m.mu.Lock()
	foundHash := ""
	for tokenHash, session := range m.sessions {
		if session.ID == id || webUISessionID(tokenHash) == id {
			foundHash = tokenHash
			break
		}
	}
	if foundHash != "" {
		delete(m.sessions, foundHash)
	}
	m.mu.Unlock()
	if foundHash == "" {
		return false, false
	}
	m.persistSessions()
	return foundHash == currentHash, true
}

// RevokeOtherSessions 保留当前会话并撤销其他所有会话。
func (m *AuthManager) RevokeOtherSessions(currentToken string) int {
	currentHash := hashToken(currentToken)
	m.mu.Lock()
	revoked := 0
	for tokenHash := range m.sessions {
		if tokenHash == currentHash {
			continue
		}
		delete(m.sessions, tokenHash)
		revoked++
	}
	m.mu.Unlock()
	if revoked > 0 {
		m.persistSessions()
	}
	return revoked
}

// persistSessions 把有效会话写回存储；失败只影响重启后需要重新登录。
func (m *AuthManager) persistSessions() {
	if m.store == nil {
		return
	}
	m.mu.Lock()
	set := storage.WebUISessionSet{Sessions: make([]storage.WebUISession, 0, len(m.sessions))}
	now := time.Now()
	for tokenHash, session := range m.sessions {
		if now.Before(session.ExpiresAt) {
			session.TokenHash = tokenHash
			normalizeWebUISession(&session, now)
			set.Sessions = append(set.Sessions, session)
		}
	}
	m.mu.Unlock()
	_ = m.store.SaveWebUISessions(context.Background(), set)
}

func webUISessionID(tokenHash string) string {
	tokenHash = strings.TrimSpace(tokenHash)
	if len(tokenHash) > 24 {
		return tokenHash[:24]
	}
	return tokenHash
}

func normalizeWebUISession(session *storage.WebUISession, now time.Time) {
	if session.ID == "" {
		session.ID = webUISessionID(session.TokenHash)
	}
	if session.DeviceName == "" {
		session.DeviceName = "Web 登录"
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	if session.LastSeenAt.IsZero() {
		session.LastSeenAt = session.CreatedAt
	}
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
		path == "/api/auth/owner/challenge",
		path == "/api/auth/owner/verify",
		path == "/api/auth/owner/pair",
		path == "/api/auth/owner/pair/status":
		return true
	case path == "/api/health":
		// 健康检查供监控探活。
		return true
	case strings.HasPrefix(path, "/onebot/"):
		// NapCat 反向 WebSocket 由 OneBot access token 单独鉴权。
		return true
	case strings.HasPrefix(path, "/api/qqbot/media/"), strings.HasPrefix(path, "/api/assistant/media/"):
		// 临时媒体使用高熵、短有效期 token，供 NapCat 在独立进程或容器中拉取。
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
	router.GET("/api/auth/sessions", h.listSessions)
	router.DELETE("/api/auth/sessions/:id", h.revokeSession)
	router.POST("/api/auth/sessions/revoke-others", h.revokeOtherSessions)
}

// status 返回鉴权开关与当前登录态。
func (h *AuthHandler) status(c *gin.Context) {
	token, _ := c.Cookie(authCookieName)
	authenticated := h.manager.Authenticate(token)
	result := gin.H{
		"auth_required": h.manager.Required(),
		"authenticated": authenticated,
	}
	if authenticated {
		result["username"] = h.manager.Username()
	}
	c.JSON(http.StatusOK, result)
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
	token, err := h.manager.LoginWithMetadata(payload.Username, payload.Password, authSessionMetadata(c))
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

func (h *AuthHandler) listSessions(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	token, _ := c.Cookie(authCookieName)
	c.JSON(http.StatusOK, gin.H{"sessions": h.manager.Sessions(token)})
}

func (h *AuthHandler) revokeSession(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		writeError(c, http.StatusBadRequest, errors.New("session id is required"))
		return
	}
	token, _ := c.Cookie(authCookieName)
	current, found := h.manager.RevokeSession(id, token)
	if !found {
		writeError(c, http.StatusNotFound, errors.New("session not found"))
		return
	}
	if current {
		h.setSessionCookie(c, "", -1)
	}
	recordRequestOperation(c, h.logs, "auth.session.revoke", "WebUI 会话已撤销", id, map[string]any{"current": current})
	c.JSON(http.StatusOK, gin.H{"revoked": true, "current": current})
}

func (h *AuthHandler) revokeOtherSessions(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	token, _ := c.Cookie(authCookieName)
	revoked := h.manager.RevokeOtherSessions(token)
	recordRequestOperation(c, h.logs, "auth.session.revoke_others", "其他 WebUI 会话已撤销", "", map[string]any{"revoked": revoked})
	c.JSON(http.StatusOK, gin.H{"revoked": revoked})
}

// logout 使当前会话失效并清除 cookie。
func (h *AuthHandler) logout(c *gin.Context) {
	if token, err := c.Cookie(authCookieName); err == nil {
		h.manager.Logout(token)
	}
	h.setSessionCookie(c, "", -1)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// setPassword 设置或修改管理账号与密码。
func (h *AuthHandler) setPassword(c *gin.Context) {
	var payload struct {
		CurrentPassword string `json:"current_password"`
		NewUsername     string `json:"new_username"`
		NewPassword     string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	username, err := h.manager.SetCredentials(payload.CurrentPassword, payload.NewUsername, payload.NewPassword)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrWrongPassword) {
			status = http.StatusUnauthorized
			time.Sleep(400 * time.Millisecond)
		}
		logAndWriteError(c, h.logs, status, "auth.password", err, "", nil)
		return
	}
	// 改密清空了所有会话，立刻给当前端签发新会话，避免自己被登出。
	token, err := h.manager.LoginWithMetadata(username, payload.NewPassword, authSessionMetadata(c))
	if err == nil {
		h.setSessionCookie(c, token, int(authSessionTTL/time.Second))
	}
	recordRequestOperation(c, h.logs, "auth.password", "WebUI 管理凭据已更新", "", nil)
	c.JSON(http.StatusOK, gin.H{"ok": true, "username": username})
}

func authSessionMetadata(c *gin.Context) AuthSessionMetadata {
	userAgent := strings.TrimSpace(c.Request.UserAgent())
	return AuthSessionMetadata{
		DeviceName: authDeviceName(userAgent),
		UserAgent:  userAgent,
		IPAddress:  strings.TrimSpace(c.ClientIP()),
	}
}

func authDeviceName(userAgent string) string {
	lower := strings.ToLower(userAgent)
	device := "浏览器"
	switch {
	case strings.Contains(lower, "iphone") || strings.Contains(lower, "ipad"):
		device = "iOS 浏览器"
	case strings.Contains(lower, "android"):
		device = "Android 浏览器"
	case strings.Contains(lower, "macintosh") || strings.Contains(lower, "mac os"):
		device = "macOS 浏览器"
	case strings.Contains(lower, "windows"):
		device = "Windows 浏览器"
	case strings.Contains(lower, "linux"):
		device = "Linux 浏览器"
	}
	return device
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
