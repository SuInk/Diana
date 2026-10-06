// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
)

type fakeConn struct {
	mu      sync.Mutex
	sent    []Frame
	closed  bool
	respond func(Frame) *Frame
	deliver func(Frame)
}

func (f *fakeConn) Send(frame Frame) error {
	f.mu.Lock()
	f.sent = append(f.sent, frame)
	respond, deliver := f.respond, f.deliver
	f.mu.Unlock()
	if respond == nil || deliver == nil {
		return nil
	}
	if reply := respond(frame); reply != nil {
		go deliver(*reply)
	}
	return nil
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

type memoryStore struct {
	mu  sync.Mutex
	doc Document
}

func (s *memoryStore) LoadDesktopControl(context.Context) (Document, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc, true, nil
}

func (s *memoryStore) SaveDesktopControl(_ context.Context, doc Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc = doc
	return nil
}

func enabledPolicy() Policy {
	return Policy{Enabled: true, WriteEnabled: false}.WithDefaults()
}

func writePolicy() Policy {
	return Policy{Enabled: true, WriteEnabled: true}.WithDefaults()
}

func f64(v float64) *float64 { return &v }

func newTestHub(t *testing.T, policy Policy, windows []WindowInfo) (*Hub, *Connection, *fakeConn) {
	t.Helper()
	registry := NewRegistry(context.Background(), &memoryStore{})
	if _, err := registry.SetPolicy(context.Background(), policy); err != nil {
		t.Fatalf("设置策略失败：%v", err)
	}
	hub := NewHub(registry)
	conn := &fakeConn{}
	c, _, err := hub.Register(conn, Hello{ProtocolVersion: ProtocolVersion, HelperID: "mac-1", Platform: "macos"}, TokenInfo{ID: "t1"})
	if err != nil {
		t.Fatalf("握手失败：%v", err)
	}
	png := base64.StdEncoding.EncodeToString([]byte("fake-png"))
	conn.respond = func(frame Frame) *Frame {
		switch frame.Op {
		case OpWindowScreenshot:
			return &Frame{Type: FrameResult, ID: frame.ID, Data: rawJSON(ScreenshotPayload{
				WindowID: "w1", Mime: "image/png", Data: png,
			})}
		case OpWindowClick, OpWindowType, OpWindowKey:
			return &Frame{Type: FrameResult, ID: frame.ID, Data: rawJSON(ActionResult{
				WindowID: "w1", Op: frame.Op, OK: true,
			})}
		default:
			return &Frame{Type: FrameResult, ID: frame.ID, Data: rawJSON(map[string]any{"ok": true})}
		}
	}
	conn.deliver = c.HandleFrame
	if windows != nil {
		c.HandleFrame(Frame{Type: FrameWindows, Data: rawJSON(WindowsPayload{Windows: windows})})
	}
	return hub, c, conn
}

func TestWindowsListFiltersUnauthorized(t *testing.T) {
	hub, _, _ := newTestHub(t, Policy{
		Enabled:     true,
		AllowedApps: []string{"com.apple.Safari"},
	}.WithDefaults(), []WindowInfo{
		{ID: "w1", AppName: "Safari", BundleID: "com.apple.Safari", Title: "Home"},
		{ID: "w2", AppName: "Mail", BundleID: "com.apple.Mail", Title: "Inbox"},
	})
	result, err := hub.Dispatch(context.Background(), Command{Op: OpWindowsList})
	if err != nil {
		t.Fatalf("列窗口失败：%v", err)
	}
	var payload WindowsPayload
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(payload.Windows) != 1 || payload.Windows[0].ID != "w1" {
		t.Fatalf("未授权窗口应不可见，得到 %+v", payload.Windows)
	}
}

func TestWindowsListEmptyAllowlistShowsAll(t *testing.T) {
	hub, _, _ := newTestHub(t, enabledPolicy(), []WindowInfo{
		{ID: "w1", AppName: "Safari", BundleID: "com.apple.Safari"},
		{ID: "w2", AppName: "Mail", BundleID: "com.apple.Mail"},
	})
	result, err := hub.Dispatch(context.Background(), Command{Op: OpWindowsList})
	if err != nil {
		t.Fatalf("列窗口失败：%v", err)
	}
	var payload WindowsPayload
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(payload.Windows) != 2 {
		t.Fatalf("空白名单应列出全部，得到 %d", len(payload.Windows))
	}
}

func TestScreenshotDeniedForUnauthorizedWindow(t *testing.T) {
	hub, _, _ := newTestHub(t, Policy{
		Enabled:     true,
		AllowedApps: []string{"com.apple.Safari"},
	}.WithDefaults(), []WindowInfo{
		{ID: "w1", AppName: "Safari", BundleID: "com.apple.Safari"},
		{ID: "w2", AppName: "Mail", BundleID: "com.apple.Mail"},
	})
	_, err := hub.Dispatch(context.Background(), Command{Op: OpWindowScreenshot, WindowID: "w2"})
	if ErrorCode(err) != CodeWindowUnknown {
		t.Fatalf("未授权窗口应报 window_unknown，得到 %v (%s)", err, ErrorCode(err))
	}
}

func TestDispatchDeniedDuringTakeover(t *testing.T) {
	hub, conn, _ := newTestHub(t, enabledPolicy(), []WindowInfo{
		{ID: "w1", AppName: "Safari", BundleID: "com.apple.Safari", Active: true},
	})
	conn.SetTakeover(true, "主人在用")
	_, err := hub.Dispatch(context.Background(), Command{Op: OpWindowsList})
	if ErrorCode(err) != CodeTakeover {
		t.Fatalf("接管时应拒绝，得到 %v (%s)", err, ErrorCode(err))
	}
}

func TestDispatchDisabled(t *testing.T) {
	registry := NewRegistry(context.Background(), &memoryStore{})
	hub := NewHub(registry)
	_, err := hub.Dispatch(context.Background(), Command{Op: OpWindowsList})
	if ErrorCode(err) != CodeDisabled {
		t.Fatalf("未启用应拒绝，得到 %v (%s)", err, ErrorCode(err))
	}
}

func TestScreenshotSucceedsForAllowedWindow(t *testing.T) {
	hub, _, _ := newTestHub(t, enabledPolicy(), []WindowInfo{
		{ID: "w1", AppName: "Safari", BundleID: "com.apple.Safari", Active: true},
	})
	result, err := hub.Dispatch(context.Background(), Command{Op: OpWindowScreenshot, WindowID: "w1"})
	if err != nil {
		t.Fatalf("截图失败：%v", err)
	}
	var payload ScreenshotPayload
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if payload.Data == "" {
		t.Fatal("应带回截图数据")
	}
}

func TestWriteOpsRequireWriteEnabled(t *testing.T) {
	hub, _, _ := newTestHub(t, enabledPolicy(), []WindowInfo{
		{ID: "w1", AppName: "Safari", BundleID: "com.apple.Safari", Active: true},
	})
	_, err := hub.Dispatch(context.Background(), Command{Op: OpWindowClick, WindowID: "w1", X: f64(10), Y: f64(20)})
	if ErrorCode(err) != CodeWriteDisabled {
		t.Fatalf("未开写应拒绝，得到 %v (%s)", err, ErrorCode(err))
	}
}

func TestClickTypeKeyWhenWriteEnabled(t *testing.T) {
	hub, _, _ := newTestHub(t, writePolicy(), []WindowInfo{
		{ID: "w1", AppName: "Safari", BundleID: "com.apple.Safari", Active: true},
	})
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpWindowClick, WindowID: "w1", X: f64(10), Y: f64(20)}); err != nil {
		t.Fatalf("click：%v", err)
	}
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpWindowType, WindowID: "w1", Text: "hello"}); err != nil {
		t.Fatalf("type：%v", err)
	}
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpWindowKey, WindowID: "w1", Key: "Return"}); err != nil {
		t.Fatalf("key：%v", err)
	}
}

