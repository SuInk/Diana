// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"testing"
	"time"
)

func TestWarmBrowserIdleShutdownAndRelaunch(t *testing.T) {
	w := &warmBrowser{idle: 10 * time.Millisecond}
	session := &warmBrowserSession{root: context.Background(), cancel: func() {}, process: &sandboxedChromeProcess{cancel: func() {}, done: make(chan error)}}
	w.session, w.users = session, 1
	w.release(session)
	time.Sleep(50 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.session != nil || w.users != 0 {
		t.Fatalf("idle browser kept: session=%v users=%d", w.session, w.users)
	}
}

func TestWarmBrowserDoesNotReuseDifferentActiveConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, executable string
		window           BrowserWindow
	}{
		{"executable", "other-chrome", BrowserWindowVisible},
		{"window", "chrome", BrowserWindowHidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &warmBrowserSession{executable: "chrome", requestedWindow: BrowserWindowVisible, root: context.Background(), process: &sandboxedChromeProcess{done: make(chan error)}}
			w := &warmBrowser{session: session, users: 1}
			got, release, err := w.acquire(context.Background(), tc.executable, SandboxedBrowserConfig{Window: tc.window})
			if err == nil || got != nil || release != nil || w.users != 1 {
				t.Fatalf("incompatible browser reused: session=%v users=%d err=%v", got, w.users, err)
			}
		})
	}
}
