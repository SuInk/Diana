// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

type fakeOwnerRuntime struct {
	cfg   assistant.BotConfig
	calls []fakeOwnerAPICall
}

type fakeOwnerAPICall struct {
	action string
	params map[string]any
}

func (f *fakeOwnerRuntime) Config() assistant.BotConfig {
	return f.cfg
}

func (f *fakeOwnerRuntime) CallOneBotAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	f.calls = append(f.calls, fakeOwnerAPICall{action: action, params: params})
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

func createOwnerChallenge(t *testing.T, router *gin.Engine, runtime *fakeOwnerRuntime) (string, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/owner/challenge", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("create challenge = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		ChallengeToken string `json:"challenge_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	if payload.ChallengeToken == "" || len(runtime.calls) != 1 {
		t.Fatalf("challenge payload = %+v, calls = %+v", payload, runtime.calls)
	}
	call := runtime.calls[0]
	if call.action != "send_private_msg" {
		t.Fatalf("challenge action = %q", call.action)
	}
	segments, ok := call.params["message"].([]map[string]any)
	if !ok || len(segments) != 1 {
		t.Fatalf("challenge message = %#v", call.params["message"])
	}
	data, ok := segments[0]["data"].(map[string]string)
	if !ok {
		t.Fatalf("challenge data = %#v", segments[0]["data"])
	}
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(data["text"])
	if code == "" || strings.Contains(rec.Body.String(), code) {
		t.Fatalf("code delivery leaked or missing: response=%s message=%q", rec.Body.String(), data["text"])
	}
	return code, payload.ChallengeToken
}

func verifyOwnerChallenge(router *gin.Engine, challengeToken, code string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/owner/verify",
		strings.NewReader(`{"challenge_token":"`+challengeToken+`","code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

func TestOwnerChallengeLoginFullFlow(t *testing.T) {
	runtime := &fakeOwnerRuntime{cfg: assistant.BotConfig{
		Platform: assistant.PlatformOneBotV11, OwnerID: "10001", OwnerLoginEnabled: true, Name: "Diana",
	}}
	router, manager, _ := newOwnerLoginTestRouter(t, runtime)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/owner/status", nil))
	if !strings.Contains(rec.Body.String(), `"code_delivery_available":true`) {
		t.Fatalf("status = %s", rec.Body.String())
	}

	code, challengeToken := createOwnerChallenge(t, router, runtime)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/owner/challenge", nil))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("challenge cooldown = %d, retry-after = %q", rec.Code, rec.Header().Get("Retry-After"))
	}

	rec = verifyOwnerChallenge(router, "not-the-browser-token", code)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong challenge token = %d: %s", rec.Code, rec.Body.String())
	}
	wrongCode := "000000"
	if code == wrongCode {
		wrongCode = "999999"
	}
	rec = verifyOwnerChallenge(router, challengeToken, wrongCode)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong challenge code = %d: %s", rec.Code, rec.Body.String())
	}
	rec = verifyOwnerChallenge(router, challengeToken, code)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify challenge = %d: %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	token := strings.TrimPrefix(strings.Split(cookie, ";")[0], authCookieName+"=")
	if !manager.Authenticate(token) {
		t.Fatal("issued challenge session invalid")
	}

	rec = verifyOwnerChallenge(router, challengeToken, code)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("challenge code reuse = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestOwnerChallengeDeliverySupportsTelegram(t *testing.T) {
	action, params, err := ownerChallengeDelivery(assistant.BotConfig{
		Platform: assistant.PlatformTelegram,
		OwnerID:  "10001",
	}, "login code")
	if err != nil {
		t.Fatal(err)
	}
	if action != "sendMessage" || params["chat_id"] != int64(10001) || params["text"] != "login code" {
		t.Fatalf("delivery = %q %#v", action, params)
	}
}

func TestOwnerChallengeExpiresWhenOwnerChanges(t *testing.T) {
	runtime := &fakeOwnerRuntime{cfg: assistant.BotConfig{
		Platform: assistant.PlatformOneBotV11, OwnerID: "10001", OwnerLoginEnabled: true,
	}}
	router, _, _ := newOwnerLoginTestRouter(t, runtime)
	code, challengeToken := createOwnerChallenge(t, router, runtime)
	runtime.cfg.OwnerID = "20002"

	rec := verifyOwnerChallenge(router, challengeToken, code)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("challenge survived owner change = %d: %s", rec.Code, rec.Body.String())
	}
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