func TestWriteDeniedDuringTakeover(t *testing.T) {
	hub, conn, _ := newTestHub(t, writePolicy(), []WindowInfo{
		{ID: "w1", AppName: "Safari", BundleID: "com.apple.Safari", Active: true},
	})
	conn.SetTakeover(true, "主人接管")
	_, err := hub.Dispatch(context.Background(), Command{Op: OpWindowClick, WindowID: "w1", X: f64(1), Y: f64(1)})
	if ErrorCode(err) != CodeTakeover {
		t.Fatalf("接管时应拒绝写操作，得到 %v (%s)", err, ErrorCode(err))
	}
}

func TestClickDeniedForUnauthorizedWindow(t *testing.T) {
	hub, _, _ := newTestHub(t, Policy{
		Enabled: true, WriteEnabled: true, AllowedApps: []string{"com.apple.Safari"},
	}.WithDefaults(), []WindowInfo{
		{ID: "w1", AppName: "Safari", BundleID: "com.apple.Safari"},
		{ID: "w2", AppName: "Mail", BundleID: "com.apple.Mail"},
	})
	_, err := hub.Dispatch(context.Background(), Command{Op: OpWindowClick, WindowID: "w2", X: f64(1), Y: f64(1)})
	if ErrorCode(err) != CodeWindowUnknown {
		t.Fatalf("未授权窗口写操作应拒，得到 %v (%s)", err, ErrorCode(err))
	}
}

func TestClickRequiresCoordinates(t *testing.T) {
	hub, _, _ := newTestHub(t, writePolicy(), []WindowInfo{
		{ID: "w1", AppName: "Safari", BundleID: "com.apple.Safari", Active: true},
	})
	_, err := hub.Dispatch(context.Background(), Command{Op: OpWindowClick, WindowID: "w1"})
	if ErrorCode(err) != CodeBadRequest {
		t.Fatalf("缺坐标应 bad_request，得到 %v (%s)", err, ErrorCode(err))
	}
}
