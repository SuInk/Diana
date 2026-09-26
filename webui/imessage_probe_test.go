package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func probeIMessage(t *testing.T, stored assistant.BotConfig, body string) map[string]any {
	t.Helper()
	r := assistant.NewRuntime(stored, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	h := NewBotHandler(context.Background(), struct{ BotRuntime }{r})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/assistant/imessage/test", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	botTestRouter(h).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestIMessageProbeUsesStoredPasswordOnlyForTheStoredServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/server/info" || r.URL.Query().Get("password") != "bb-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":401,"message":"Unauthorized","error":{"type":"Authentication Error","message":"Invalid password"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":200,"message":"Success","data":{"server_version":"1.9.9","private_api":false,"detected_imessage":"diana@icloud.com"}}`))
	}))
	defer server.Close()
	stored := assistant.BotConfig{ID: "bot-a", Platform: assistant.PlatformIMessage, IMessageServerURL: server.URL, IMessagePassword: "bb-secret"}

	result := probeIMessage(t, stored, `{"profile_id":"bot-a"}`)
	if result["connected"] != true || result["detected_imessage"] != "diana@icloud.com" {
		t.Fatalf("probe with stored credentials = %v", result)
	}

	// 地址改成别处时，已保存的密码不能跟过去。
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("password") != "" {
			t.Errorf("stored password was sent to another server")
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer other.Close()
	result = probeIMessage(t, stored, `{"profile_id":"bot-a","server_url":"`+other.URL+`"}`)
	if result["connected"] != false || result["error"] == "" {
		t.Fatalf("probe against another server = %v", result)
	}
}
