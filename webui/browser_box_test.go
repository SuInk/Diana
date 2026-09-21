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

	"github.com/SuInk/diana/model/browserbox"

	"github.com/gin-gonic/gin"
)

type memoryBrowserBoxStore struct {
	mu  sync.Mutex
	doc browserbox.Document
	ok  bool
}

func (s *memoryBrowserBoxStore) LoadBrowserBox(context.Context) (browserbox.Document, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc, s.ok, nil
}

func (s *memoryBrowserBoxStore) SaveBrowserBox(_ context.Context, doc browserbox.Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc = doc
	s.ok = true
	return nil
}

func newBrowserBoxRouter(t *testing.T) (*gin.Engine, *browserbox.Manager) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := browserbox.New(context.Background(), &memoryBrowserBoxStore{}, t.TempDir())
	handler := NewBrowserBoxHandler(manager)
	router := gin.New()
	handler.Register(router)
	return router, manager
}

// 没启用时状态接口要如实说没跑，而不是 500。
func TestBrowserBoxStatusReportsStopped(t *testing.T) {
	router, _ := newBrowserBoxRouter(t)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browser-box/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态接口应成功，得到 %d", recorder.Code)
	}
	var status browserbox.Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("解析状态失败：%v", err)
	}
	if status.Running || status.Settings.Enabled {
		t.Fatalf("默认应是关着的：%+v", status)
	}
}

// 配置接口要把黑名单规范化后存下来。
func TestBrowserBoxSettingsRoundTrip(t *testing.T) {
	router, manager := newBrowserBoxRouter(t)
	body := strings.NewReader(`{"enabled":false,"denied_hosts":["Blocked.example.com"],"window_width":1024,"window_height":768}`)
	request := httptest.NewRequest(http.MethodPut, "/api/browser-box/settings", body)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("保存配置应成功，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	settings := manager.Settings()
	if settings.WindowWidth != 1024 || settings.WindowHeight != 768 {
		t.Fatalf("尺寸没存住：%+v", settings)
	}
	if len(settings.DeniedHosts) != 1 || settings.DeniedHosts[0] != "blocked.example.com" {
		t.Fatalf("黑名单没规范化：%+v", settings)
	}
}

// 浏览器没跑的时候，标签页和实时画面都该明确说「没运行」。
func TestBrowserBoxEndpointsRequireRunningBrowser(t *testing.T) {
	router, _ := newBrowserBoxRouter(t)
	for _, path := range []string{"/api/browser-box/tabs", "/api/browser-box/live"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s 在浏览器没跑时应报 503，得到 %d", path, recorder.Code)
		}
	}
}

// 接管开关要能切，且切完模型那一侧立刻拿不到地址。
func TestBrowserBoxTakeoverToggle(t *testing.T) {
	router, manager := newBrowserBoxRouter(t)
	request := httptest.NewRequest(http.MethodPost, "/api/browser-box/takeover", strings.NewReader(`{"active":true}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("切接管应成功，得到 %d", recorder.Code)
	}
	if !manager.Takeover() {
		t.Fatal("接管没生效")
	}
	if manager.AgentCDPURL() != "" {
		t.Fatal("接管时模型不该拿到地址")
	}
}

// 跨站页面不能连实时画面：那等于能看你的浏览器。
func TestSameOriginWebSocketRejectsCrossSite(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/browser-box/live", nil)
	request.Host = "diana.local"
	request.Header.Set("Origin", "https://evil.example.com")
	if sameOriginWebSocket(request) {
		t.Fatal("跨站来源应被拒")
	}
	request.Header.Set("Origin", "http://diana.local")
	if !sameOriginWebSocket(request) {
		t.Fatal("同源应放行")
	}
}
