// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

type fakeOwnerRuntime struct {
	mu    sync.Mutex
	cfg   assistant.BotConfig
	calls []fakeOwnerAPICall
}

type fakeOwnerAPICall struct {
	action string
	params map[string]any
}

func (f *fakeOwnerRuntime) Config() assistant.BotConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}

func (f *fakeOwnerRuntime) CallOneBotAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeOwnerAPICall{action: action, params: params})
	return map[string]any{}, nil
}

func (f *fakeOwnerRuntime) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		out = append(out, oneBotCallText(call))
	}
	return out
}

func oneBotCallText(call fakeOwnerAPICall) string {
	if text, ok := call.params["text"].(string); ok {
		return text
	}
	segments, ok := call.params["message"].([]map[string]any)
	if !ok || len(segments) == 0 {
		return ""
	}
	data, ok := segments[0]["data"].(map[string]string)
	if !ok {
		return ""
	}
	return data["text"]
}

func ownerLoginRuntime() *fakeOwnerRuntime {
	return &fakeOwnerRuntime{cfg: assistant.BotConfig{
		Platform: assistant.PlatformOneBotV11, OwnerID: "10001", Name: "Diana", OwnerLoginEnabled: true,
	}}
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

func claimOwnerPairing(router *gin.Engine, code string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/owner/pair/claim",
		strings.NewReader(`{"code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

func ownerPrivateMessage(handler *OwnerLoginHandler, text string) bool {
	return handler.ConsumePrivateMessage(context.Background(), assistant.MessageEvent{
		Kind: assistant.EventKindPrivate, UserID: "10001",
	}, text)
}

// 主人私聊发回验证码即完成登录，网页轮询到之后拿到会话，并且主人会收到一条
// 带来源信息的回执。
func TestOwnerPairingApprovesOnCode(t *testing.T) {
	runtime := ownerLoginRuntime()
	router, manager, handler := newOwnerLoginTestRouter(t, runtime)

	code, pollToken := createOwnerPairing(t, router)
	if !ownerPrivateMessage(handler, "登录控制台 "+code) {
		t.Fatal("owner code was not consumed")
	}

	rec := pollOwnerPairing(t, router, pollToken)
	if !strings.Contains(rec.Body.String(), `"approved":true`) {
		t.Fatalf("approved poll = %d: %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	token := strings.TrimPrefix(strings.Split(cookie, ";")[0], authCookieName+"=")
	if !manager.Authenticate(token) {
		t.Fatal("issued session invalid")
	}

	sent := runtime.sentTexts()
	if len(sent) != 1 || !strings.Contains(sent[0], "192.0.2.1") {
		t.Fatalf("owner was not told about the login: %+v", sent)
	}
	if strings.Contains(sent[0], code) {
		t.Fatalf("receipt echoed the code back: %q", sent[0])
	}

	if ownerPrivateMessage(handler, code) {
		t.Fatal("single-use code was accepted twice")
	}
	rec = pollOwnerPairing(t, router, pollToken)
	if !strings.Contains(rec.Body.String(), `"expired":true`) {
		t.Fatalf("poll token reuse = %d: %s", rec.Code, rec.Body.String())
	}
}

// 网页没能自动跳转时，主人可以把私聊发过的验证码填回来直接兑换会话。
func TestOwnerPairingClaimByCode(t *testing.T) {
	runtime := ownerLoginRuntime()
	router, manager, handler := newOwnerLoginTestRouter(t, runtime)

	code, _ := createOwnerPairing(t, router)
	// 还没在私聊里确认的话，先填是不给过的。
	if rec := claimOwnerPairing(router, code); rec.Code != http.StatusUnauthorized {
		t.Fatalf("claim before approval = %d: %s", rec.Code, rec.Body.String())
	}
	if !ownerPrivateMessage(handler, code) {
		t.Fatal("owner code was not consumed")
	}

	rec := claimOwnerPairing(router, code)
	if rec.Code != http.StatusOK {
		t.Fatalf("claim = %d: %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	token := strings.TrimPrefix(strings.Split(cookie, ";")[0], authCookieName+"=")
	if !manager.Authenticate(token) {
		t.Fatal("issued session invalid")
	}
	if rec := claimOwnerPairing(router, code); rec.Code != http.StatusUnauthorized {
		t.Fatalf("claim reuse = %d: %s", rec.Code, rec.Body.String())
	}
}

// 6 位验证码必须限流，否则可以靠穷举抢走一个已确认但还没被网页取走的配对。
func TestOwnerPairingClaimIsThrottled(t *testing.T) {
	runtime := ownerLoginRuntime()
	router, _, _ := newOwnerLoginTestRouter(t, runtime)

	for i := 0; i <= authThrottleFreeAttempts; i++ {
		if rec := claimOwnerPairing(router, "000000"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := claimOwnerPairing(router, "000000")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("throttled claim = %d, retry-after = %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// 对不上任何配对的消息要交回对话逻辑，别把主人正常聊天里的数字吞掉。
func TestOwnerPairingLeavesOrdinaryMessagesAlone(t *testing.T) {
	runtime := ownerLoginRuntime()
	router, _, handler := newOwnerLoginTestRouter(t, runtime)
	code, _ := createOwnerPairing(t, router)

	other := "000000"
	if code == other {
		other = "999999"
	}
	for _, text := range []string{other, "今天天气怎么样", "12345", "1234567"} {
		if ownerPrivateMessage(handler, text) {
			t.Fatalf("swallowed ordinary message %q", text)
		}
	}
	if sent := runtime.sentTexts(); len(sent) != 0 {
		t.Fatalf("ordinary messages triggered a reply: %+v", sent)
	}
}

// 非主人、非私聊的消息都不能推动配对。
func TestOwnerPairingIgnoresOtherSenders(t *testing.T) {
	runtime := ownerLoginRuntime()
	router, _, handler := newOwnerLoginTestRouter(t, runtime)
	code, _ := createOwnerPairing(t, router)

	if handler.ConsumePrivateMessage(context.Background(), assistant.MessageEvent{
		Kind: assistant.EventKindGroup, UserID: "10001",
	}, code) {
		t.Fatal("group message advanced pairing")
	}
	if handler.ConsumePrivateMessage(context.Background(), assistant.MessageEvent{
		Kind: assistant.EventKindPrivate, UserID: "20002",
	}, code) {
		t.Fatal("non-owner message advanced pairing")
	}
}

func TestOwnerMessageDeliverySupportsTelegram(t *testing.T) {
	action, params, err := ownerMessageDelivery(assistant.BotConfig{
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

func TestOwnerLoginUnavailableWhenDisabled(t *testing.T) {
	runtime := &fakeOwnerRuntime{cfg: assistant.BotConfig{
		Platform: assistant.PlatformOneBotV11, OwnerID: "10001",
	}}
	router, _, _ := newOwnerLoginTestRouter(t, runtime)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/owner/pair", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("disabled pairing = %d", rec.Code)
	}
	if rec := claimOwnerPairing(router, "123456"); rec.Code != http.StatusBadRequest {
		t.Fatalf("disabled claim = %d", rec.Code)
	}

	runtime = &fakeOwnerRuntime{cfg: assistant.BotConfig{
		Platform: assistant.PlatformOneBotV11, OwnerLoginEnabled: true,
	}}
	router, _, _ = newOwnerLoginTestRouter(t, runtime)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/owner/status", nil))
	if !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("status = %s", rec.Body.String())
	}
}
