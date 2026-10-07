package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestAgentBrowserRejectsNonHTTPCDPAddress(t *testing.T) {
	if err := validateCDPURL("file:///etc/passwd"); err == nil {
		t.Fatal("非 http 地址被放行了")
	}
	if err := validateCDPURL("ws://127.0.0.1:9222"); err == nil {
		t.Fatal("ws 地址被放行了")
	}
	// 空表示回到默认地址，由 WithDefaults 填，不该报错。
	if err := validateCDPURL("  "); err != nil {
		t.Fatalf("空地址 = %v", err)
	}
	if err := validateCDPURL("http://127.0.0.1:9222"); err != nil {
		t.Fatalf("正常地址 = %v", err)
	}
}

// 连不上时要把失败原文摆到界面上，而不是等模型某次调用失败才发现地址是错的。
func TestAgentBrowserTestReportsFailureInline(t *testing.T) {
	r := assistant.NewRuntime(assistant.BotConfig{}, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	h := NewBotHandler(context.Background(), struct{ BotRuntime }{r})
	router := botTestRouter(h)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/assistant/agent-browser/test", strings.NewReader(`{"profile_id":"bot-a","cdp_url":"http://127.0.0.1:1"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d", response.Code)
	}
	var result struct {
		Connected bool   `json:"connected"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Connected || strings.TrimSpace(result.Error) == "" {
		t.Fatalf("探测结果 = %+v", result)
	}
}

// 列表是界面上「接上之后模型能用什么」那句话的来源，缺一个就是承诺不符。
func TestAgentBrowserListsTheInteractiveTools(t *testing.T) {
	for _, name := range []string{"browser_open", "browser_text", "browser_click", "browser_type", "browser_screenshot"} {
		found := false
		for _, listed := range interactiveBrowserTools {
			if listed == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("工具 %q 不在列表里: %#v", name, interactiveBrowserTools)
		}
	}
}

func TestAgentBrowserScreenshotAccessPersistsWithoutChangingOtherSettings(t *testing.T) {
	cfg := assistant.BotConfig{ID: "bot-a", OwnerID: "owner", OneBotAccessToken: "saved-secret", SystemPrompt: "saved persona"}.WithDefaults()
	runtime := assistant.NewRuntime(cfg, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	h := NewBotHandler(context.Background(), runtime)
	h.SetProfileStore(NewMemoryBotProfileStoreFromSet(assistant.ProfileSet{Profiles: []assistant.BotConfig{cfg, {ID: "bot-b"}}}))
	router := botTestRouter(h)
	post := func(body string, status int) {
		t.Helper()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/assistant/agent-browser", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("save status=%d: %s", response.Code, response.Body.String())
		}
	}
	post(`{"profile_id":"bot-a","cdp_url":"http://127.0.0.1:9223","timeout_ms":20000,"screenshot_access":{"mode":"whitelist","allowed_users":[" member ","member",""],"allowed_hosts":[" Example.COM ","example.com"],"allowed_groups":[" group ","group"]}}`, http.StatusOK)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/assistant/agent-browser?profile=bot-a", nil))
	var result struct {
		ScreenshotAccess assistant.BrowserScreenshotAccess `json:"screenshot_access"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	want := assistant.BrowserScreenshotAccess{Mode: assistant.BrowserScreenshotWhitelist, AllowedUsers: []string{"member"}, AllowedHosts: []string{"example.com"}, AllowedGroups: []string{"group"}}
	if !reflect.DeepEqual(result.ScreenshotAccess, want) {
		t.Fatalf("read access=%#v", result.ScreenshotAccess)
	}
	// 老客户端只更新接入参数时，不能重置现有截图授权。
	post(`{"profile_id":"bot-a","cdp_url":"http://127.0.0.1:9224","timeout_ms":21000}`, http.StatusOK)
	saved, _ := h.profiles.Profiles().ConfigForProfile("bot-a")
	if !reflect.DeepEqual(saved.AgentBrowserScreenshotAccess, want) || saved.OneBotAccessToken != "saved-secret" || saved.SystemPrompt != "saved persona" {
		t.Fatal("saving browser settings changed the access policy or unrelated configuration")
	}
	other, _ := h.profiles.Profiles().ConfigForProfile("bot-b")
	if other.AgentBrowserScreenshotAccess.Mode != assistant.BrowserScreenshotOwnerOnly {
		t.Fatal("screenshot access leaked into another bot")
	}
	post(`{"profile_id":"bot-a","screenshot_access":{"mode":"everyone"}}`, http.StatusBadRequest)
	post(`{"profile_id":"bot-a","screenshot_access":{"mode":"all"}}`, http.StatusBadRequest)
	post(`{"profile_id":"bot-a","screenshot_access":{"mode":"whitelist","allowed_hosts":["*.example.com"]}}`, http.StatusBadRequest)
	post(`{"profile_id":"bot-a","screenshot_access":{"mode":"whitelist","allowed_hosts":["https://example.com"]}}`, http.StatusBadRequest)
}

func TestAgentBrowserOperationAllowlistPersistsAndValidates(t *testing.T) {
	cfg := assistant.BotConfig{ID: "bot-a", SystemPrompt: "saved persona"}.WithDefaults()
	runtime := assistant.NewRuntime(cfg, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	h := NewBotHandler(context.Background(), runtime)
	h.SetProfileStore(NewMemoryBotProfileStoreFromSet(assistant.ProfileSet{Profiles: []assistant.BotConfig{cfg, {ID: "bot-b"}}}))
	router := botTestRouter(h)
	post := func(body string, status int) {
		t.Helper()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/assistant/agent-browser", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	post(`{"profile_id":"bot-a","operation_access":{"mode":"whitelist","allowed_users":[" member ","member"],"allowed_hosts":[" EXAMPLE.COM ","login.example.com"]}}`, http.StatusOK)
	want := assistant.BrowserOperationAccess{Mode: "whitelist", AllowedUsers: []string{"member"}, AllowedHosts: []string{"example.com", "login.example.com"}}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/assistant/agent-browser?profile=bot-a", nil))
	var result struct {
		OperationAccess assistant.BrowserOperationAccess `json:"operation_access"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.OperationAccess, want) {
		t.Fatalf("read=%#v", result.OperationAccess)
	}
	post(`{"profile_id":"bot-a","timeout_ms":25000}`, http.StatusOK)
	saved, _ := h.profiles.Profiles().ConfigForProfile("bot-a")
	if !reflect.DeepEqual(saved.AgentBrowserOperationAccess, want) || saved.SystemPrompt != "saved persona" {
		t.Fatal("legacy save reset operation policy or persona")
	}
	other, _ := h.profiles.Profiles().ConfigForProfile("bot-b")
	if other.AgentBrowserOperationAccess.Mode != "owner_only" {
		t.Fatal("operation policy leaked between bots")
	}
	for _, body := range []string{
		`{"profile_id":"bot-a","operation_access":{"mode":"all"}}`,
		`{"profile_id":"bot-a","operation_access":{"mode":"whitelist","allowed_hosts":["*.example.com"]}}`,
		`{"profile_id":"bot-a","operation_access":{"mode":"whitelist","allowed_hosts":["https://example.com"]}}`,
	} {
		post(body, http.StatusBadRequest)
	}
}
