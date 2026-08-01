package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

type fakeOwnerRuntime struct {
	cfg assistant.BotConfig
}

func (f *fakeOwnerRuntime) Config() assistant.BotConfig {
	return f.cfg
}

func (f *fakeOwnerRuntime) CallOneBotAPI(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}

func newOwnerLoginTestRouter(t *testing.T, runtime *fakeOwnerRuntime) (*gin.Engine, *AuthManager, *OwnerLoginHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := NewAuthManager(&memoryAuthStore{})
	if err := manager.SetPassword("", "console-pass-1"); err != nil {
		t.Fatalf("SetPassword() error = %v", err)
	}
	router := gin.New()
	handler := NewOwnerLoginHandler(manager, runtime)
	handler.Register(router)
	return router, manager, handler
}

func createOwnerPairing(t *testing.T, router *gin.Engine) (string, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/owner/pair", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("create pairing = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Code      string `json:"code"`
		PollToken string `json:"poll_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode pairing: %v", err)
	}
	if len(payload.Code) != 6 || payload.PollToken == "" {
		t.Fatalf("invalid pairing payload: %+v", payload)
	}
	return payload.Code, payload.PollToken
}

func pollOwnerPairing(t *testing.T, router *gin.Engine, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/owner/pair/status",
		strings.NewReader(`{"poll_token":"`+token+`"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

func TestOwnerLoginFullFlow(t *testing.T) {
	runtime := &fakeOwnerRuntime{cfg: assistant.BotConfig{
		OwnerID: "10001", OwnerLoginEnabled: true, Name: "Diana",
	}}
	router, manager, handler := newOwnerLoginTestRouter(t, runtime)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/owner/status", nil))
	if !strings.Contains(rec.Body.String(), `"available":true`) {
		t.Fatalf("status = %s", rec.Body.String())
	}

	code, pollToken := createOwnerPairing(t, router)
	rec = pollOwnerPairing(t, router, pollToken)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"approved":false`) {
		t.Fatalf("pending poll = %d: %s", rec.Code, rec.Body.String())
	}

	if handler.ConsumePrivateMessage(context.Background(), assistant.MessageEvent{
		Kind: assistant.EventKindGroup, UserID: "10001",
	}, code) {
		t.Fatal("group message approved pairing")
	}
	if handler.ConsumePrivateMessage(context.Background(), assistant.MessageEvent{
		Kind: assistant.EventKindPrivate, UserID: "20002",
	}, code) {
		t.Fatal("non-owner message approved pairing")
	}
	if !handler.ConsumePrivateMessage(context.Background(), assistant.MessageEvent{
		Kind: assistant.EventKindPrivate, UserID: "10001",
	}, "登录控制台 "+code) {
		t.Fatal("owner private message did not approve pairing")
	}
	if handler.ConsumePrivateMessage(context.Background(), assistant.MessageEvent{
		Kind: assistant.EventKindPrivate, UserID: "10001",
	}, code) {
		t.Fatal("single-use code was accepted twice")
	}

	rec = pollOwnerPairing(t, router, pollToken)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"approved":true`) {
		t.Fatalf("approved poll = %d: %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	token := strings.TrimPrefix(strings.Split(cookie, ";")[0], authCookieName+"=")
	if !manager.Authenticate(token) {
		t.Fatal("issued session invalid")
	}

	rec = pollOwnerPairing(t, router, pollToken)
	if !strings.Contains(rec.Body.String(), `"expired":true`) {
		t.Fatalf("poll token reuse = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestOwnerLoginUnavailableWhenDisabled(t *testing.T) {
	runtime := &fakeOwnerRuntime{cfg: assistant.BotConfig{OwnerID: "10001"}}
	router, _, _ := newOwnerLoginTestRouter(t, runtime)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/owner/pair", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("disabled pairing = %d", rec.Code)
	}

	runtime = &fakeOwnerRuntime{cfg: assistant.BotConfig{OwnerLoginEnabled: true}}
	router, _, _ = newOwnerLoginTestRouter(t, runtime)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/owner/status", nil))
	if !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("status = %s", rec.Body.String())
	}
}
