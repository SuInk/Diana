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
