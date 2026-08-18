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

func (f *fakeOwnerRuntime) setOwnerID(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg.OwnerID = id
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

func boolPtr(value bool) *bool {
	return &value
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

func ownerPrivateMessage(handler *OwnerLoginHandler, text string) bool {
	return handler.ConsumePrivateMessage(context.Background(), assistant.MessageEvent{
		Kind: assistant.EventKindPrivate, UserID: "10001",
	}, text)
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
	texts := runtime.sentTexts()
	if payload.ChallengeToken == "" || len(texts) != 1 {
		t.Fatalf("challenge payload = %+v, sent = %+v", payload, texts)
	}
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(texts[0])
	if code == "" || strings.Contains(rec.Body.String(), code) {
		t.Fatalf("code delivery leaked or missing: response=%s message=%q", rec.Body.String(), texts[0])
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

func ownerCodeLoginRuntime() *fakeOwnerRuntime {
	return &fakeOwnerRuntime{cfg: assistant.BotConfig{
		Platform: assistant.PlatformOneBotV11, OwnerID: "10001", Name: "Diana",
		OwnerLoginEnabled: true, OwnerLoginCodeEnabled: boolPtr(true),
	}}
}

// 验证码下发默认关闭，私聊确认默认开启：两个开关都没显式配置时，status 必须
// 只放行私聊确认这一种。
func TestOwnerLoginMethodDefaults(t *testing.T) {
	runtime := &fakeOwnerRuntime{cfg: assistant.BotConfig{
		Platform: assistant.PlatformOneBotV11, OwnerID: "10001", OwnerLoginEnabled: true,
	}}
	router, _, _ := newOwnerLoginTestRouter(t, runtime)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/owner/status", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `"available":true`) ||
		!strings.Contains(body, `"pair_available":true`) ||
		!strings.Contains(body, `"code_available":false`) {
		t.Fatalf("status = %s", body)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/owner/challenge", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("challenge should be disabled by default = %d: %s", rec.Code, rec.Body.String())
	}
	if sent := runtime.sentTexts(); len(sent) != 0 {
		t.Fatalf("disabled challenge still messaged the owner: %+v", sent)
	}
}

// 关掉私聊确认后，配对端点和私聊拦截都必须失效。
func TestOwnerLoginPairCanBeDisabled(t *testing.T) {
	runtime := ownerCodeLoginRuntime()
	runtime.cfg.OwnerLoginPairEnabled = boolPtr(false)
	router, _, handler := newOwnerLoginTestRouter(t, runtime)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/owner/pair", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("disabled pairing = %d: %s", rec.Code, rec.Body.String())
	}
	if ownerPrivateMessage(handler, "123456") {
		t.Fatal("disabled pairing still consumed a private message")
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/owner/status", nil))
	if !strings.Contains(rec.Body.String(), `"pair_available":false`) {
		t.Fatalf("status = %s", rec.Body.String())
	}
}

// 主人发回验证码只是让机器人回问来源，必须再回一次「确认」才真正放行。
func TestOwnerPairingRequiresSecondConfirmation(t *testing.T) {
	runtime := &fakeOwnerRuntime{cfg: assistant.BotConfig{
		Platform: assistant.PlatformOneBotV11, OwnerID: "10001", OwnerLoginEnabled: true,
	}}
	router, manager, handler := newOwnerLoginTestRouter(t, runtime)

	code, pollToken := createOwnerPairing(t, router)
	if !ownerPrivateMessage(handler, code) {
		t.Fatal("owner code was not consumed")
	}

	rec := pollOwnerPairing(t, router, pollToken)
	if !strings.Contains(rec.Body.String(), `"approved":false`) ||
		!strings.Contains(rec.Body.String(), `"awaiting_confirm":true`) {
		t.Fatalf("pairing approved without confirmation: %s", rec.Body.String())
	}
	sent := runtime.sentTexts()
	if len(sent) != 1 || !strings.Contains(sent[0], "192.0.2.1") || !strings.Contains(sent[0], "确认") {
		t.Fatalf("owner was not told who is logging in: %+v", sent)
	}
	// 验证码只在网页上显示，机器人不把它回声一遍。
	if strings.Contains(sent[0], code) {
		t.Fatalf("confirmation prompt echoed the code back: %q", sent[0])
	}

	if !ownerPrivateMessage(handler, "确认 "+code) {
		t.Fatal("confirmation was not consumed")
	}
	rec = pollOwnerPairing(t, router, pollToken)
	if !strings.Contains(rec.Body.String(), `"approved":true`) {
		t.Fatalf("approved poll = %d: %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	token := strings.TrimPrefix(strings.Split(cookie, ";")[0], authCookieName+"=")
	if !manager.Authenticate(token) {
		t.Fatal("issued session invalid")
	}
}

// 主人主动拒绝时配对立刻作废，不用干等过期。
func TestOwnerPairingCanBeCancelled(t *testing.T) {
	runtime := &fakeOwnerRuntime{cfg: assistant.BotConfig{
		Platform: assistant.PlatformOneBotV11, OwnerID: "10001", OwnerLoginEnabled: true,
	}}
	router, _, handler := newOwnerLoginTestRouter(t, runtime)

	code, pollToken := createOwnerPairing(t, router)
	if !ownerPrivateMessage(handler, code) {
		t.Fatal("owner code was not consumed")
	}
	if !ownerPrivateMessage(handler, "取消 "+code) {
		t.Fatal("cancellation was not consumed")
	}
	rec := pollOwnerPairing(t, router, pollToken)
	if !strings.Contains(rec.Body.String(), `"expired":true`) {
		t.Fatalf("cancelled pairing still alive: %s", rec.Body.String())
	}
	if ownerPrivateMessage(handler, "确认 "+code) {
		t.Fatal("cancelled pairing accepted a confirmation")
	}
}

// 没有待确认的登录时，「确认」这类日常用词不能被登录流程吞掉。
func TestOwnerPairingLeavesOrdinaryMessagesAlone(t *testing.T) {
	runtime := &fakeOwnerRuntime{cfg: assistant.BotConfig{
		Platform: assistant.PlatformOneBotV11, OwnerID: "10001", OwnerLoginEnabled: true,
	}}
	_, _, handler := newOwnerLoginTestRouter(t, runtime)

	for _, text := range []string{"确认", "取消", "123456", "今天天气怎么样"} {
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
	runtime := &fakeOwnerRuntime{cfg: assistant.BotConfig{
		Platform: assistant.PlatformOneBotV11, OwnerID: "10001", OwnerLoginEnabled: true,
	}}
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

func TestOwnerChallengeLoginFullFlow(t *testing.T) {
	runtime := ownerCodeLoginRuntime()
	router, manager, _ := newOwnerLoginTestRouter(t, runtime)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/owner/status", nil))
	if !strings.Contains(rec.Body.String(), `"code_available":true`) {
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

func TestOwnerChallengeExpiresWhenOwnerChanges(t *testing.T) {
	runtime := ownerCodeLoginRuntime()
	router, _, _ := newOwnerLoginTestRouter(t, runtime)
	code, challengeToken := createOwnerChallenge(t, router, runtime)
	runtime.setOwnerID("20002")

	rec := verifyOwnerChallenge(router, challengeToken, code)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("challenge survived owner change = %d: %s", rec.Code, rec.Body.String())
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
