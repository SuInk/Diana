// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"testing"
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
