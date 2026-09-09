package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type extensionTestRuntime struct {
	BotRuntime
	request agent.ExtensionAdminRequest
}

func (r *extensionTestRuntime) AdministerExtensions(_ context.Context, request agent.ExtensionAdminRequest) (any, error) {
	r.request = request
	return map[string]any{"items": []any{}}, nil
}

func TestExtensionEndpointRejectsUnavailableRuntime(t *testing.T) {
	r := assistant.NewRuntime(assistant.BotConfig{}, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	h := NewBotHandler(context.Background(), struct{ BotRuntime }{r})
	router := botTestRouter(h)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/assistant/extensions", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", response.Code)
	}
}

func TestExtensionEndpointUsesRequestAndBoundsBody(t *testing.T) {
	r := &extensionTestRuntime{BotRuntime: assistant.NewRuntime(assistant.BotConfig{}, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)}
	router := botTestRouter(NewBotHandler(context.Background(), r))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/assistant/extensions", strings.NewReader(`{"operation":"enabled","kind":"skill","name":"demo","profile_id":"a","enabled":false}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != 200 || r.request.ProfileID != "a" || r.request.Enabled || r.request.Operation != "enabled" {
		t.Fatalf("request scope lost: %d %+v", response.Code, r.request)
	}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/assistant/extensions", strings.NewReader(`{"content":"`+strings.Repeat("x", 4<<20)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != 400 {
		t.Fatal("unbounded extension request")
	}
}

func TestExtensionsRequireConsoleAuthentication(t *testing.T) {
	auth := NewAuthManager(&memoryAuthStore{})
	if _, err := auth.Bootstrap("admin", "test-password"); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(auth.Middleware())
	runtime := &extensionTestRuntime{BotRuntime: assistant.NewRuntime(assistant.BotConfig{}, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)}
	NewBotHandler(context.Background(), runtime).Register(router)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(method, "/api/assistant/extensions", strings.NewReader(`{"operation":"test","kind":"mcp","name":"example"}`))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous extension access: %s %d", method, response.Code)
		}
	}
	if runtime.request.Operation != "" {
		t.Fatal("anonymous request reached extension manager")
	}
}
