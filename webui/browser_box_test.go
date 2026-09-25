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
	"github.com/SuInk/diana/model/storage"

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
	for _, path := range []string{"/api/browser-box/tabs?bot=bot-a", "/api/browser-box/live?bot=bot-a"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s 在浏览器没跑时应报 503，得到 %d", path, recorder.Code)
		}
	}
}

// 每台机器人各有一份登录态：进程相关的接口不指明机器人就拒掉，不替用户猜一台。
func TestBrowserBoxEndpointsRequireBot(t *testing.T) {
	router, _ := newBrowserBoxRouter(t)
	for _, path := range []string{"/api/browser-box/tabs", "/api/browser-box/live"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s 没带机器人时应报 400，得到 %d", path, recorder.Code)
		}
	}
}

// 接管开关要能切，且切完模型那一侧立刻拿不到地址。
func TestBrowserBoxTakeoverToggle(t *testing.T) {
	router, manager := newBrowserBoxRouter(t)
	if _, err := manager.SetSettings(context.Background(), browserbox.Settings{Enabled: true}); err != nil {
		t.Fatalf("打开内置浏览器失败：%v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/browser-box/takeover?bot=bot-a", strings.NewReader(`{"active":true}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("切接管应成功，得到 %d", recorder.Code)
	}
	if !manager.Bot("bot-a").Status().Takeover {
		t.Fatal("接管没生效")
	}
	if manager.Bot("bot-b").Status().Takeover {
		t.Fatal("接管只该作用于那一台机器人")
	}
	// 有人开着画面才算真的在接管；人走了机器人要用就直接收回，见 browserbox 的测试。
	detach := manager.Bot("bot-a").AttachViewer()
	defer detach()
	if _, err := manager.Bot("bot-a").Endpoint(context.Background()); err == nil {
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

type recordingAppLog struct {
	mu      sync.Mutex
	entries []storage.AppLogEntry
}

func (r *recordingAppLog) AppendLog(_ context.Context, entry storage.AppLogEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
	return nil
}

func newLiveInputFixture(t *testing.T) (*BrowserBoxHandler, *browserbox.Bot, *gin.Context, *recordingAppLog) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := browserbox.New(context.Background(), &memoryBrowserBoxStore{}, t.TempDir())
	handler := NewBrowserBoxHandler(manager)
	logs := &recordingAppLog{}
	handler.SetLogStore(logs)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/browser-box/live?bot=bot-a", nil)
	return handler, manager.Bot("bot-a"), c, logs
}

// 画面默认只能看：没显式接管时，点击、按键、打字、地址栏、后退刷新一律不送给页面，
// 也不会顺手把浏览器从机器人手里抢走。
func TestLiveInputIgnoredUntilExplicitTakeover(t *testing.T) {
	handler, bot, _, logs := newLiveInputFixture(t)
	for _, message := range liveInputSamples() {
		if handler.claimLiveInput(bot, message) {
			t.Fatalf("没接管时 %+v 不该送给页面", message)
		}
	}
	if bot.Takeover() {
		t.Fatal("在画面上动手不该自己打开接管，接管要点按钮")
	}
	if len(logs.entries) != 0 {
		t.Fatalf("没接管就不该记接管：%+v", logs.entries)
	}
}

// 点了「接管」之后，这些输入才送给页面：点击、拖动、滚动、打字、换网址都靠它们。
func TestLiveInputForwardedDuringTakeover(t *testing.T) {
	handler, bot, _, _ := newLiveInputFixture(t)
	bot.SetTakeover(true)
	defer bot.SetTakeover(false)
	for _, message := range liveInputSamples() {
		if !handler.claimLiveInput(bot, message) {
			t.Fatalf("接管期间 %+v 应送给页面", message)
		}
	}
}

func liveInputSamples() []liveMessage {
	return []liveMessage{
		{Type: "mouse", Mouse: &browserbox.MouseEvent{Type: "mousePressed", X: 10, Y: 10, Button: "left", ClickCount: 1}},
		{Type: "mouse", Mouse: &browserbox.MouseEvent{Type: "mouseMoved", X: 10, Y: 10}},
		{Type: "mouse", Mouse: &browserbox.MouseEvent{Type: "mouseWheel", X: 10, Y: 10, DeltaY: -120}},
		{Type: "mouse", Mouse: &browserbox.MouseEvent{Type: "mouseReleased", X: 10, Y: 10, Button: "left"}},
		// 焦点留在画面上时 Cmd+Tab 切窗口，先按下的就是 Meta。
		{Type: "key", Key: &browserbox.KeyEvent{Type: "rawKeyDown", Key: "Meta", Modifiers: 4}},
		{Type: "key", Key: &browserbox.KeyEvent{Type: "keyDown", Key: "Enter"}},
		{Type: "key", Key: &browserbox.KeyEvent{Type: "keyUp", Key: "Enter"}},
		{Type: "text", Text: "你好"},
		{Type: "navigate", URL: "https://example.com"},
		{Type: "reload"},
		{Type: "back"},
	}
}
