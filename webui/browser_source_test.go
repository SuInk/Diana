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

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/browserbox"
	"github.com/SuInk/diana/model/browserctl"
	"github.com/SuInk/diana/model/browsersource"

	"github.com/gin-gonic/gin"
)

type memoryBrowserSourceStore struct {
	doc browsersource.Settings
	ok  bool
}

func (s *memoryBrowserSourceStore) LoadBrowserSource(context.Context) (browsersource.Settings, bool, error) {
	return s.doc, s.ok, nil
}

func (s *memoryBrowserSourceStore) SaveBrowserSource(_ context.Context, doc browsersource.Settings) error {
	s.doc, s.ok = doc, true
	return nil
}

func putBrowserSource(t *testing.T, router *gin.Engine, body string) browserSourceState {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/api/browser-source", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT %s 应成功，得到 %d：%s", body, recorder.Code, recorder.Body.String())
	}
	var state browserSourceState
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// 两个开关各管各的；优先级单独落盘，这一轮按优先级取第一个用得上的。
func TestBrowserSourceSwitchesAreIndependent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	box := browserbox.New(ctx, &memoryBrowserBoxStore{}, t.TempDir())
	registry := browserctl.NewRegistry(ctx, nil)
	store := &memoryBrowserSourceStore{}
	handler := NewBrowserSourceHandler(ctx, box, registry, browserctl.NewHub(registry), store)
	router := gin.New()
	handler.Register(router)

	// 内置浏览器的进程按需起，打开开关不会在开发机上真拉起 Chrome。
	putBrowserSource(t, router, `{"box_enabled":true}`)
	state := putBrowserSource(t, router, `{"extension_enabled":true}`)
	if !state.Extension.Enabled || !state.Box.Enabled {
		t.Fatalf("两个开关应能同时开着：%+v", state)
	}
	state = putBrowserSource(t, router, `{"order":["extension","box"]}`)
	if strings.Join(state.Order, ",") != "extension,box" || strings.Join(store.doc.Order, ",") != "extension,box" {
		t.Fatalf("优先级没存住：%+v / %+v", state.Order, store.doc)
	}
	// 扩展排在前面但没连上，这一轮落到内置浏览器（前提是本机找得到 Chrome）。
	want := browsersource.Off
	if state.Box.Usable {
		want = browsersource.Box
	}
	if state.Active != want {
		t.Fatalf("扩展没连上时应按顺序落到下一个：%+v", state)
	}
	state = putBrowserSource(t, router, `{"box_enabled":false,"extension_enabled":false}`)
	if state.Box.Enabled || state.Extension.Enabled || state.Active != browsersource.Off {
		t.Fatalf("都关掉后应是不用：%+v", state)
	}
}

type memoryBrowserSandboxStore struct {
	memoryBrowserSourceStore
	mode string
}

func (s *memoryBrowserSandboxStore) LoadBrowserSandbox(context.Context) (string, bool, error) {
	return s.mode, s.mode != "", nil
}

func (s *memoryBrowserSandboxStore) SaveBrowserSandbox(_ context.Context, mode string) error {
	s.mode = mode
	return nil
}

// 沙箱档位默认「仅沙箱」，改了立即生效并落盘，重启后读回来。
func TestBrowserSourceSandboxMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	t.Cleanup(func() { agent.SetBrowserSandboxMode(agent.BrowserSandboxOnly) })
	agent.SetBrowserSandboxMode(agent.BrowserSandboxOnly)
	box := browserbox.New(ctx, &memoryBrowserBoxStore{}, t.TempDir())
	registry := browserctl.NewRegistry(ctx, nil)
	store := &memoryBrowserSandboxStore{}
	handler := NewBrowserSourceHandler(ctx, box, registry, browserctl.NewHub(registry), store)
	router := gin.New()
	handler.Register(router)

	if mode := handler.state().Sandbox.Mode; mode != agent.BrowserSandboxOnly {
		t.Fatalf("默认档位应为仅沙箱，得到 %q", mode)
	}
	state := putBrowserSource(t, router, `{"sandbox":"off"}`)
	if state.Sandbox.Mode != agent.BrowserSandboxOff || state.Sandbox.Active || store.mode != "off" {
		t.Fatalf("关闭沙箱没生效或没落盘：%+v / %q", state.Sandbox, store.mode)
	}

	request := httptest.NewRequest(http.MethodPut, "/api/browser-source", strings.NewReader(`{"sandbox":"never"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || store.mode != "off" {
		t.Fatalf("非法档位应被拒且不改存储：%d %q", recorder.Code, store.mode)
	}

	// 模拟重启：进程内回到默认值，构造 handler 时从存储读回。
	agent.SetBrowserSandboxMode(agent.BrowserSandboxOnly)
	store.mode = "auto"
	NewBrowserSourceHandler(ctx, box, registry, browserctl.NewHub(registry), store)
	if mode := agent.CurrentBrowserSandbox().Mode; mode != agent.BrowserSandboxAuto {
		t.Fatalf("重启后没读回保存的档位：%q", mode)
	}
}
