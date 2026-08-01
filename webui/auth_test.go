package webui

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

// TestPBKDF2SHA256KnownVector 用公开测试向量验证自实现的 PBKDF2。
func TestPBKDF2SHA256KnownVector(t *testing.T) {
	// RFC 7914 附录里的 PBKDF2-HMAC-SHA256 向量（P="passwd", S="salt", c=1, dkLen=64 的前 32 字节）。
	got := pbkdf2SHA256([]byte("passwd"), []byte("salt"), 1, 32)
	want := "55ac046e56e3089fec1691c22544b605f94185216dde0465e68b9d57c20dacbc"
	if hex.EncodeToString(got) != want {
		t.Fatalf("pbkdf2 = %s, want %s", hex.EncodeToString(got), want)
	}
	// 多迭代第二向量（c=80000 太慢，用 c=2 自洽校验：两次调用结果一致且与 c=1 不同）。
	a := pbkdf2SHA256([]byte("passwd"), []byte("salt"), 2, 32)
	b := pbkdf2SHA256([]byte("passwd"), []byte("salt"), 2, 32)
	if hex.EncodeToString(a) != hex.EncodeToString(b) || hex.EncodeToString(a) == want {
		t.Fatalf("iteration handling broken")
	}
}

type memoryAuthStore struct {
	auth     storage.WebUIAuth
	authSet  bool
	sessions storage.WebUISessionSet
	sessSet  bool
}

// LoadWebUIAuth 返回内存里的密码记录。
func (m *memoryAuthStore) LoadWebUIAuth(context.Context) (storage.WebUIAuth, bool, error) {
	return m.auth, m.authSet, nil
}

// SaveWebUIAuth 保存密码记录。
func (m *memoryAuthStore) SaveWebUIAuth(_ context.Context, auth storage.WebUIAuth) error {
	m.auth = auth
	m.authSet = true
	return nil
}

// LoadWebUISessions 返回内存里的会话集合。
func (m *memoryAuthStore) LoadWebUISessions(context.Context) (storage.WebUISessionSet, bool, error) {
	return m.sessions, m.sessSet, nil
}

// SaveWebUISessions 保存会话集合。
func (m *memoryAuthStore) SaveWebUISessions(_ context.Context, set storage.WebUISessionSet) error {
	m.sessions = set
	m.sessSet = true
	return nil
}

func newAuthTestRouter(t *testing.T) (*gin.Engine, *AuthManager, *memoryAuthStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := &memoryAuthStore{}
	manager := NewAuthManager(store)
	router := gin.New()
	router.Use(manager.Middleware())
	NewAuthHandler(manager).Register(router)
	router.GET("/api/protected", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	router.GET("/api/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	router.GET("/api/qqbot/group-admin/session", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	return router, manager, store
}

// TestAuthDisabledByDefaultAllowsAll 验证对应功能场景。
func TestAuthDisabledByDefaultAllowsAll(t *testing.T) {
	router, _, _ := newAuthTestRouter(t)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/protected", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("open mode should allow access, got %d", rec.Code)
	}
}

// TestAuthFullFlow 验证对应功能场景。
func TestAuthFullFlow(t *testing.T) {
	router, manager, store := newAuthTestRouter(t)

	// 设置密码后未登录请求被拦截。
	if err := manager.SetPassword("", "diana-secret-1"); err != nil {
		t.Fatalf("SetPassword() error = %v", err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/protected", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("protected without login = %d", rec.Code)
	}

	// 豁免路径不受影响。
	for _, path := range []string{"/api/health", "/api/qqbot/group-admin/session", "/api/auth/status"} {
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("exempt path %s = %d", path, rec.Code)
		}
	}

	// 错误密码登录失败。
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"wrong-password"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password login = %d", rec.Code)
	}

	// 正确密码登录拿到 cookie。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"diana-secret-1"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, authCookieName+"=") || !strings.Contains(cookie, "HttpOnly") {
		t.Fatalf("cookie = %q", cookie)
	}

	// 带 cookie 访问受保护接口成功。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	req.Header.Set("Cookie", strings.Split(cookie, ";")[0])
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("protected with cookie = %d", rec.Code)
	}

	// 会话已持久化（重启后仍有效）。
	if !store.sessSet || len(store.sessions.Sessions) == 0 {
		t.Fatalf("sessions not persisted: %+v", store.sessions)
	}
	restarted := NewAuthManager(store)
	token := strings.TrimPrefix(strings.Split(cookie, ";")[0], authCookieName+"=")
	if !restarted.Authenticate(token) {
		t.Fatal("session should survive restart")
	}

	// 登出后会话失效。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Cookie", authCookieName+"="+token)
	router.ServeHTTP(rec, req)
	if manager.Authenticate(token) {
		t.Fatal("token should be invalid after logout")
	}
}

// TestAuthBootstrapAndPasswordRules 验证对应功能场景。
func TestAuthBootstrapAndPasswordRules(t *testing.T) {
	store := &memoryAuthStore{}
	manager := NewAuthManager(store)
	if err := manager.Bootstrap("bootstrap-pass"); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if !manager.Required() {
		t.Fatal("bootstrap should enable auth")
	}
	// Bootstrap 不覆盖已有密码。
	if err := manager.Bootstrap("another-pass-xx"); err != nil {
		t.Fatalf("Bootstrap() second error = %v", err)
	}
	if _, err := manager.Login("another-pass-xx"); err == nil {
		t.Fatal("second bootstrap should not overwrite password")
	}
	if _, err := manager.Login("bootstrap-pass"); err != nil {
		t.Fatalf("original password should work: %v", err)
	}
	// 修改密码需要旧密码，且新密码有长度下限。
	if err := manager.SetPassword("wrong-current", "new-password-1"); err == nil {
		t.Fatal("wrong current password accepted")
	}
	if err := manager.SetPassword("bootstrap-pass", "short"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := manager.SetPassword("bootstrap-pass", "new-password-1"); err != nil {
		t.Fatalf("SetPassword() error = %v", err)
	}
	if _, err := manager.Login("new-password-1"); err != nil {
		t.Fatalf("new password login failed: %v", err)
	}
}
