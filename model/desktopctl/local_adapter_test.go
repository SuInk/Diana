// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAttachLocalMockNoDisplay(t *testing.T) {
	registry := NewRegistry(context.Background(), &memoryStore{})
	if _, err := registry.SetPolicy(context.Background(), writePolicy()); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(registry)
	adapter := &MockAdapter{Windows: []WindowInfo{
		{ID: "1", AppName: "Safari", BundleID: "com.apple.Safari", Active: true},
	}}
	_, _, err := hub.AttachLocal(context.Background(), adapter, Hello{Platform: "macos"}, TokenInfo{ID: "local"})
	if err != nil {
		t.Fatalf("AttachLocal：%v", err)
	}
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpWindowsList}); err != nil {
		t.Fatalf("list：%v", err)
	}
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpWindowScreenshot, WindowID: "1"}); err != nil {
		t.Fatalf("screenshot：%v", err)
	}
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpWindowClick, WindowID: "1", X: f64(5), Y: f64(5)}); err != nil {
		t.Fatalf("click：%v", err)
	}
	if len(adapter.Clicks) != 1 {
		t.Fatalf("应记录一次点击，得到 %d", len(adapter.Clicks))
	}
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpWindowType, WindowID: "1", Text: "hi"}); err != nil {
		t.Fatalf("type：%v", err)
	}
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpWindowKey, WindowID: "1", Key: "Return"}); err != nil {
		t.Fatalf("key：%v", err)
	}
}

func TestMockAdapterPermissionDeniedOnWrite(t *testing.T) {
	registry := NewRegistry(context.Background(), &memoryStore{})
	if _, err := registry.SetPolicy(context.Background(), writePolicy()); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(registry)
	adapter := &MockAdapter{
		Windows:  []WindowInfo{{ID: "1", AppName: "Safari", BundleID: "com.apple.Safari", Active: true}},
		WriteErr: PermissionDenied("Accessibility（辅助功能）"),
	}
	if _, _, err := hub.AttachLocal(context.Background(), adapter, Hello{Platform: "macos"}, TokenInfo{ID: "local"}); err != nil {
		t.Fatal(err)
	}
	_, err := hub.Dispatch(context.Background(), Command{Op: OpWindowClick, WindowID: "1", X: f64(1), Y: f64(1)})
	if ErrorCode(err) != CodePermissionDenied {
		t.Fatalf("应报 permission_denied，得到 %v (%s)", err, ErrorCode(err))
	}
	if err == nil || !strings.Contains(err.Error(), "Accessibility") {
		t.Fatalf("错误应可解释，得到 %v", err)
	}
}

type panicAdapter struct{ MockAdapter }

func (*panicAdapter) Screenshot(context.Context, string) (ScreenshotPayload, error) {
	panic("adapter failure")
}
func TestLocalAdapterPanicReturnsError(t *testing.T) {
	result := make(chan Frame, 1)
	conn := &LocalConn{Adapter: &panicAdapter{}}
	conn.SetDeliver(func(f Frame) { result <- f })
	if err := conn.Send(Frame{Type: FrameCommand, ID: "panic-command", Op: OpWindowScreenshot}); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-result:
		if reply.Type != FrameResult || reply.ID != "panic-command" || reply.Code != CodeHelper {
			t.Fatalf("unexpected reply: %+v", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("panic must return an error instead of leaving the command pending")
	}
}
