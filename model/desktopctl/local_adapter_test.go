// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"testing"
	"time"
)

func TestAttachLocalMockNoDisplay(t *testing.T) {
	registry := NewRegistry(context.Background(), &memoryStore{})
	if _, err := registry.SetPolicy(context.Background(), enabledPolicy()); err != nil {
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
	result, err := hub.Dispatch(context.Background(), Command{Op: OpWindowsList})
	if err != nil {
		t.Fatalf("list：%v", err)
	}
	if len(result.Data) == 0 {
		t.Fatal("应有窗口清单")
	}
	if _, err := hub.Dispatch(context.Background(), Command{Op: OpWindowScreenshot, WindowID: "1"}); err != nil {
		t.Fatalf("screenshot：%v", err)
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
