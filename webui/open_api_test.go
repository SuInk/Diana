// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

type memoryAPIKeyStore struct {
	set   storage.WebUIAPIKeySet
	saved bool
}

// LoadWebUIAPIKeys 返回内存里的密钥集合。
func (m *memoryAPIKeyStore) LoadWebUIAPIKeys(context.Context) (storage.WebUIAPIKeySet, bool, error) {
	return m.set, m.saved, nil
}

// SaveWebUIAPIKeys 保存密钥集合。
func (m *memoryAPIKeyStore) SaveWebUIAPIKeys(_ context.Context, set storage.WebUIAPIKeySet) error {
	m.set = set
	m.saved = true
	return nil
}

type fakePusher struct {
	targets []assistant.ExternalMessageTarget
	texts   []string
	err     error
	status  assistant.RuntimeStatus
}

// PushExternalMessage 记录一次推送调用。
func (p *fakePusher) PushExternalMessage(_ context.Context, target assistant.ExternalMessageTarget, text string) error {
	if p.err != nil {
		return p.err
	}
	p.targets = append(p.targets, target)
	p.texts = append(p.texts, text)
	return nil
}

// Status 返回预置的运行时状态。
func (p *fakePusher) Status() assistant.RuntimeStatus {
	return p.status
}

func newOpenAPITestRouter(t *testing.T) (*gin.Engine, *OpenAPIHandler, *memoryAPIKeyStore, *fakePusher) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := &memoryAPIKeyStore{}
	pusher := &fakePusher{}
	handler := NewOpenAPIHandler(NewOpenAPIKeyManager(store), pusher)
	router := gin.New()
	handler.Register(router)
	return router, handler, store, pusher
}

func createOpenAPITestKey(t *testing.T, router *gin.Engine, name string) (id, token string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/openapi/keys", strings.NewReader(`{"name":"`+name+`"}`))
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create key status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Key   OpenAPIKeyInfo `json:"key"`
		Token string         `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Key.ID, payload.Token
}

func TestOpenAPIKeyLifecycle(t *testing.T) {
	router, _, store, _ := newOpenAPITestRouter(t)
	id, token := createOpenAPITestKey(t, router, "ci-notify")
	if !strings.HasPrefix(token, "diana_") {
		t.Fatalf("token prefix = %q", token)
	}
	if !store.saved || len(store.set.Keys) != 1 {
		t.Fatalf("key not persisted: %#v", store.set)
	}
	if store.set.Keys[0].TokenHash == token || store.set.Keys[0].TokenHash == "" {
		t.Fatalf("plaintext must not be persisted: %#v", store.set.Keys[0])
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/openapi/keys", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "ci-notify") || strings.Contains(body, token) || strings.Contains(body, "token_hash") {
		t.Fatalf("list leaked secrets or missed key: %s", body)
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/openapi/keys/"+id, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("revoke status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if len(store.set.Keys) != 0 {
		t.Fatalf("key not removed from store: %#v", store.set)
	}

	// 吊销后的密钥立即失效。
	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/openapi/v1/messages", strings.NewReader(`{"user_id":"1","text":"hi"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key status = %d", recorder.Code)
	}
}

func TestOpenAPIKeyManagerReloadsFromStore(t *testing.T) {
	store := &memoryAPIKeyStore{}
	manager := NewOpenAPIKeyManager(store)
	_, token, err := manager.Create("bridge")
	if err != nil {
		t.Fatal(err)
	}
	reloaded := NewOpenAPIKeyManager(store)
	if _, ok := reloaded.Authenticate(token); !ok {
		t.Fatal("key should survive a manager reload")
	}
}

func TestOpenAPIPushMessage(t *testing.T) {
	router, _, _, pusher := newOpenAPITestRouter(t)
	_, token := createOpenAPITestKey(t, router, "ci-notify")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/openapi/v1/messages",
		strings.NewReader(`{"platform":"telegram","group_id":"123","user_id":"456","text":"build passed"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("push status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if len(pusher.targets) != 1 {
		t.Fatalf("pusher targets = %#v", pusher.targets)
	}
	target := pusher.targets[0]
	if target.Platform != "telegram" || target.GroupID != "123" || target.UserID != "456" {
		t.Fatalf("target = %#v", target)
	}
	if pusher.texts[0] != "build passed" {
		t.Fatalf("text = %q", pusher.texts[0])
	}
}

func TestOpenAPIPushRejectsBadRequests(t *testing.T) {
	router, _, _, pusher := newOpenAPITestRouter(t)
	_, token := createOpenAPITestKey(t, router, "ci-notify")

	cases := []struct {
		name       string
		body       string
		token      string
		wantStatus int
	}{
		{"missing token", `{"user_id":"1","text":"hi"}`, "", http.StatusUnauthorized},
		{"wrong token", `{"user_id":"1","text":"hi"}`, "diana_deadbeef", http.StatusUnauthorized},
		{"missing target", `{"text":"hi"}`, token, http.StatusBadRequest},
		{"empty text", `{"user_id":"1","text":"  "}`, token, http.StatusBadRequest},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/openapi/v1/messages", strings.NewReader(testCase.body))
		if testCase.token != "" {
			request.Header.Set("Authorization", "Bearer "+testCase.token)
		}
		router.ServeHTTP(recorder, request)
		if recorder.Code != testCase.wantStatus {
			t.Fatalf("%s: status = %d, want %d", testCase.name, recorder.Code, testCase.wantStatus)
		}
	}
	if len(pusher.targets) != 0 {
		t.Fatalf("no push should reach the runtime: %#v", pusher.targets)
	}
}

func TestOpenAPIPushReportsDeliveryFailure(t *testing.T) {
	router, _, _, pusher := newOpenAPITestRouter(t)
	_, token := createOpenAPITestKey(t, router, "ci-notify")
	pusher.err = context.DeadlineExceeded

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/openapi/v1/messages", strings.NewReader(`{"user_id":"1","text":"hi"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("delivery failure status = %d", recorder.Code)
	}
}

func TestOpenAPIRateLimit(t *testing.T) {
	router, handler, _, _ := newOpenAPITestRouter(t)
	_, token := createOpenAPITestKey(t, router, "ci-notify")
	handler.limiter = newOpenAPIRateLimiter(2, time.Minute)

	statuses := make([]int, 0, 3)
	for range 3 {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/openapi/v1/messages", strings.NewReader(`{"user_id":"1","text":"hi"}`))
		request.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(recorder, request)
		statuses = append(statuses, recorder.Code)
	}
	if statuses[0] != http.StatusOK || statuses[1] != http.StatusOK || statuses[2] != http.StatusTooManyRequests {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestOpenAPIStatusListsChannels(t *testing.T) {
	router, _, _, pusher := newOpenAPITestRouter(t)
	_, token := createOpenAPITestKey(t, router, "ci-notify")
	pusher.status = assistant.RuntimeStatus{
		Running: true,
		Channels: []assistant.ChannelStatus{
			{ProfileID: "bot-1", Platform: "telegram", Name: "TG", Connected: true},
		},
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/openapi/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"running":true`) || !strings.Contains(body, `"platform":"telegram"`) {
		t.Fatalf("status body = %s", body)
	}

	// 状态接口同样只认有效密钥。
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/openapi/v1/status", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", recorder.Code)
	}
}
